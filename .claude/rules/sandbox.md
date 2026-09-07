---
paths:
  - internal/sandbox/**
  - internal/daemon/sandbox*.go
  - internal/tui/sandbox*.go
---

# Docker sandbox panes

Design doc: `docs/superpowers/specs/2026-09-07-docker-sandbox-design.md`. It
records four revisions, each after a review found a fatal flaw in the previous
one — a cross-pane escape, a mount set that could not `git add`, and a `/repo`
mount that exposed the user's whole main checkout. Read its "What the earlier
versions got wrong" section before changing the mount set.

## The mount set is the security boundary

`internal/sandbox` is pure path arithmetic except `docker.go`, deliberately:
`dev.sh test` runs the suite inside a container with no Docker, and a boundary
that needs a daemon to test is a boundary nobody tests. Every rule below is
pinned by a table test.

**Only the repository's `.git` enters the container, never the main checkout's
working tree.** Mounting the checkout root was measured handing the agent
`cat /repo/.env` → `SECRET=hunter2`. It also removes the nested-worktree
exposure by construction.

**`/repo/.git/objects` is READ-ONLY.** New objects go to the pane's own store
via `GIT_OBJECT_DIRECTORY`, with the repository's store as a read-only
alternate. Objects are every commit for every branch, so a writable store makes
one `rm -rf` an unrecoverable loss of the repository's local history rather
than the sandboxed worktree's.

**Exactly four things are writable, and each has a reason.** `refs`, `logs` and
`worktrees/<name>` because a linked worktree keeps its index and HEAD in the
MAIN repository — without them `git add` answers
`Unable to create '.../index.lock': Read-only file system`. Everything else in
`.git` stays read-only because **host git EXECUTES `.git/hooks` and honours
`.git/config`**: a container that can write them has host code execution,
measured via `core.fsmonitor`.

**Both `objects/info` directories are shadowed with an empty read-only mount.**
Git follows `info/alternates` and `info/commit-graph` transitively. The `/repo`
side carries the line Quil itself writes in host path form, which container git
cannot normalise — it printed `error: unable to normalize alternate object
path` on EVERY git invocation inside the container. The `/quil` side is
agent-writable and is what the host's own line points at.

**The two gitdir overlays live outside every mounted directory.** A read-only
flag is per mount point, so an overlay reachable read-write elsewhere is
rewritable by the agent — and rewriting `admin-gitdir` lets `git worktree
prune` inside the container delete the host's worktree registration, taking
uncommitted work with it. v2 put them inside the pane's own root and the agent
rewrote them through the other path.

**Container-side paths are `/`-joined, never `filepath.Join`.** On a Windows
daemon that emits backslashes. `RunArgs` takes `hostGOOS` as a parameter rather
than reading `runtime.GOOS` so both shapes are testable on Linux CI.

## The object store, and the one ordering rule

Teardown is: **harvest → kill the container → remove the alternates line →
remove the per-pane tree → remove the worktree.** Every step depends on the one
before. Harvesting after the line is gone loses the commits; deleting the tree
while the container lives lets the container recreate it; removing the worktree
while the container holds the bind mount fails, with three retries at 250 ms.

**The harvest copies only `??/` shards and `pack/pack-*`, never `info/`.** A
plain copy of the store was measured dragging an agent-written `commit-graph`
into the user's real object store after a container-side `git repack -a -d`.
Note `git gc` FAILS in the container (no `gc.pid.lock` on the read-only mount)
but `git repack -a -d` SUCCEEDS, so packs are a real case.

**The alternates line is in the host's NATIVE path form.** An MSYS-style
`/c/...` makes git answer `unable to normalize alternate object path` on every
command in the repository.

**Removal deletes only this pane's line.** Two sandbox panes on one repository
write two lines, and the user may have one of their own.

A dangling line makes EVERY git command in that repository fail. The registry
at `$QUIL_HOME/sandbox/alternates.json` records which repository each pane
wrote into, so the startup repair knows where to look. It is best-effort: the
registry lives in `$QUIL_HOME` too, so `reset-daemon` defeats it, and the docs
carry the one-line manual fix.

## Refusals that must never soften

A sandbox pane **never falls back to a host spawn**. An unavailable engine and
an unsafe mapping both fail the spawn with `SpawnError`. A pane the user asked
to isolate must not quietly run unisolated.

`NewMapping` refuses when `$QUIL_HOME` lies inside a path it would mount — the
dev build puts it inside the checkout, so sandboxing a quil worktree would
mount every pane's settings file and, on Linux, the daemon socket. Containment
is tested on **symlink-resolved** paths: a junction defeats a string prefix
test.

The persisted pane type carries a `sandbox/` prefix. Auto-update has a rollback
path, and a daemon too old to read `sandbox_image` would otherwise restore the
pane as an ordinary one pointed at the worktree — an agent on the host, with no
error anywhere. An unknown type takes the existing fallback to `terminal`.

A **claude-code** pane refuses to spawn without a hook binary. Running hookless
is not a degraded mode there: with no `.id` record the next restart passes
`--session-id` for an id whose transcript exists, which claude refuses with
exit 129.

## Everything that reads agent-written content goes through `os.Root`

The pane's tree is written by a process inside the container. On a Linux host —
including the remote-daemon case — a symlink planted there resolves on the
HOST. Measured: `ln -s /etc/passwd` inside the container succeeded on the bind
mount and a host-side reader printed the host's own file. **On Windows the same
link is an inert reparse point, so a Windows-only test never sees this.**

The harvest and the spool forwarder both open through `os.Root`. Any new
daemon-side read under `$QUIL_HOME/sandbox/panes/<id>` owes the same.

## Hooks, and the three GOOS reads

`hookPaths` separates "where to WRITE this file" from "what to CALL it". Same
directory for a host pane, two different ones for a sandbox pane — conflating
them hands the container a `--settings C:\Users\...` it cannot open.

Codex takes the child's OS explicitly and it reaches **three** independent
decisions: `HookCommandFor` (the shell spelling, which is hashed into the trust
key), `ConfigOverrideArgs`'s GOOS argument, and the shim check. Switching fewer
than all three leaves codex prompting for trust on every pane.

The spool forwarder respects four `Spool` invariants a naive copy breaks: it
opens and closes per batch (a long-lived handle strands the file on Windows),
caps bytes per pass, never replays from offset zero on restart, and recovers
from truncation.

## Scoping and labels

Every container carries `quil.pane` AND `quil.home`. Docker labels are
engine-wide and the dev and production daemons share one engine, so a sweep on
`quil.pane` alone would have a dev daemon force-remove every production sandbox
container — the always-on `dev-environment.md` rule. `quil.home` is
canonicalised before hashing, because `QuilDir()` returns `$QUIL_HOME`
verbatim.

The sweep command is `docker ps -a --filter … --format …`, **without `-q`**:
`docker ps -aq --format` prints `WARNING: Ignoring custom format` and returns
container ids.

## Shutdown

`docker kill` in parallel under a 2 s budget, never `docker stop`. Stop's grace
is ten seconds per container and claude as PID 1 ignores SIGTERM, while the
daemon's whole budget is five seconds before it is SIGKILLed. Killing the
docker CLI does **not** stop its container, so leaving them running would leave
agents nothing can reach or stop.

## Still unmeasured

Two items block a confident ship and are marked in the design doc: resize
propagation through `docker run -it` under ConPTY (the one area of this codebase
with documented irreversible damage), and the default in-container sign-in.
