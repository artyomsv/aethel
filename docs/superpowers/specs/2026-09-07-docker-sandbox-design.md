# Docker sandbox panes — design

**Status:** design v4 — reviewed and cleared to implement. Written 2026-09-07 and
revised three times the same day: v1 shipped a sandbox escape, v2 shipped a mount
set that could not `git commit`, v3 mounted the user's whole main checkout
(secrets included) into the container. Do not implement from memory of any of them.

Open an AI pane (claude-code, opencode, codex) inside a local Docker container
with a git worktree bind-mounted into it, so the agent's filesystem reach is
bounded by the container rather than by the host.

Everything labelled **measured** was run against a real container on this machine
(Docker 29.6.2, linux/x86_64) on 2026-09-07. Two things remain unmeasured and
block shipping — see [Must be measured before shipping](#must-be-measured-before-shipping).

## What the earlier versions got wrong

Recorded so none of it is reintroduced.

**v1:**

1. Mounting `$QUIL_HOME/sessions` read-write was **host code execution**: that
   directory holds `<paneID>.settings.json`, a list of shell commands Claude Code
   runs, so a sandboxed agent could write another pane's file and have the host
   run it.
2. `git worktree prune` and `repair` inside the container **destroyed the host
   worktree**, because the main repo's admin `gitdir` still named the host path.
3. Resume was claimed to need no rewrite layer. It does.
4. The `destAvail` precedent was cited backwards — it is remote-only and falls
   back to local detection.
5. Windows CRLF made every file read as modified inside the container.
6. The container sweep would let a **dev daemon kill production containers**.

**v2:**

7. **The mount set could not commit.** `/repo` was made read-only to stop `prune`,
   which also locked out `git add` — a worktree's index lives in the main repo.
   Measured: `fatal: Unable to create '/repo/.git/worktrees/wt/index.lock':
   Read-only file system`, rc=128.
8. The fallback v2 offered for that ("make the rest of `/repo/.git` writable") was
   itself **host code execution**: `.git/hooks` and `.git/config` are executed by
   host git. Measured — a container wrote `core.fsmonitor=/tmp/x` and the host's
   next `git status` tried to exec it.
9. **The two git overlays were writable by the agent**, because they lived inside
   the read-write per-pane mount. Rewriting `admin-gitdir` re-opened bug 2.
10. The escape came back in the **dev layout**: `quil-dev.exe` puts `$QUIL_HOME`
    *inside* the checkout, so sandboxing a quil worktree mounted the whole dev
    `$QUIL_HOME` — every pane's settings file, and on Linux the daemon socket.
11. The shared Claude config dir was a **cross-pane channel** of the same class as
    bug 1.
12. A **new tab + worktree + sandbox spawned the agent on the host**, because
    `FirstPaneSpec` carried no sandbox field.
13. Putting the pane id in the slug **broke the resume picker** and rested on a
    false premise (transcripts are per session, not per directory).

**v3:**

14. **`/repo` mounted the main checkout root**, giving the agent read access to the
    user's entire primary source tree and any secrets in it. Measured:
    `cat /repo/.env` → `SECRET=hunter2`.
15. The harvest was specified as "a plain no-clobber file copy", which drags an
    agent-controlled `objects/info/` — `commit-graph`, `alternates` — into the
    user's real object store.
16. Neither `objects/info` directory was shadowed, so **every** container git
    command printed `error: unable to normalize alternate object path`, and the
    agent could poison the store the host's alternates line points at.
17. `git repack -a -d` succeeds in the container, so "gc fails, objects stay loose"
    was not the guarantee it was written as.
18. The `NewMapping` refusals compared cleaned paths, which a junction defeats.
19. The codex GOOS fix named one of **three** independent `runtime.GOOS` reads.

## Why bind-mount, not clone

**Clone inside the container** was rejected. The work would be trapped: the only
way out is `git push`, which puts the user's SSH key or token inside the sandbox —
the thing Anthropic's dev-container guidance explicitly warns against. It also
blinds every host-side git feature Quil ships (`internal/daemon/worktree_status.go`,
the sidebar git state) and copies the whole history per pane.

**Bind-mount the existing worktree** was adopted. The obstacle is that a linked
worktree's `.git` is a FILE holding an absolute host path:

```
gitdir: E:/Projects/Stukans/quil/.git/worktrees/feat-docker-sandbox
```

Measured, in a real container:

| Mount | `git status` inside |
|---|---|
| worktree alone | `fatal: not a git repository: (null)` |
| worktree + main repo, side by side | `fatal: not a git repository: (null)` |
| worktree + main repo + a `.git` file overlaid with a container path | `## feat/abs` ✅ |
| worktree created with `git worktree add --relative-paths` | `## feat/rel` ✅ |

The **overlay** is chosen over `--relative-paths`, which writes
`extensions.relativeWorktrees` into the user's repository and breaks older git and
every libgit2-based tool on the host.

## Git: the mount set and the object store

This section is the heart of the design and the part both earlier versions got
wrong. Every claim in it is measured.

### Why the main repo cannot simply be read-only

A linked worktree keeps its index, HEAD, logs and lock files in
`<main>/.git/worktrees/<name>/`, not in the worktree directory. With `/repo`
read-only the agent can edit files and never save them:

```
$ git add b.txt
fatal: Unable to create '/repo/.git/worktrees/wt/index.lock': Read-only file system
ADD rc=128     COMMIT rc=128
```

Making the rest of `/repo/.git` writable to fix that is worse: host git executes
`.git/hooks/*` and honours `.git/config`. Measured — a container wrote
`core.fsmonitor=/tmp/x` into `/repo/.git/config`, and the host's next
`git -C wt status` answered `fatal: cannot exec '/tmp/x'`.

So the writable set must be **exactly** what committing needs, and nothing that
host git executes.

### Only `.git` enters the container, never the main working tree

`/repo` is **`<main>/.git`**, not the main checkout root. Mounting the root exposes
the user's entire primary source tree read-only — every file, uncommitted work, and
any `.env` or credential file sitting in it. Measured against v3's own mount set:

```
$ cat /repo/.env
SECRET=hunter2
```

The container needs git metadata, not a second copy of the user's source. Measured
with `<main>/.git → /repo/.git:ro`: `ls -a /repo` shows only `.git`, `cat /repo/.env`
fails, and `git status`, `git log` through the alternate and `git commit` all still
work.

This also removes the nested-worktree exposure by construction — another worktree's
working directory is not under `.git` — so the `NewMapping` refusal for it is
belt-and-suspenders rather than the primary defence.

### The writable set

`/repo/.git` is mounted read-only, with four narrow read-write sub-mounts:

| Path | Why it must be writable |
|---|---|
| `/repo/.git/refs` | the branch pointer the commit moves |
| `/repo/.git/logs` | the reflog that commit writes |
| `/repo/.git/worktrees/<name>` | this worktree's index, HEAD and lock files |
| — | **`.git/objects` stays READ-ONLY**, see below |

Everything else in `.git` stays read-only, and the reason is specific rather than
conservative: **`.git/hooks` and `.git/config` are executed by host git**, and
`.git/worktrees/<other>` belongs to a different pane.

### Both `objects/info` directories are shadowed read-only

Git reads `info/alternates` and `info/commit-graph` from an object store
**transitively**, and two of those directories are in the wrong hands.

`/repo/.git/objects/info/alternates` is the line **Quil itself writes**, in the
host's native path form — which container git then tries to follow as a nested
alternate. Measured, on every single git invocation inside the container:

```
error: unable to normalize alternate object path:
       /repo/.git/objects/C:/Users/.../panes/objects
```

That is exactly the "agent burns context on phantom problems" outcome the CRLF
section exists to prevent, and v3's own measurements missed it because they wrote
the line and then re-tested only the host.

`/quil/objects/info/` is **agent-writable**, and the host's live alternates line
points at that store — so a planted `info/alternates` there makes host git print
`error: object directory /… does not exist` on every command.

Both are shadowed with an empty read-only directory mount. Measured with both in
place: the container's noise is gone, history still reads through the alternate,
`git status` and `git commit` still work (`COMMIT rc=0`), the host sees the commit,
host `git fsck` is silent, and the agent's write is refused —
`sh: can't create /quil/objects/info/alternates: Read-only file system`.

### The harvest copies objects, never `info/`

"A plain no-clobber copy of the store" is wrong and would silently corrupt the
user's repository. The harvest copies **only** the sharded `??/` loose-object
directories and `pack/pack-*.{pack,idx,rev}`. It never copies `info/`.

Measured: after a container-side `git repack -a -d` the store held `info/packs` and
a real `info/commit-graph`, and a naive `cp -rn` moved both into the user's real
object store — where an agent-planted `commit-graph` is trusted ancestry and an
agent-planted `info/alternates` breaks every host git command.

The name filter also handles two lesser cases for free: in-progress `tmp_obj_*`
files are skipped, and since git writes objects and packs atomically (temp +
rename) a well-formed name is always a complete file, so the harvest needs no
locking against a live container.

**Container-side `git repack -a -d` SUCCEEDS**, unlike `git gc` — it does not take
`gc.pid.lock`. Measured: `repack rc=0`, loose objects replaced by a pack. So the
harvest must handle packs, which the `pack-*` rule does, and the "objects stay
loose" reasoning is a convenience rather than a guarantee. The docs advise against
running `git gc`, `git repack` **and** `git commit-graph write` in a sandbox pane.

### The object store is per-pane (option B)

New objects are written to a private per-pane store, with the repository's own
object store as a **read-only** alternate:

```
GIT_OBJECT_DIRECTORY=/quil/objects
GIT_ALTERNATE_OBJECT_DIRECTORIES=/repo/.git/objects
```

Measured: `ADD rc=0`, `COMMIT rc=0`, and `git cat-file -p HEAD:a.txt` read history
through the alternate correctly.

The point is that `/repo/.git/objects` **stays read-only**. Objects are the actual
content of the repository — every commit, tree and blob for every branch. With a
writable object store, one `rm -rf` inside a sandbox destroys the entire
repository's local history, not just the sandboxed worktree's. Read-only removes
that outcome entirely.

What remains shared, and is accepted: `refs` is writable, so a sandbox pane can
move or delete any branch including `main`. That damage is recoverable — the
objects survive, and `git reflog` / `git fsck` find them.

### The cost: the host cannot see the commit until Quil says where to look

The new objects live in a directory the repository does not know about, so during
a live session the worktree is broken on the host. Measured immediately after a
container commit:

```
git -C wt status   →  fatal: bad object HEAD
git -C main fsck   →  error: refs/heads/feat/x: invalid sha1 pointer c40ad05...
```

`git status` on that worktree is exactly what `internal/daemon/worktree_status.go`
runs for the close dialog's uncommitted-work warning. The main checkout is
unaffected — `git -C main status` and `log` stayed correct.

**Quil therefore writes one line into the repository**, at pane spawn:

```
<main>/.git/objects/info/alternates   ←   <host path of the per-pane object store>
```

Measured with it in place: `git status` → `## feat/x`, `git log` shows the
container's commit, `git fsck` is silent. The path must be in the host's **native**
form (`C:/...` on Windows) — a `/c/...` MSYS-style path gives
`error: unable to normalize alternate object path` on every git command.

### Harvest, and the order that matters

`git gc` on the host does **not** absorb an alternate's objects. Measured: after a
host `gc`, deleting the per-pane store still broke the repository. The objects must
be copied deliberately.

They can be: git objects are content-addressed and immutable, so a plain
no-clobber file copy is correct and idempotent. Measured — copy, remove the
alternates line, delete the store, and the host repository is fully intact:
`git status` clean, `git log` shows the commit, `git fsck` silent.

**The order is load-bearing.** Harvest → remove the line → delete the store. A
crash after the copy leaves a stale store (wasted disk, harmless). A delete before
the copy loses the commit.

**Harvest continuously, not only at teardown** — every 30 s and on pane idle, plus
once at teardown. The copy is small (only the session's new loose objects) and it
means almost nothing is ever stranded if the daemon dies.

Container-side `git gc` fails and that is fine: it cannot create
`/repo/.git/gc.pid.lock` on the read-only mount, so objects stay loose and the
harvest stays a file copy. If a future flow does produce a pack, the same copy
handles it — pack filenames are content-derived. Document that the agent should
not run `git gc`.

One harmless line appears on every container commit and must not be treated as an
error: `error: Unable to create '/repo/.git/packed-refs.lock': Read-only file
system`. The commit succeeds; git is only declining to opportunistically pack refs.

### The stale-pointer hazard

A pointer line whose target no longer exists makes **every** git command in that
repository fail:

```
error: object directory ...\sandbox\panes\<id>\objects does not exist;
       check .git/objects/info/alternates
```

Normal teardown removes the line before the store. The real hazard is anything
that wipes `$QUIL_HOME` from outside — `reset-daemon.ps1` does exactly that.

Two mitigations, both required:

1. **Startup repair.** On daemon start, and before any sandbox spawn, strip
   alternates lines that point inside `$QUIL_HOME/sandbox` and no longer resolve.
   The daemon records which repositories it has written into, at
   `$QUIL_HOME/sandbox/alternates.json`, so it knows where to look.
2. **Honest documentation.** The record lives in `$QUIL_HOME`, so a wiped
   `$QUIL_HOME` defeats the repair. The docs must state the one-line manual fix:
   delete the offending line from `<repo>/.git/objects/info/alternates`.

Two sandbox panes on one repository write **two lines**. Removal must delete only
the pane's own line, never rewrite the file wholesale — another pane's line, or a
line the user put there, must survive.

### Author identity

Measured: without one, the commit fails with `Author identity unknown`.
`user.name` and `user.email` live in the host's global git config, which is not
mounted. The daemon reads them with `git config --get` and passes them as
`GIT_AUTHOR_*` / `GIT_COMMITTER_*`. If the host has none, the pane still spawns and
the docs say the image must set them.

### Non-root and line endings

A non-root container user hits `fatal: detected dubious ownership` — and non-root
is required, because Claude Code refuses `--dangerously-skip-permissions` as root.

On a Windows host `core.autocrlf` lives in the user's global config, which the
container does not mount, so container git compares CRLF working files against LF
blobs and reports the whole tree as modified. Measured on a file the container
never touched:

```
== status ==     ## feat/x
                  M a.txt
host bytes:      l i n e 1 \r \n l i n e 2 \r \n
```

The agent then burns context on phantom changes, or commits a whole-repo
line-ending rewrite. Quil's own repo is immune (`.gitattributes`, `eol=lf`); a
user's repo generally is not.

Both fixed by environment, measured working. The set is **host-OS dependent** and
`internal/sandbox`'s table tests must cover both shapes — CI runs on Linux, so a
Windows-only shape is otherwise never built:

```
GIT_CONFIG_KEY_0=safe.directory   VALUE_0=*
GIT_CONFIG_KEY_1=core.autocrlf    VALUE_1=true      # Windows host only
GIT_CONFIG_KEY_2=user.name        VALUE_2=<from host git config>
GIT_CONFIG_KEY_3=user.email       VALUE_3=<from host git config>
```

### The two overlay files live outside every mounted directory

The `.git` overlay and the admin-`gitdir` overlay are read-only **mounts**, but a
read-only flag is per mount point: the same inode reachable read-write elsewhere is
writable. v2 put both files inside the read-write per-pane root, so the agent
rewrote them through their other path — measured, `cat /work/w/.git` →
`gitdir: /quil/evil` — and with the admin file rewritten, `git worktree prune`
destroyed the host worktree exactly as in v1.

They therefore live in `$QUIL_HOME/sandbox/overlays/<paneID>/`, a directory that is
**never mounted as a directory**. Only the two files are mounted, read-only, and
a bind-mounted file cannot be unlinked or replaced from inside (measured:
`rm: can't remove '/work/w/.git': Resource busy`).

Residual, and it must be stated: the pane's own admin directory is writable, so the
agent can delete **or rewrite** its own `commondir`, `HEAD` or `index` — only
`gitdir` is overlaid. Blast radius equals `rm -rf /work/<slug>` — its own worktree.
But the close dialog then sees `fatal: not a git repository` and must not report
nothing to lose, or the user deletes real work silently. **Confirm** (do not
rebuild) that the existing error-as-reason path covers this:
`internal/daemon/worktree_status.go:185-192` already reports a git failure as
`st.Error` rather than a zero count, and a sandbox worktree is worktree-owned so it
passes the owned filter. One test asserting the reason surfaces.

## Refusing an unsafe mapping

`NewMapping` returns an error — surfaced as `SpawnError`, **never a host spawn** —
rather than producing a mapping that would defeat the sandbox.

**Every containment test resolves symlinks first.** A cleaned absolute path is not
a real path: on Windows a junction (`C:\dev` → `…\.quil\…`) makes `$QUIL_HOME`
physically reside under the worktree while a string prefix test says it does not.
Run `filepath.EvalSymlinks` on both sides before comparing, as `defaultCWD` already
does for the same class of check.

**`$QUIL_HOME` inside a mounted path.** The dev build sets
`QUIL_HOME` to `<exe dir>/.quil` (`cmd/quil/main.go:76`, `cmd/quild/main.go:76`),
so sandboxing a quil worktree would mount the whole dev `$QUIL_HOME` read-write at
`/work/<slug>/.quil`: every host pane's `settings.json`, `workspace.json`, all
history, and on Linux `quild.sock`, an AF_UNIX socket the container can `connect()`
to for full daemon control. Refuse when the cleaned, absolute,
case-folded-on-Windows `config.QuilDir()` lies inside any host path the mapping
would mount — the worktree or the main root.

**Nested worktrees under the main root.** Now belt-and-suspenders rather than the
primary defence: mounting `<main>/.git` instead of the checkout root already keeps
every other worktree's working directory out of the container. Keep the refusal
anyway, so a future mount-set change cannot silently re-open it.

**A subdirectory of a worktree.** v2's `NewMapping` read `.git` in `hostCWD`, so a
pane created after browsing into `wt/src` would mount `src` alone and answer
`fatal: not a git repository` inside. Resolve with
`git rev-parse --show-toplevel --git-common-dir` — the daemon already shells to git
in `internal/gitworktree` — mount the toplevel, and set `-w` to the subdirectory.

One table-test row per case.

## Hooks

Quil's notification sidebar, work-in-progress spinner, input history and
session-id tracking are hook-driven. The hook is not a network client —
`cmd/quild/hook.go:16` states it "deliberately does NOT initialize the daemon,
logger, or config-from-disk". It reads JSON on stdin, reads five environment
variables, and appends files under `$QUIL_HOOK_HOME`.

Measured: a cross-built `quild-linux-amd64`, mounted into an Alpine container and
run as uid 1000, wrote `sessions/<paneid>.id`, `events/<paneid>.jsonl` and
`history/<paneid>.jsonl` to the host. The events line is exactly what the daemon's
spool watcher consumes. **No change to the hook binary is needed.**

### One per-pane root, never the shared directories

v1 mounted `$QUIL_HOME/{sessions,events,history,claudehook}` read-write. That was
the escape. Each holds data for **every** pane on the machine:

- `sessions/<paneID>.settings.json` is a list of shell commands the host runs →
  cross-pane host code execution.
- `history/*.jsonl` is every prompt typed into every AI pane in every project →
  confidentiality break, plus injected text shown in another pane's history picker
  as something the user typed.
- `events/*.jsonl` → cross-pane forgery; attacker-chosen text rendered in the
  sidebar and in Windows toasts, which nothing sanitises (`sanitizeRemoteText`
  covers remote-daemon strings only); and a daemon DoS, since `Spool.Tick` walks
  every `*.jsonl` on a 200 ms ticker (`internal/hookevents/spool.go:150-215`) and
  `safePaneID` bounds the name but not the count.
- `claudehook/hook.log` → log poisoning and unbounded disk fill. Nothing executes
  from there today, but `internal/config/config.go:875-877` still describes it as
  holding hook *scripts*, which is stale and would mislead anyone sizing the blast
  radius. Correct that comment as part of this work.

The hook derives every path from one environment variable
(`cmd/quild/hook.go:55`), so **one mount replaces all four**:

```
$QUIL_HOME/sandbox/panes/<paneID>  →  /quil   rw
QUIL_HOOK_HOME=/quil
```

The hook writes `/quil/sessions/<paneID>.id`, `/quil/events/<paneID>.jsonl`,
`/quil/history/<paneID>.jsonl` and `/quil/claudehook/hook.log` — all inside the
pane's own host subtree, at the container paths it already builds. **No hook
change.** The per-pane object store is `/quil/objects`, in the same root.

**Do NOT use single-file bind mounts instead.** A file bind mount is pinned to the
inode, and `Spool.Init` unlinks every `*.jsonl` at daemon start
(`internal/hookevents/spool.go:139`) while `Spool.Cleanup` unlinks on pane destroy
(`:414`). After an unlink the container keeps writing to the orphaned inode and the
daemon sees an empty file — events lost silently, with no error anywhere.

### Every daemon access under the per-pane root goes through `os.Root`

The tree is now writable by an untrusted process, and on Linux — including the
remote-daemon case this design supports — a symlink planted inside it resolves on
the **host**. Measured: `ln -s /etc/passwd /quil/sessions/<id>.transcript` was
created successfully, and a host-side reader printed the host's `/etc/passwd`.
`rm -r /quil/sessions; ln -s ../../../sessions /quil/sessions` would redirect the
daemon's own `WriteSettingsFile` into the real shared `$QUIL_HOME/sessions/`.

On Windows the same link is an inert reparse point, so **a Windows-only
measurement never sees this.**

Every daemon read and write under `$QUIL_HOME/sandbox/panes/<paneID>` uses
`os.OpenRoot` (Go 1.25 per `go.mod`; `os.Root` refuses symlink and `..` escapes):
the forwarder, `ReadPersistedSession`, `WriteSettingsFile`, `panehistory`, the
harvest, and cleanup. `internal/panehistory/history.go:167` already has a
`rejectSymlink` for one of these files — this generalises it. One Linux-CI test
with a symlinked `sessions/`.

### The spool forwarder

The per-pane root moves the event file, so the daemon needs a forwarder appending
new lines from `$QUIL_HOME/sandbox/panes/<paneID>/events/<paneID>.jsonl` into the
real `$QUIL_HOME/events/<paneID>.jsonl`. A forwarder beats a second `Spool` because
it keeps one ingest path — but a naive one breaks three documented `Spool`
invariants:

- **It must open, append and close per batch.** `internal/hookevents/spool.go:210-219`
  warns that "a LONG-LIVED producer holding the spool handle open would be
  stranded" — on Windows, NTFS reports a stale directory-entry size and `Tick`
  skips the file, so events stop arriving with no error.
- **It must cap bytes**, per tick and per pane, with one sidebar event on overflow.
  `Spool` caps a line (`:330`) but not a file, and rotates only its own file and
  only when idle (`:259-266`). An agent writing 1 GB would have it copied to host
  disk.
- **It must not replay on restart.** `Spool.Init` unlinks precisely to avoid replay
  (`:96-111`). The forwarder must unlink-after-drain on start and treat
  `size < offset` as `Spool` does, or a daemon restart replays the pane's entire
  event history into the sidebar — and the agent can truncate the file underneath
  it deliberately.
- **It must restamp `ts_ms`.** The hook stamps the **container** clock, which
  drifts across a WSL2 sleep, and `spoolIsFresh` compares it against a host mtime.

### Resume needs the hook, so a hookless sandbox pane must not spawn

v2 said a failed quild fetch degrades to "runs without hooks". For a claude pane
that is not a degradation, it is a delayed crash: with no `.id` record a restart
takes the `hadSession` branch with `hookID == ""`, nothing is located, and the
fresh-start args pass `--session-id <existing id>` — which claude refuses once the
transcript exists, exit 129 (`internal/daemon/daemon.go:4370-4376`, `:4797-4809`).

A sandbox claude pane with no hook binary therefore **refuses to spawn**, with
`SpawnError` "hook binary unavailable — retry with Alt+R". Non-claude sandbox panes
may proceed hookless with a warning and one sidebar event.

### Container user identity

On a Linux host the daemon creates the per-pane root 0700 as the host user
(`internal/claudehook/claudehook.go:266`), while the container user is whatever the
image sets. A mismatch means `RunHook` cannot write and its errors are swallowed
(`cmd/quild/hook.go:16-17`), and claude cannot persist its sign-in.

Docker Desktop on Windows ignores uids, so **the "ran as uid 1000, wrote to the
host" measurement above proves nothing for a Linux host** — which is the
remote-daemon case. On a Linux host Quil passes a Quil-chosen
`--user <daemon uid>:<gid>` (never user-supplied). Add the Linux shape to the
`RunArgs` table test and measure it on the remote VM.

### opencode and codex

Both need more than a path mapping.

**opencode** loads a plugin script from `filepath.Join(quilDir, "opencodehook", ...)`
(`internal/opencodehook/opencodehook.go:152-154`) and embeds its absolute path into
`OPENCODE_CONFIG_CONTENT`, refusing a relative one (`:174-181`). Copy the script
into the per-pane root at spawn and embed the container path.

**codex** takes the daemon's OS: `internal/daemon/daemon.go:4117` passes
`runtime.GOOS` into `codexhook.ConfigOverrideArgs`. On a Windows daemon that emits
the PowerShell spelling `& $env:QUIL_HOOK_EXE codex-hook`
(`internal/codexhook/codexhook.go:74`) and a `C:\...` session-flags path (`:148`) —
and the second is half the trust key, so codex finds no matching `trusted_hash` and
prompts on every pane.

**There are THREE independent GOOS reads, and all three must be routed**, not just
the one parameter: `codexhook.HookCommand()` reads `runtime.GOOS` internally
(`internal/codexhook/codexhook.go:69` → `hookCommandFor`), `daemon.go:4117` passes
`runtime.GOOS` to `ConfigOverrideArgs`, and `sessionFlagsPath(goos)` (`:148`) builds
the trust path. Switching only the `ConfigOverrideArgs` argument still leaves the
PowerShell command spelling in the hashed body, so the key still mismatches and
codex still prompts — the exact symptom this section exists to remove.

Verified, and worth keeping: containerising does **not** break the codex trust hash
itself. The hash covers a constant command naming `$QUIL_HOOK_EXE`
(`internal/codexhook/codexhook.go:68`, `:165-183`), never a path.

Also host-shaped and needing the container's rules: `codexhook.IsShim` and the
`exec.LookPath` at `internal/daemon/daemon.go:4099` inspect a host binary, and
`pathIsArgvSafe` (`internal/claudehook/claudehook.go:263`) checks metacharacters
chosen for the host shell.

**Cheap test that catches the whole class:** assert a sandbox codex pane's argv
contains no backslash and no `.exe`.

## Resume

**v1 was wrong.** The daemon does not build the transcript path from
`configDir + cwd`. It uses the path the **hook wrote** and probes it on the host:

```
internal/daemon/daemon.go:4477  add(rec.ID, "hook", rec.TranscriptPath)
internal/daemon/daemon.go:4499  transcriptExistsFn(transcript)
```

Inside a container that is a container path, which does not exist on the host. The
probe answers `candidateMissing`, `locatedOwnSession` returns false, and the pane
spawns with `--session-id <id>` — which claude refuses once the transcript exists,
exit 129. Every Alt+R and every restore of a sandbox claude pane would die on the
error screen: the exact bug `locatedOwnSession` was written to fix.

**A container→host rewrite is required**, applied before `transcriptState`
(`internal/daemon/daemon.go:4494`). It is a prefix swap — but a **validating** one.
The sidecar path is agent-written and `transcriptExistsFn` is
`statExistsWithinBudget` (`internal/daemon/daemon.go:4054`), which stats on the
host and spends a blocking-FS permit: an unvalidated path is an existence oracle
for any host path ending `<id>.jsonl`, and a permit burned on a dead mount every
restart. Rewrite **only** a path starting with the pane's own container config
prefix whose cleaned result stays under the pane's host config dir. Anything else
is `candidateUnknown`.

The session **picker** is wrong the same way. `internal/daemon/claudesessions.go:98`
and `:223` call `claudesessions.List` / `ReadDetail`, which resolve the DAEMON's
`~/.claude` (`internal/claudesessions/claudesessions.go:214`, `:132`). For a sandbox
pane the picker would list host sessions and hand claude an id its own config dir
has never seen. The `In` variants (`ListIn`, `ProjectDirIn`, `TranscriptPathIn`)
exist but currently have **no production caller**; switch both seams to them and
pass the sandbox config dir.

`EscapeCWD` is platform-neutral (every non-alphanumeric becomes `-`), so
`/work/quil-a1b2c3d4` maps to `-work-quil-a1b2c3d4` on both sides. Note its >200
character truncation-plus-hash branch (`:105-117`) — irrelevant at these path
lengths, but a table-test row.

## Authentication

Quil never reads, copies, stores or refreshes a credential.

**The Claude config directory is per-pane** (`/quil/claude`, inside the per-pane
root), not shared. A shared one is a cross-pane channel of the same class as the
`sessions/` escape: `CLAUDE_CONFIG_DIR` holds user-scope `settings.json` (hooks),
`.claude.json` (`mcpServers`), `CLAUDE.md`, every transcript and `history.jsonl`.
Pane A could plant a `SessionStart` hook or an MCP server that pane B's claude runs
inside B's container, or append turns to B's transcript that load on B's `--resume`.

That makes the auth mode decide the sign-in cost:

**`auth = "token"` (recommended, and the documented multi-pane path).** The daemon
forwards `CLAUDE_CODE_OAUTH_TOKEN` from **its own environment**. No credential file
is needed, so a per-pane config dir costs nothing. This mirrors the Codespaces
secret Anthropic's dev-container page documents. It costs the pane Remote Control
and claude.ai connectors.

The token must **not** reach argv. `spawnPane` logs the full command line
(`internal/daemon/daemon.go:4909`) and that log is rendered by the F1 viewer, so
`-e NAME=value` would print the token. Pass a bare `-e CLAUDE_CODE_OAUTH_TOKEN` —
docker forwards it from its own environment — and set the value on the docker CLI
via `ptySession.SetEnv`. Test that `RunArgs` output never contains a `=` after that
key.

**`auth = ""` (default, browser sign-in).** The user signs in inside the container,
once per pane, into that pane's own config dir. Anthropic documents the fallback
for a callback that cannot reach a container: "copy the code shown in the browser
and paste it at the `Paste code here if prompted` prompt". **Unmeasured**, and it
is the default path, so it is on the must-measure list.

`[sandbox] shared_claude_config = true` opts into one sign-in for all sandbox panes.
It must say in the config comment and the docs: **all sandbox panes then share one
trust domain.**

Copying `~/.claude/.credentials.json` is deliberately NOT implemented: it appears
nowhere in Anthropic's documentation, reads as "collect, store, or intermediate …
session tokens" under the Authentication and credential use policy, cannot work on
a macOS host (Keychain, not a file), and whether a container's refresh invalidates
the host's refresh token is unverified. A support enquiry is outstanding.

**Quil publishes no image.** Running Claude Code "in your products or services
(e.g. in hosted sandboxes or other agent infrastructure)" triggers Commercial Terms
conditions. A user-supplied image means Quil pre-installs nothing and that section
never applies. This is why the image field is free text and not the dropdown
originally requested.

## Mount set

For a pane whose host CWD is inside a linked worktree:

| Host | Container | Mode |
|---|---|---|
| `<main>/.git` — **not the checkout root** | `/repo/.git` | ro |
| `<main>/.git/refs` | `/repo/.git/refs` | rw |
| `<main>/.git/logs` | `/repo/.git/logs` | rw |
| `<main>/.git/worktrees/<name>` | `/repo/.git/worktrees/<name>` | rw |
| `$QUIL_HOME/sandbox/empty` (always empty) | `/repo/.git/objects/info` | ro |
| `$QUIL_HOME/sandbox/empty` (always empty) | `/quil/objects/info` | ro |
| worktree toplevel | `/work/<slug>` | rw |
| `$QUIL_HOME/sandbox/overlays/<paneID>/dot-gitdir` | `/work/<slug>/.git` | ro |
| `$QUIL_HOME/sandbox/overlays/<paneID>/admin-gitdir` | `/repo/.git/worktrees/<name>/gitdir` | ro |
| `$QUIL_HOME/sandbox/panes/<paneID>` | `/quil` | rw |
| `$QUIL_HOME/sandbox/bin/<ver>/quild-linux-<arch>` | `/usr/local/bin/quild` | ro |

`/quil` contains `objects/`, `claude/`, `sessions/`, `events/`, `history/`,
`claudehook/` and the copied opencode plugin script.

For an ordinary checkout (not a linked worktree) the `/repo` rows and the two
overlays disappear: one rw mount of the toplevel at `/work/<slug>`, with `.git` a
real directory inside it. The per-pane object store still applies, with
`<toplevel>/.git/objects` as the read-only alternate, and both `objects/info`
shadows still apply — at `/work/<slug>/.git/objects/info` and `/quil/objects/info`.

**No shared `$QUIL_HOME` subdirectory is mounted, and `$QUIL_HOME` itself never
is** — enforced by the `NewMapping` refusal above rather than asserted.

Environment: `QUIL_PANE_ID`, `QUIL_HOOK_HOME=/quil`, `QUIL_HOOK_MODE`,
`QUIL_RECORD_HISTORY`, `QUIL_HOOK_EXE=/usr/local/bin/quild` (codex),
`CLAUDE_CONFIG_DIR=/quil/claude`, `GIT_OBJECT_DIRECTORY=/quil/objects`,
`GIT_ALTERNATE_OBJECT_DIRECTORIES=/repo/.git/objects`, the host-OS-dependent
`GIT_CONFIG_*` set, and a bare `CLAUDE_CODE_OAUTH_TOKEN` under `auth = "token"`.

`internal/sandbox.RunArgs` must not use `filepath.Join` for the **container** side
of any path: on a Windows daemon that emits backslashes. Separators belong to the
machine holding the disk. Tests run on Linux CI, so a Windows-only separator bug is
caught only by tests constructing Windows inputs deliberately.

## Container lifecycle

`--rm` is not used, so a crashed sandbox leaves logs to diagnose
(`docker logs quil-<paneID>`).

| Event | Container | Rationale |
|---|---|---|
| TUI close | keeps running | The daemon keeps ordinary PTY children too. |
| Daemon shutdown | `docker kill`, all in parallel, ≤2 s total | See budget below. |
| Daemon crash | orphaned → startup sweep reaps | |
| Pane restart (Alt+R) | `docker rm -f`, then run | Same pane id, same container name. |
| Pane / tab destroy | `docker rm -f` | Always, independent of `RemoveWorktree`. |
| Machine reboot | gone (no restart policy) | |

**`docker stop` cannot be used at shutdown.** Its grace is 10 s per container and
claude as PID 1 ignores SIGTERM (the kernel applies no default action to PID 1),
while `stopDaemonEscalating` allows `MsgShutdown` 5 s before SIGTERM/SIGKILL
(`.claude/rules/daemon-lifecycle.md:126`, `cmd/quil/daemonctl.go:67+`). Two sandbox
panes would need 20 s and be SIGKILLed at 5, orphaning the containers and leaving
the pid file — every restart taking the crash path. Use `docker kill` in parallel
under one ≤2 s deadline, after the final snapshot. The conversation survives in the
pane's config dir for `--resume`, and the harvest runs before the kill.

Three hazards:

**Name conflict on respawn.** `docker rm -f` is not instantaneous and a container in
"Removal In Progress" still holds the name — measured:
`Conflict. The container name "/t1" is already in use`. `handleRestartPaneReq`
(`internal/daemon/daemon.go:5783`) closes the old PTY in a goroutine, so the old
CLI's death races the new run. **Retry `docker run` once** after a short backoff.

**The teardown goroutine skips PTY-less panes.** `internal/daemon/session.go:469`
is `if pty == nil { continue }`, so a sandbox pane that failed to spawn leaks its
container forever. Container teardown must not live behind that guard.
`releasePanes` is also a package function with no `*Daemon` receiver (`:463`), so it
needs a package-level seam var or a signature change.

**Three teardown steps must be ordered.** The container must die before the
per-pane root and the worktree are removed. Today `cleanupPaneArtifacts` runs
synchronously at `internal/daemon/daemon.go:2623` — *before* `DestroyPane` — and
`removeOwnedWorktrees` runs unsequenced at `:2642` with only 3 retries at 250 ms
(`internal/daemon/worktree_remove.go:39-41`). So a live container recreates
`events/` after the tree is deleted (leaking it forever, and failing outright on
Windows), and holds the bind mount that makes worktree removal fail.

The order is: **harvest objects → `docker rm -f` → remove the alternates line →
remove the per-pane root and overlays → remove the worktree.** One completion
signal from the container removal gates the last three.

The `docker rm -f` itself belongs on the `releasePanes` off-lock goroutine — a
blocking teardown call must never run under `sm.mu` (`.claude/CLAUDE.md`, always-on
invariants).

### The startup sweep must be scoped to this daemon

v1's sweep filtered on `label=quil.pane` alone. Docker labels are engine-wide and
the dev and production daemons share one engine, so a production pane id is never
in the dev workspace and **a dev daemon would force-remove every production sandbox
container**, killing the owner's live agents. That violates
`.claude/rules/dev-environment.md`, the one rule marked *(always on)*.

Every container carries a second label `quil.home`, and the sweep filters on
**both**. `QuilDir()` returns `os.Getenv("QUIL_HOME")` verbatim
(`internal/config/config.go:726-729`), so a trailing slash or a case difference
between a TUI-spawned and a manually started daemon would make a daemon miss its
own orphans — canonicalise with `filepath.Abs` + `Clean` and `EqualFold` on Windows,
as `IsDefaultQuilDir` does, or persist a random install id under
`$QUIL_HOME/sandbox/`. It fails safe either way: a mismatch never reaps another
install's containers.

The sweep command is, verbatim and measured:

```
docker ps -a --filter label=quil.pane --filter label=quil.home=<hash> \
             --format '{{.Label "quil.pane"}}'
```

`-a` without `-q`. **`docker ps -aq --format …` silently does the wrong thing** —
measured: `WARNING: Ignoring custom format, because both --format and --quiet are
set`, and it prints container ids instead of pane ids. Verified against a real
labelled container: the two-filter form printed the pane id, and a wrong
`quil.home` value matched nothing.

## The slug

`/work/<sanitized basename>-<8 hex digest of the host path>`.

**The digest covers the host path only.** v2 added the pane id to stop two panes
sharing a directory; that rested on a false premise and broke resume. Transcripts
are per **session**, not per project directory
(`internal/claudesessions/claudesessions.go:3`), so two panes never interleave —
two host panes in one CWD is the everyday case today. With the pane id in the slug,
closing a sandbox pane and opening a new one on the same worktree gives a fresh
project directory and an **empty resume picker forever**. `index.lock` contention
between two panes on one worktree is identical to two host panes and is not the
sandbox's to solve.

**Sanitize the basename** to `[A-Za-z0-9_-]`, excluding `.` so `..` is unreachable
by construction — the `config.RecentCWDsPath(dest)` precedent in
`.claude/rules/remote-dialogs.md`. It matters because the string reaches both `-w`
and the container side of a colon-delimited `-v`: a directory named `feat:ro` yields
`-v /path/wt:/work/feat:ro-a1b2c3d4`, which docker parses as a mode.

**An empty basename** (a drive root, a trailing separator) yields `/work/-<hex>`.
Harmless, but a table-test row.

**Moving the repository loses sandbox transcripts.** The digest changes, so the
container workdir changes, so `EscapeCWD` names a different project directory.
Worse, the persisted `ContainerCWD` names the old slug while the container starts at
the new one, and `claude --resume` fails with "No conversation found" and silently
starts fresh. On a mismatch between persisted and computed slug, log it and emit one
sidebar event — visible rather than silent.

## Components

### `internal/sandbox` (new)

Pure and testable without Docker.

- `Mapping` — host↔container pairs for one pane, with the separator rule per side
  stated explicitly.
- `NewMapping(quilDir, hostCWD, paneID) (Mapping, error)` — resolves the toplevel
  via git, derives the slug, and **refuses** every unsafe case above.
- `DotGitOverlay(m)`, `AdminGitdirOverlay(m)` — the two one-line files.
- `RunArgs(spec, m, hostGOOS, identity) []string` — the full argv. `hostGOOS` is a
  parameter, not `runtime.GOOS`, so both `GIT_CONFIG_*` shapes are testable on CI.
- `AlternatesLine(m)`, `HarvestObjects(m)` — the object-store half.
- `ContainerName(paneID)`, `HomeLabel(quilDir)`.
- `docker.go` — the only file that executes `docker`, behind a `runDocker` seam
  var so every other test needs no daemon. It mirrors `gitworktree.runGit`
  (`internal/gitworktree/gitworktree.go:63`) exactly: `exec.CommandContext`, a
  `WaitDelay`, bounded stdout/stderr buffers rather than `cmd.Output()`, and
  **`hideWindow(cmd)` from a `//go:build windows` file**. The last is not
  cosmetic: the daemon is spawned `DETACHED_PROCESS` and owns no console, so
  every Windows child gets a brand-new console allocated — a real window that
  appears and vanishes. `docker info` alone runs on every 30 s cache expiry, so
  without it the user gets a flashing window while the create dialog is open.
  `docker run` is exempt because it is the PTY child, not an `exec` here.

Always-present flags: `--name`, `--label quil.pane=`, `--label quil.home=`, `-i`,
`-t`, `-w`, and `--user` on a Linux host. **No `--rm`**, no `--privileged`, no
user-supplied flag of any kind.

### IPC (`internal/ipc/protocol.go`)

```go
// A POINTER, for the reason WorktreeSpec is one (protocol.go:304): nil keeps
// every existing producer — MCP create_pane, restore, the plugin dialog — on the
// unchanged path with no branch anywhere in the daemon.
Sandbox *SandboxSpec `json:"sandbox,omitempty"`

type SandboxSpec struct {
    Image string `json:"image"`
}
```

**`FirstPaneSpec` gains the same field** (`internal/ipc/protocol.go:419-430`), and
the daemon's hand-written field copy at `internal/daemon/daemon.go:2072-2080` gains
it too. Without that, the setup dialog's own designed flow — new tab, worktree
chosen, sandbox on — builds the replacement pane from a payload with
`Sandbox == nil` and **spawns claude on the host**. Test by driving `MsgCreateTab`
with both specs and asserting the replacement create carries it.

Trust: any IPC client can set this. The daemon validates the image reference
against a conservative grammar before it reaches argv. The MCP bridge does not
expose the field.

```go
MsgSandboxCapReq  = "sandbox_cap_req"
MsgSandboxCapResp = "sandbox_cap_resp"

type SandboxCapRespPayload struct {
    Available     bool   `json:"available"`
    ServerVersion string `json:"server_version,omitempty"`
    OSType        string `json:"os_type,omitempty"`
    Arch          string `json:"arch,omitempty"`   // Go form: amd64 | arm64
    Error         string `json:"error,omitempty"`
}
```

`Available` requires `OSType == "linux"`. Docker Desktop in Windows-containers mode
answers `docker info` normally and then fails every linux image at `run`.

Answered by `docker info --format '{{.ServerVersion}} {{.OSType}} {{.Architecture}}'`
on a worker goroutine behind its OWN single-flight slot, with a **10 s timeout**
stated because a hung Docker Desktop parks the worker on every cache expiry. Cached
30 s — a daemon runs for weeks while Docker Desktop starts and stops. Detection is
`docker info`, never `docker --version`: the CLI can be present with no engine
reachable. Docker reports the machine form (`x86_64`, `aarch64`); the daemon
normalises to the Go form, because the field's only consumer is a release-asset name.

### Persistence, and what an older daemon does with it

`SandboxImage` and `ContainerCWD` are persisted. Persistence is an ad-hoc
`map[string]any` blob, **not** `PaneInfo`, so each field costs four edits following
the `worktree_owned` precedent: struct field (`internal/daemon/session.go`, mirror
at `internal/tui/model.go:166`), daemon write (`daemon.go:3868`), daemon restore
(`:864`), TUI decode (`model.go:7208`). The MCP-facing `PaneInfo`
(`internal/ipc/protocol.go:577-591`) is a separate struct that does not even carry
`WorktreeOwned`; a sandbox badge goes through the `map[string]any` path.

**Restore reads only the keys it knows** (`internal/daemon/daemon.go:864-905`), and
auto-update has a rollback path (`.claude/rules/auto-update.md`). An older daemon
would therefore restore a sandbox pane as an ordinary `claude-code` pane with the
worktree as its CWD — **an agent on the host, silently un-sandboxed.**

Persist the pane type as a distinct value (`sandbox/claude-code`). A daemon that
does not know it takes the existing unknown-type fallback to `terminal`
(`internal/daemon/daemon.go:4729-4732`) — a shell, not an agent.

**A sandbox pane never falls back to a host spawn.** Docker unavailable at restore
(Docker Desktop is rarely up at Windows login) yields `SpawnError`
"sandbox unavailable", and Alt+R retries.

### Client-side availability (`internal/tui`)

Filed per destination — `Model.destSandbox[dest]` keyed by `ipc.Message.Origin`,
read only through `Model.sandboxAvailableFor(dest)`.

**Do not copy `pluginAvailableFor` verbatim.** It refuses a local answer
(`internal/tui/plugins_client.go:101`) and falls back to local detection when a
destination has not answered (`:135`). Both are wrong here: the local daemon's
answer is the only one that exists for the case this feature targets, and there is
no local fallback. File every answer including the local one, and treat a silent
destination as unavailable.

**Snapshot availability into the dialog state at open**, exactly as
`createPaneDialogDest()` pins the destination. The answer is asynchronous (10 s
timeout, 30 s cache), so an answer landing mid-dialog would otherwise change
`setupFieldCount` and `setupFieldKind` under a live cursor and shift
`setupChromeRows`. Request at attach and at each dialog open.

On a **remote** daemon a sandbox pane means a container on the remote machine, and
every host path in `Mapping` is the remote's `$QUIL_HOME`. Correct by construction;
stating it is the point.

### Setup dialog (`internal/tui/dialog.go`)

Field order becomes:

```
CWD → kube → toggles → worktree → sandbox → session → Continue
```

Between worktree and session on purpose: the sandbox mounts whatever directory the
worktree choice settles on, and the session listing is scoped to the directory the
sandbox choice then relocates.

**Eight sites, not two:**

| Site | Why |
|---|---|
| `setupFieldCount` `:4183` | count |
| `setupFieldKind` `:4210` | kind |
| `renderCreatePaneSetupDialog` `:5498-5817` | keeps its OWN parallel `fieldIdx` walk and never calls `setupFieldKind` |
| key dispatch `switch kind` `:4302` | space-toggle and image typing |
| `onSetupFieldFocused` `:4380` | on-focus side effects |
| `needsSetup` gate `:3722` | a sandbox-only plugin would skip the dialog entirely |
| `const setupChromeRows = 26` `:5200` | a new row changes it; pinned by `internal/tui/sessions_test.go:522` |
| `submitSetupDialog` `:4788` | carry the image into the payload |

A toggle with an inline text field: space switches the sandbox on and off, typing
edits the image while on. Off by default. An empty image with the sandbox on is
refused at Continue with an inline message, never silently defaulted.

### Spawn (`internal/daemon/daemon.go:4702`)

The real order in `spawnPane` is: plugin type settles (`:4709`) → `cmd` (`:4732`) →
resume-id resolution (`:4745-4822`) → `resolveSpawnArgs` (`:4824`) →
`shellinit.Configure`, which may replace both `cmd` and `args` (`:4827`) → the hook
switch (`:4856-4877`) → `SetEnv` (`:4885`) → host `exec.LookPath` (`:4903`) →
`SetCWD` (`:4908`) → `Start` (`:4910`).

Sandbox changes:

1. Build the `sandbox.Mapping` before the hook switch, and thread it into all three
   prep functions — each currently takes `config.QuilDir()`, the host dir
   (`:4858`, `:4869`, `:4874`), and that is the value the mapping translates. Pass
   `"linux"` where a prep takes a GOOS.
2. **Skip the host `exec.LookPath` at `:4903`.** The agent binary lives in the
   container; resolving it on the host fails or resolves the wrong file.
3. **Keep `ptySession.SetCWD(pane.CWD)` on the HOST path.** `ownedWorktreePaths`
   and the whole git subsystem read it. The agent's working directory comes from
   `docker run -w`.
4. Wrap **after** `shellinit.Configure`, never between it and the hook switch.
   (`ShellIntegration` is false for AI plugins, so this is harmless today — but it
   is a second `cmd`/`args` replacement and the ordering must be deliberate.)
5. Write the alternates line and both overlay files before `docker run`.

The order matters because the settings JSON embeds the hook command's path: a wrap
applied before the hook switch would bake a host path into a file only the
container reads.

### Linux hook binary

Staged at `$QUIL_HOME/sandbox/bin/<version>/`, fetched once per version+arch.

The usable entry point is `(*update.Stager).Stage(ctx, rel)`
(`internal/update/stage.go:112`), already parameterised on GOOS/GOARCH. Note that
`downloadHashed`, `fetchChecksums` and `extractBinaries` are **unexported**, and
`Release` is a method on `*Checker` (`internal/update/github.go:78`).
`extractBinaries` needs no change: `BinaryNames("linux")` is `{quil, quild}` and the
published linux `.tar.gz` carries both.

**`Stager.Root` MUST be `$QUIL_HOME/sandbox/bin/`, never `config.UpdateDir()`.**
The apply path scans that tree and installs whatever it finds
(`cmd/quil/update_apply.go:34`, `:45`, `:125`). On a Linux host the linux archive's
names are identical to a real update's.

`QUIL_SANDBOX_QUILD` overrides the path with a locally built binary — required for
dev builds, whose version is not a published release
(`internal/daemon/update.go:52`, `:81`, `:234` gate on `version.IsRelease()`).

### Memory reporting

The collector walks the host process tree from the pane PID
(`internal/daemon/procreport.go:179-190`), and the agent lives in the Docker VM on
Windows or a containerd cgroup on Linux — never under `docker.exe`. So a sandbox
pane's figure would be the docker CLI's own RSS.

Report sandbox panes as "in container — not measured" in `get_pane_memory` and the
memory dialog, or query `docker stats --no-stream --format` behind the same seam.

## Failure UX

| Case | Required behaviour |
|---|---|
| Image not local | A **preparing placeholder**, the way `constructPreparingPane` covers `git worktree add`. A pull has no timeout and no placeholder today. |
| Image does not exist | `SpawnError` naming the image and the docker message |
| Image has no `claude` | `SpawnError` with `exec: "claude": executable file not found in $PATH`, plus a docs link |
| Docker engine stops mid-session | container dies; `--rm` is not used, so `docker logs quil-<paneID>` survives |
| Docker unavailable at restore | `SpawnError` "sandbox unavailable"; Alt+R retries; **never a host spawn** |
| Linux quild fetch fails | claude pane refuses to spawn (`SpawnError`); other plugins warn + one sidebar event |
| `--name` conflict on respawn | retry `docker run` once after a short backoff |
| Docker Desktop drive not shared | detect and report it in the pane |
| Stale alternates line found at start | strip it, log it, one sidebar event |
| Not signed in | see must-measure below |

## Configuration

```toml
[sandbox]
# "" (default) — sign in inside the container, once per pane.
# "token"      — forward CLAUDE_CODE_OAUTH_TOKEN from the daemon's environment.
#                Recommended when using more than one sandbox pane: no credential
#                file is needed, so each pane keeps its own config dir for free.
auth = ""
# One sign-in for every sandbox pane, at the cost of merging them into ONE trust
# domain: any sandbox pane can then write hooks and MCP servers that every other
# sandbox pane's claude will run.
shared_claude_config = false
# Pre-fills the dialog's image field. No value ships as a default.
default_image = ""
```

## Security notes

**What the sandbox bounds:** the agent's process reach, to the container; and its
filesystem reach, to `/work/<slug>` rw, the repository's **`.git` only** at
`/repo/.git` ro with four narrow rw sub-mounts, and its own `/quil` subtree. The
main checkout's working tree never enters the container, so no other pane's data
and none of the user's other source is reachable.

**What it does not bound, and why:**

- **Branch pointers.** `refs` is writable, so a sandbox pane can move or delete any
  branch including `main`. Recoverable: the objects survive and `git reflog` finds
  them. Making `refs` read-only would make committing impossible.
- **Its own worktree and admin directory.** Blast radius equals
  `rm -rf /work/<slug>`. The close dialog must therefore treat
  "not a git repository" on an owned worktree as *unknown*, not *clean*.
- **The Claude credential in its own config dir.** Anthropic's own warning applies:
  dev containers "do not prevent a malicious project from exfiltrating anything
  accessible inside the container, including the Claude Code credentials stored in
  `~/.claude`". Per-pane dirs bound it to that pane;
  `shared_claude_config = true` removes that bound deliberately.
- **Network egress.** The image author's business. Anthropic's `init-firewall.sh` is
  the model and needs `NET_ADMIN`/`NET_RAW`, which Quil does not grant.

**What the object store buys:** `/repo/.git/objects` is read-only, so no sandbox
pane can delete the repository's history. That is the single largest irreversible
loss available in a bind-mounted worktree, and it is closed.

**Not exposed:** `~/.ssh`, cloud credential files, any shared `$QUIL_HOME`
subdirectory, the `$QUIL_HOME` root, the docker socket, other panes' worktrees.

Usage limits deserve one line in the user docs: "Advertised usage limits for Pro
and Max plans assume ordinary, individual usage of Claude Code." A multiplexer makes
many concurrent agents easy.

## Must be measured before shipping

**1. Resize propagation through `docker run -it` under ConPTY — MEASURED, and it
works.** A throwaway probe (`cmd/sandboxprobe`, deleted after the run) opened a
real ConPTY through `internal/pty.NewWithSize(100, 30)`, ran
`docker run -i -t alpine:3 sh -c 'stty size; sleep 2; stty size; sleep 3; stty size'`,
and resized the PTY to 120x40 between the second and third readings:

```
  size: 30 rows x 100 cols     ← initial, matches the PTY
  size: 30 rows x 100 cols     ← before the resize
  size: 40 rows x 120 cols     ← after the resize
```

Three facts follow. The docker CLI **does** see a ConPTY as a terminal, so
`-i -t` is safe — worth knowing because it fails hard otherwise: from a
non-terminal stdin docker answers `cannot attach stdin to a TTY-enabled
container because stdin is not a terminal`. The container's initial size is
correct, so a pane opens at the right geometry. And an ordinary resize reaches
the container, so no pane is left mis-sized.

The `resizeKick` jiggle worry is therefore about REPAINTING, not about geometry:
a real resize propagates, so a sandbox pane is always correctly sized even if the
back-to-back pair is coalesced. That reduces the risk from "the 1x1 class of
irreversible damage" to "a pane may need a keystroke to repaint after a resize",
which is cosmetic and self-correcting. Left as a follow-up rather than a blocker;
if it shows up in use, separate the two `Resize` calls for sandbox panes.

The original concern, kept because the reasoning is still why the kick exists:

- `resizeKick` (`internal/daemon/daemon.go:3333-3345`) makes two `Resize` calls back
  to back with no delay. Windows has no SIGWINCH; a console client learns its size
  by asking, so the docker CLI must poll, and two changes inside one poll interval
  are indistinguishable from none — while the jiggle exists precisely to manufacture
  an observable change.
- The kick fires on the pane's **first output**, chosen because events fired before
  the child reads input "are dropped and never replayed"
  (`daemon.go:3322-3332`). For a sandbox pane the first output is docker's own pull
  progress, minutes before claude starts. The kick is spent on docker.

This is the one part of the codebase with a documented history of irreversible
damage — the 1x1 headless-attach incident "permanently re-wrapped every child's
transcript" — which is why it was treated as blocking until measured.

Still unmeasured, and deliberately not blocking: the same probe under the inbox
Win10 ConPTY host rather than the bundled OpenConsole. Geometry is carried by the
same `Resize` call on both, and a wrong answer there would be a repaint nuisance
rather than a mis-sized pane.

**2. The default in-container sign-in.** Asserted, not measured. Anthropic documents
the paste-the-code fallback, which should make it work with no published port — but
if a port turns out to be needed, that is a hole in "no user-supplied flag of any
kind" and must be designed, not discovered by a user.

## Performance and environment notes for the docs

All measured on this machine.

- **Bind-mount IO is ~20x slower.** Counting 1102 files: 0.14 s on the host, 4.5 s
  in the container. Extrapolated to a 100k-file monorepo, `git status`, ripgrep and
  a test run become minutes. A repository living inside WSL2 does not pay this cost.
- **inotify does not cross a Docker Desktop bind mount.** Measured: `inotifywait -m`
  in the container saw no event for a host create or modify, while a later poll saw
  both. So `tsc --watch`, `jest --watch`, `vite` and `nodemon` sit silent and the
  agent concludes its edits had no effect. Not fixable in Quil — document that
  watchers need polling mode (`CHOKIDAR_USEPOLLING=1`, `--watch-poll`).
- **Windows bind-mount paths pass through unchanged**, measured:
  `-v 'E:\Projects\…:/w:ro'` returned the correct file count. The drive must be
  shared in Docker Desktop's file-sharing settings.
- **`git gc` inside a sandbox fails** and should not be run.

## Testing

Docker is unavailable inside `dev.sh test` (the suite already runs in a container),
so the split is deliberate:

- `internal/sandbox` — table tests for `NewMapping`: worktree, plain checkout,
  subdirectory of a worktree, missing `.git`, empty basename, a basename containing
  `:`, and every refusal (`$QUIL_HOME` inside the worktree, `$QUIL_HOME` inside the
  main root, nested worktrees). Both overlay contents. `RunArgs` argv shape for
  **both** host-OS `GIT_CONFIG_*` shapes and the Linux `--user` shape. Container-side
  separators built from deliberately Windows-shaped inputs. `RunArgs` never contains
  `=` after `CLAUDE_CODE_OAUTH_TOKEN`.
- Object store — `AlternatesLine` in the host's native path form; `HarvestObjects`
  idempotent over a repeated run; removal deletes only this pane's line from a file
  with two; the startup repair strips a dead line and leaves a live one.
- Resume — the validating container→host rewrite, including refusal of a path
  outside the prefix; the picker seams resolving against the pane's config dir.
- `spawnPane` — a fake `apty.Session` asserting `cmd` is `docker`, the original
  command is the argv tail, the settings path in argv is a container path, and no
  host `LookPath` ran. Driven through the create handler, never by calling the
  helper: a direct-call test can pass against code the call site makes unreachable.
- `MsgCreateTab` with a `FirstPaneSpec` carrying both a worktree and a sandbox spec,
  asserting the replacement create carries the sandbox spec.
- Codex/opencode — a sandbox pane's argv contains no backslash and no `.exe`.
- Forwarder — closes between batches; caps bytes; does not replay after a restart;
  restamps `ts_ms`.
- Symlink — a Linux-CI test with a symlinked `sessions/` under the per-pane root.
- Teardown — a seam counting `rm -f`, including the failed-spawn pane with no PTY,
  and the ordering of harvest → kill → line → root → worktree.
- Sweep — a container labelled with another `quil.home` is never removed;
  canonicalisation of a trailing slash and a case difference.
- Availability — filed per origin including the local `""`; `OSType != "linux"` is
  unavailable; a second daemon's answer does not change the first's.
- Restore — a `sandbox/claude-code` pane type is unknown to the old restore path
  and falls back to `terminal`.
- Real-Docker tests behind a skip-if-unavailable guard, mirroring
  `internal/gitworktree/realgit_test.go`.

## Out of scope

- **Repositories with submodules.** Measured broken: a submodule's `.git` in a
  linked worktree is a relative path that escapes into a sibling directory
  (`gitdir: ../../main/.git/worktrees/wt/modules/sub`), and the `/repo` +
  `/work/<slug>` layout does not preserve the host's relative offset. From
  `/work/w/sub`, `../..` is `/`. Quil's own repository uses exactly the layout that
  produces this, so it is the common shape. Fixing it means mirroring the host's
  relative layout in the container; until then the docs must say so rather than stay
  silent, which reads as supported.
- A Quil-published container image (legal; see Authentication).
- A dropdown of predefined images — there is no official Anthropic image, only a
  dev-container *feature* and a reference `.devcontainer/` the docs call "a working
  example rather than a maintained base image".
- Reading the repository's own `.devcontainer/devcontainer.json`.
- Network egress policy inside the container.
- Sandboxing non-AI panes.
- macOS verification. The design is host-OS neutral and the token path avoids the
  Keychain entirely, but only Windows has been measured.

## Documentation to update

- `docs/features.md` — the sandbox pane.
- `docs/configuration.md` — the `[sandbox]` table, including the
  `shared_claude_config` trust-domain warning.
- `docs/troubleshooting.md` — Docker not detected, drive not shared, image missing
  `claude`, the one-time sign-in, and **the stale alternates line and its one-line
  manual fix**.
- A new `docs/` page: the Dockerfile users need (built on
  `ghcr.io/anthropics/devcontainer-features/claude-code`), the ~20x IO cost, the
  polling-watchers note, do-not-run-`git gc`, and the submodule limitation.
- `internal/config/config.go:875-877` — correct the stale comment describing
  `claudehook/` as holding hook *scripts*.
- `.claude/rules/sandbox.md`, gated on `internal/sandbox/`, `daemon/sandbox*.go`,
  `tui/sandbox*.go`. Keeps `.claude/CLAUDE.md` under the 100,000-byte gate.
- `changelog.d/feat-docker-sandbox.md` with a `headline:` line.
