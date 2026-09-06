---
name: notify-activate-stale-raise-comment
description: cmd/quil-activate/main.go's doc comment still says RunActivation "raises the window", but commit ee3cbe2 removed that behavior everywhere else (activate_run.go, docs, CHANGELOG, site copy) without touching this file.
metadata:
  type: project
---

Round-2 finding (2026-08-14) on branch `feat/system-notifications`.

Commit `ee3cbe2` ("drop the window raise and ship click-to-route only") removed
the window-raise feature and correctly updated `CHANGELOG.md`,
`cmd/quil/notify.go`, `docs/troubleshooting.md`, and
`internal/notify/activate_run.go` (whose doc comment now explicitly states
RunActivation does **not** raise the window, and why). `docs/features.md`,
`docs/roadmap.md`, and `site/src/data/features.ts` were also already accurate
(click-to-route only, no raise claim).

One file `ee3cbe2` did **not** touch: `cmd/quil-activate/main.go:24`. Its
package doc comment still reads:

> "Everything it may do is in notify.RunActivation: parse the URI, validate the
> pane id, send that id to the TUI over a per-PID pipe, raise the window."

This is now false — `RunActivation` (`internal/notify/activate_run.go`)
deliberately does not raise the window. [[notify-claude-md-quil-activate-gap]]
is the related finding about the same commit's documentation completeness.

**Why:** not caught by `documentation-maintenance.md` (that rule's `paths:`
only covers CLAUDE.md/README.md/AGENTS.md/GEMINI.md/rules files, not `.go`
doc comments), but it's exactly the class of staleness the orchestrator asks
about explicitly for this feature (window-raise removal accuracy).

**How to apply:** On the next round, check whether `cmd/quil-activate/main.go`'s
package comment was corrected to match `activate_run.go`'s account. If still
present, keep flagging as MEDIUM.
