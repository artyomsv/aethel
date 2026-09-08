---
name: trace-wire-fields-end-to-end
description: For any new IPC field or feature flag in quil, grep producer AND consumer before reviewing behaviour — the docker-sandbox branch shipped 7k lines whose on-switch was never wired, and every test was green
metadata:
  type: project
---

When a quil change adds a new `ipc` payload field, a new `Model` flag, or a new
`Pane` field, the first check is `git grep` for **every** name in the chain and
confirm both a producer and a consumer exist in non-test code.

**Why:** the `feat/docker-sandbox` branch (2026-09-07, 46 files, 7166 insertions)
had four independent breaks on the single path from "user ticks the box" to
"pane runs in a container", and all four were invisible to the suite:

- `requestSandboxCap` — defined, never called, so the dialog row never rendered.
- `resetSandboxField` — the only call site was the SUBMIT teardown, 19 lines
  before `sandboxSpec()` read the flag it had just cleared. Always nil.
- `ipc.SandboxSpec` — produced by the TUI in 3 places, read by the daemon in 0.
  `pane.SandboxImage` was writable only from a snapshot key nothing wrote.
- `sandbox.ImageOK` — three comments in two packages assert the daemon calls it
  before the value reaches argv. No call site.

Every unit test was a direct call to a leaf function; nothing drove `Update` or
`handleCreatePane`. The greps that found all four took under two minutes:

```
git grep -n "<NewFuncName>" HEAD -- internal | grep -v _test.go
git grep -n "<NewIPCType>"  HEAD -- internal/daemon
```

**How to apply:** run those two greps on every new symbol in a feature branch
BEFORE reading the logic. A symbol whose only non-test reference is its own
definition is a P0 finding, not a style note — and in this repo the resulting
failure is silent (the pane spawns un-sandboxed with no error anywhere), which
is the mode `.claude/rules/` repeatedly says must never ship.

Related: [[extracted-geometry-makes-tests-tautological]] — same root cause on
the test side, a suite that never reaches the call site.
