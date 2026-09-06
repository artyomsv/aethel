---
name: package-var-seam-is-allowed
description: CONTRIBUTING.md explicitly permits package-level swappable FUNCTION vars for testability, so a `var readX = func()...` seam is NOT a go-conventions "no global mutable state" violation in quil; plain mutable vars still are.
metadata:
  type: project
---

`CONTRIBUTING.md` ("Code conventions") carries this exact carve-out:

> **No global mutable state** — pass dependencies explicitly. The exception:
> package-level swappable function vars for testability (already established
> pattern in `internal/daemon/`).

So a seam like `var readRuntimeSample = func() runtimeSample { ... }`
(`internal/tui/perfruntime.go`) is a SANCTIONED pattern, not a finding.
`.claude/rules/remote-dialogs.md` documents the same idea for
`listFilesystemRoots` ("a package-var seam because the Unix arm always reports
complete").

The carve-out is narrow: it says **function** vars. A plain mutable VALUE var
that tests write is still outside the exception and still worth flagging —
prefer a parameter.

That distinction was worth making on PR #205: a test helper briefly carried a
`var reproPaneCountOverride int` that one file wrote and another read, and it
was replaced with a plain `panes int` parameter on `buildReproModel`. **The var
no longer exists** — do not cite it as a live example; cite the shape instead.
The neighbouring precedent for when a value var genuinely must stay global is
`explicitScrollback` in `internal/tui/pane.go`, which is an ATOMIC for exactly
this reason ("a plain int makes every parallel test in the package racy against
any other that builds a pane").

**Why:** the global rule `~/.claude/rules/go-conventions.md` says "no global
mutable state" flatly. Project CLAUDE.md / CONTRIBUTING outranks the personal
global rules, so flagging the function-var seam would be a false positive that
sends the author to change working, intentional code.

**How to apply:** before flagging any package-level `var` in a Go review, check
whether it is a swappable FUNCTION var used as a test seam. If yes, list it
under Passed Rules with the CONTRIBUTING quote. If it is a mutable value, flag
it. Related: [[goroutine-shutdown-convention]] — same shape of project
convention overriding a global rule.
