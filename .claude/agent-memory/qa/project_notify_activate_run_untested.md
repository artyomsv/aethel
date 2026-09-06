---
name: project_notify_activate_run_untested
description: notify.RunActivation (the actual toast-click handler both quil-activate.exe and `quil activate` call) has zero tests on any platform, even though two of its three branches are platform-neutral and Linux-testable
metadata:
  type: project
---

`internal/notify/activate_run.go`'s `RunActivation(scheme, raw string, logf func(string, ...any))`
is the single function both toast-click entry points call (`cmd/quil-activate/main.go` and
`cmd/quil/notify.go`'s `handleActivate`). It has three branches:

1. `ParseActivateURI` fails → `logf("refused malformed activation URI: %v", err)`, return.
2. Parse succeeds, `SendActivate` fails (no listener) → `logf("no listener for pane %s...")`, return.
3. Both succeed → `logf("routed pane %s to pid %d", ...)`.

As of the desktop-notifications feature (round 1, 2026-08-13/14), `activate_run.go` has NO
corresponding `_test.go` file at all — confirmed via `grep -rn RunActivation` across the repo,
the only hits are the two call sites, the doc comment, and an unrelated cross-reference in
`setup_windows.go`.

**This is a real, closable gap, not an unavoidable Windows-only one.** The file carries no
`//go:build` tag. Branches 1 and 2 are fully exercisable on Linux today:
- Branch 1 with any string `ParseActivateURI` rejects (see `activate_test.go`'s existing table).
- Branch 2 for free, because `internal/notify/listener_other.go`'s `SendActivate` unconditionally
  returns `ErrUnsupported` on non-Windows — so on Linux CI, EVERY call into branch 2 exercises the
  real "no listener" logging path with zero setup.

Only branch 3 (the success path) needs a real listener, which is Windows-only (`Listen`/
`SendActivate` in `listener_windows.go`, tested end-to-end there via
`TestListenSendActivate_RoundTrip`) — but `RunActivation` itself, as the thin orchestration layer
around it, could still be pinned on Linux for branches 1 and 2 with a fake/recording `logf`.

**Also noted, lower risk:** `cmd/quil-activate/main.go`'s `parseArgs` (hand-rolled flag parser for
`--scheme`/`--home`/bare URI, with real edge cases: missing value after a flag, unknown flags
ignored, first-bare-arg-wins) has no test either, and — unlike `activateCommand` in
`activatecmd.go`, which was deliberately extracted to a platform-neutral file specifically so CI
could test it — `parseArgs` lives inside the `//go:build windows` `main.go`, so it can never be
tested on Linux even in principle. This one is a design choice (small enough that extraction may
not be worth it) rather than an oversight, but the asymmetry with `activatecmd.go`'s established
pattern is worth flagging if this file grows more logic.

**How to apply:** when reviewing a later round of this feature, check whether `activate_run_test.go`
now exists and covers branches 1 and 2 via a recording `logf`. If `parseArgs` gains more branches
(e.g. new flags), reconsider extracting it to a platform-neutral file the way `activateCommand` was,
so it gets the same CI-testable treatment. Related: [[project_notify_wiring_gap]] (the
`raiseAttentionToast`/`sweepOutstandingToasts` wiring gap in the same feature, since resolved) —
same family of bug: a package convention (platform-neutral extraction for testability) was applied
inconsistently within one feature.
