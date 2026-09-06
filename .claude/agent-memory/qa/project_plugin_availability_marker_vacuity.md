---
name: project-plugin-availability-marker-vacuity
description: Tests asserting "DetectAvailability re-detected locally" are vacuous when the marker is terminal/terminal-wide — both are Available:true at construction, so the assertion passes with the call deleted
metadata:
  type: project
---

In `internal/tui`, any test that asserts a local `DetectAvailability()` pass ran by
checking `pluginAvailableFor("", "terminal")` or `..., "terminal-wide")` proves
nothing. `builtinTerminal()` sets `Available: true` in the struct literal
(`internal/plugin/builtin.go`) and `builtinTerminalWide()` copies that value, so a
bare `plugin.NewRegistry()` already reports both available with no detection pass at
all. Deleting all three `DetectAvailability()` call sites (Plugins-dialog Reload,
TOML-editor save, migration save) left the whole `internal/tui` + `internal/plugin`
suite green — verified by mutation on 2026-09-03, PR #198, at BOTH 0d3b913 and the
fix commit 3cd886d. CLOSED at 4a20c91 by a `seedUnavailable(t, reg, name)` helper
(`internal/tui/dialog_test.go`) that flips the marker false before the handler runs
and `t.Fatal`s if it was already unavailable — re-verified by mutation: deleting all
three calls, and separately re-wrapping them in the old remote-mode guard, both go
red now.

The pre-#198 versions of those same tests WERE discriminating, because they seeded
the registry false via the now-deleted `Registry.SetAvailability`. Removing that
setter silently removed the seeding step, and the replacement (`applyPluginList`
into `Model.destAvail`) writes to a different store that no handler touches — so
BOTH halves of each renamed test became unfalsifiable at once.

**Why:** a marker whose expected value is already the zero-effort default cannot
detect the absence of the work that would produce it.

**How to apply:** when reviewing a test that claims a detection/refresh pass ran,
check what the marker's value would be with the pass removed. Seed the opposite
value first (`reg.Get(name).Available = false`, the idiom `toolsRegistry` in
`internal/tui/create_pane_test.go` already uses) or pick a plugin that is only
available after detection. Relatedly, when a refactor deletes a setter, look for
tests that used it purely as a fixture — see [[unit-test-bypassing-call-site]] and
[[feedback_tier_seam_and_hardcoded_dispatch_gaps]].
