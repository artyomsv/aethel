---
name: project_sandbox_wiring_gaps
description: feat/docker-sandbox — internal/sandbox's pure functions are well pinned, but every daemon and TUI call site that uses them is unpinned; 15 of 25 mutations survived
metadata:
  type: project
---

Round 1 QA of `feat/docker-sandbox` (2026-09-07, HEAD `6800726`). Suite fully green — `./...` plus
`-race` on sandbox/daemon/tui/ipc, no races — and **15 of 25 mutations survived**. The split is the
finding, and it is the same shape every time in this repo:

**`internal/sandbox` is genuinely well tested.** The mount arithmetic, the objects/info shadows, the
harvest's `info/` exclusion, the only-this-pane alternates removal, the symlink-resolved refusals —
all killed their mutations. The package is pure-by-design precisely so it is table-testable with no
Docker, and that paid off.

**Every call site that uses it is unpinned.** `prepareSandbox`, `wrapInContainer`, `writeOverlays`,
`sandboxIdentity`, `isReservedSandboxEnv`, `dockerCLIEnv`, `sandboxPaneType`, `splitSandboxType`,
`containerHookPaths`, `linuxQuildForPane` had **zero** test references between them. So: deleting the
`writeOverlays` call, dropping the `sandbox/` persisted-type prefix, and turning the
"sandbox unavailable → refuse" into a log-and-continue were all green — three separate ways to end up
with an un-sandboxed agent or a destroyed host worktree.

Three test-shape traps worth recognising again elsewhere:

- **A guard whose fixture never enters its own loop.** `TestMounts_ObjectStoreIsNeverWritable`
  iterates mounts looking for one under `objects`, but its `KindWorktree` fixture has no objects
  mount, so the body ran zero times. A control mutation proved the loop *can* fire — it is a valid
  forward guard that covers none of today's code. The one real objects mount (`KindCheckout`) was
  flippable to read-write, green.
- **A stub that discards the argument the invariant is about.** `TestSweepSandboxContainers_…` stubs
  `sandboxListFn` with a signature that takes the quil.home label and ignores it, so the
  "never reap another daemon's container" claim in its doc comment is untested; dropping the
  `--filter label=quil.home=` from the real argv was green.
- **A denylist posing as an allowlist.** The "main working tree never enters the container" test
  checks `mt.host == "/projects/main"` literally, so *adding* any other host mount passes. Assert the
  whole expected `[]mount` instead.

Also found, not test gaps but real: `claudeConfigDirForPane` has **no production call site** (its own
test is the only caller, and the bug its comment describes — a sandbox pane's session picker listing
HOST sessions — is still live), and `config.SandboxConfig.SharedClaudeConfig` is documented at length
and read nowhere.

**Why:** the branch's whole value is a refusal — "a pane the user asked to isolate must never run
unisolated" — and the refusal lived entirely in untested wiring while the arithmetic beneath it was
proven three ways over.

**How to apply:** on any feature that separates pure logic from its wiring, budget the mutation pass
for the CALL SITES, not the helpers. Grep each new unexported helper for test references first; a
zero means the mutation will survive, so probe it rather than reading it. Same lesson as
[[project_notification_timeline_wiring_gaps]] and the repo's own `unit-test-bypassing-call-site` note.
Probe method: [[project_mutation_probe_on_a_copy]].
