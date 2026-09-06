# Sidebar and Attention-Model Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix five reported defects in the project sidebar and the pane attention model, so a working pane reads as working, the sidebar is legible and scrollable, and every attention mark reaches both the pane and its tab.

**Architecture:** All changes are TUI-side (`internal/tui`) plus one line in `internal/hookevents`. No IPC, no daemon state, no protocol change. The attention fixes are edits to the existing single derivation point in `workstate.go`; the sidebar fixes extend `sidebarRows`/`sidebarVisibleRows`, which paint and hit-test already share.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2 (`charm.land/lipgloss/v2`). Tests are stdlib `testing`, table-driven.

**Spec:** `docs/superpowers/specs/2026-08-08-sidebar-attention-fixes-design.md`

## Global Constraints

- **Base is `origin/master` @ `71535ce`.** Line numbers in this plan are against that commit. Do not trust citations from the earlier research — its `model.go` numbers are ~50 lines high.
- **Build/test only via Docker:** there is no local Go toolchain. `./scripts/dev.sh test internal/tui` runs one package; bare `./scripts/dev.sh test` runs all. `dev.sh test` passes neither `-run` nor `-v` — to run ONE test, use `docker run --rm -v "$PWD":/src -w /src golang:1.25-alpine go test ./internal/tui/ -run TestName -v`.
- **`lipgloss` is the sole width authority.** Every cut goes through `truncateCells` / `elideMiddle` / `padOrTrunc`. Never slice a string by rune count for display.
- **No sidebar glyph may be an emoji-capable codepoint** — pinned by `TestSidebarGlyphs_OneCellAndNotEmojiCapable` (`sidebar_glyphwidth_test.go:38`). Any new glyph must be added to that test's map.
- **`sidebarVisibleRows` must stay pure.** Paint and hit-test both call it with the same height; a render that writes model state is how the two come to disagree.
- **Never touch `~/.quil/`** — the project owner runs Quil in production from there. Dev/testing uses the worktree only.
- **Commit style:** imperative mood, ≤72 chars on the subject. No AI/agent attribution of any kind.
- **Go conventions:** tabs for indentation, `MixedCaps`, wrap errors with `%w`, `t.Helper()` in test helpers.

---

### Task 1: `tabBlocked` — surface a parked pane on its tab

Item 6.1. Must land **before** Task 2: Task 2 stops park from setting `unseen`, and without `tabBlocked` a parked background pane would have no tab-level mark at all in between.

**Files:**
- Modify: `internal/tui/workstate.go` (add `tabBlocked` after `tabPinnedAttention`, ends line 431)
- Modify: `internal/tui/model.go:4619-4644` (`tabStyle` precedence + doc comment)
- Modify: `internal/tui/styles.go:17-24` (add `blockedTabStyle`, fix `unseenTabStyle` comment)
- Test: `internal/tui/attention_test.go`

**Interfaces:**
- Consumes: `Model.curTabs()`, `Model.activeTabIdx()`, `TabModel.Leaves()`, `PaneModel.blockedSince` (all existing).
- Produces: `func (m Model) tabBlocked(idx int) bool` and package var `blockedTabStyle lipgloss.Style`, both used by Task 2's tests.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/attention_test.go`:

```go
// TestTabBlocked_ReportsParkedPaneOnBackgroundTab pins the tab-level blocked
// mark. Before this existed a parked pane showed ▲ in the sidebar while its
// tab showed nothing at all — the defect in item 6.1.
func TestTabBlocked_ReportsParkedPaneOnBackgroundTab(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		blocked     bool
		activeTab   bool
		focusedPane bool
		want        bool
	}{
		{"parked pane on background tab", true, false, false, true},
		{"parked pane on active tab", true, true, false, true},
		{"parked pane is the focused pane", true, true, true, true},
		{"no parked pane", false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModelWithTabs(t, 2)
			ti := 1
			if tt.activeTab {
				ti = 0
			}
			tab := m.curTabs()[ti]
			pane := tab.Leaves()[0]
			if tt.blocked {
				pane.blockedSince = time.Now()
			}
			if tt.focusedPane {
				tab.ActivePane = pane.ID
			}
			if got := m.tabBlocked(ti); got != tt.want {
				t.Errorf("tabBlocked(%d) = %v, want %v", ti, got, tt.want)
			}
		})
	}
}

// TestTabStyle_BlockedOutranksUnseen pins the precedence. A pane that is
// blocked AND unseen must read as blocked: "needs you" is the more urgent of
// the two, and it is the one the user can act on.
func TestTabStyle_BlockedOutranksUnseen(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 2)
	pane := m.curTabs()[1].Leaves()[0]
	pane.unseen = true
	pane.blockedSince = time.Now()
	if got := m.tabStyle(1); got.GetBackground() != blockedTabStyle.GetBackground() {
		t.Errorf("tabStyle background = %v, want blockedTabStyle %v",
			got.GetBackground(), blockedTabStyle.GetBackground())
	}
}
```

If `newTestModelWithTabs` does not exist in the package, use whichever fixture builder the neighbouring tests in `attention_test.go` already use to construct a `Model` with N tabs and one pane each — do not add a second builder.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `m.tabBlocked undefined` and `blockedTabStyle undefined`.

- [ ] **Step 3: Add the style**

In `internal/tui/styles.go`, correct the now-wrong `unseenTabStyle` comment and add the blocked style beneath it:

```go
	// unseenTabStyle highlights a background tab containing a pane that
	// finished a turn and hasn't been focused since. Green background,
	// bright text; clears when the pane is focused.
	unseenTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("28")).
			Padding(0, 1)

	// blockedTabStyle marks a tab holding a pane parked on the user
	// (permission prompt / option select). Amber, matching the sidebar's
	// sidebarBlockedStyle so the tab bar and the strip use one vocabulary.
	// Outranks unseenTabStyle: "needs you" beats "finished while you were
	// away", and unlike unseen it names something you can act on.
	blockedTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("232")).
			Background(lipgloss.Color("214")).
			Padding(0, 1)
```

- [ ] **Step 4: Add `tabBlocked`**

In `internal/tui/workstate.go`, after `tabPinnedAttention` (which ends at line 431):

```go
// tabBlocked reports whether the tab at idx holds a pane parked on the user.
// Unlike tabUnseen the ACTIVE tab also reports true: a permission prompt is
// not a seen/unseen state, it is an outstanding question, and the tab bar is
// the only place a user scanning several projects will notice it. It stays
// true for the focused pane too — ackFocusedPane is what clears the pane's
// blockedSince, and deriving the tab mark from that one flag keeps the two
// levels from disagreeing.
func (m Model) tabBlocked(idx int) bool {
	tabs := m.curTabs()
	if idx < 0 || idx >= len(tabs) || tabs[idx].Root == nil {
		return false
	}
	for _, p := range tabs[idx].Leaves() {
		if p != nil && !p.blockedSince.IsZero() {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Wire it into `tabStyle`**

Replace `internal/tui/model.go:4619-4632` (the doc comment through the `unseenTabStyle` return):

```go
// tabStyle returns the lipgloss style for the tab at idx. Precedence: amber
// blocked mark (a pane parked on the user) > green unseen mark (background tab
// with an unfocused finished pane, OR a tab containing a pane pinned for
// attention via the context menu) > custom tab color > active/inactive default.
// Shared by renderTabBar and hitTestTab so rendered widths and click
// hit-testing never diverge.
func (m Model) tabStyle(idx int) lipgloss.Style {
	tab := m.curTabs()[idx]
	active := idx == m.activeTabIdx()
	// Blocked first: a parked agent is a question waiting on the user, and it
	// includes the ACTIVE tab because the pane may be in an unfocused split.
	if m.tabBlocked(idx) {
		return blockedTabStyle
	}
	// tabUnseen self-excludes the active tab; tabPinnedAttention deliberately
	// does not (a pin colors the active tab's label unless the pinned pane is
	// the one in focus).
	if m.tabUnseen(idx) || m.tabPinnedAttention(idx) {
		return unseenTabStyle
	}
```

Leave the rest of the function (custom colour, active/inactive) unchanged.

- [ ] **Step 6: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS, including the two new tests.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/workstate.go internal/tui/model.go internal/tui/styles.go internal/tui/attention_test.go
git commit -m "feat(tui): mark tabs holding a pane parked on the user"
```

---

### Task 2: park stops meaning "not working"

Item 1. The one-line root-cause fix, plus the tests that pin why it is safe.

**Files:**
- Modify: `internal/tui/workstate.go:282-292` (the `workPark` arm)
- Test: `internal/tui/workstate_test.go`

**Interfaces:**
- Consumes: `tabBlocked` / `blockedTabStyle` from Task 1.
- Produces: no new symbols. Changes the observable behaviour of `workPark`: `turnActive` is preserved, so `pane.working` stays true across a permission park.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/workstate_test.go`:

```go
// TestWorkPark_PermissionPromptKeepsTurnActive pins item 1. Claude fires no
// hook when the user APPROVES a Bash/Edit/Write prompt — the pane's next event
// is the turn's Stop — so a park that cleared turnActive left the pane reading
// "blocked" for the entire time the agent was actually working.
//
// Notification covers two situations and the distinction is the whole fix:
// a permission prompt arrives mid-turn (turnActive true), an idle-wait nudge
// arrives after Stop already cleared it (turnActive false).
func TestWorkPark_PermissionPromptKeepsTurnActive(t *testing.T) {
	t.Parallel()
	const (
		start = "hook.claude.UserPromptSubmit"
		park  = "hook.claude.Notification"
		stop  = "hook.claude.Stop"
	)
	tests := []struct {
		name        string
		events      []string
		wantWorking bool
	}{
		{"permission park mid-turn", []string{start, park}, true},
		{"idle-wait park after stop", []string{start, stop, park}, false},
		{"stop after a park ends the turn", []string{start, park, stop}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModelWithTabs(t, 1)
			pane := m.curTabs()[0].Leaves()[0]
			for _, ev := range tt.events {
				m.applyWorkTransition(pane.ID, ev, nil)
			}
			if pane.working != tt.wantWorking {
				t.Errorf("working = %v, want %v", pane.working, tt.wantWorking)
			}
		})
	}
}

// TestWorkPark_SetsBlockedRegardlessOfTurnState pins that preserving
// turnActive did not cost the blocked mark — that mark is what the sidebar ▲
// and the new tabBlocked both read.
func TestWorkPark_SetsBlockedRegardlessOfTurnState(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1)
	pane := m.curTabs()[0].Leaves()[0]
	m.applyWorkTransition(pane.ID, "hook.claude.UserPromptSubmit", nil)
	m.applyWorkTransition(pane.ID, "hook.claude.PermissionRequest",
		map[string]string{"tool": "Bash"})
	if pane.blockedSince.IsZero() {
		t.Error("blockedSince should be set by a park")
	}
	if pane.blockedReason != "Bash" {
		t.Errorf("blockedReason = %q, want %q", pane.blockedReason, "Bash")
	}
	if !pane.working {
		t.Error("a permission park must not stop the spinner")
	}
}
```

`applyWorkTransition(paneID, eventType string, data map[string]string)` (`workstate.go:178`) is the entry point — it resolves the pane itself and maps the event string through `workEventKind`, so tests drive it with the wire event names rather than the `workEventKind` constants. `PermissionRequest` and `Notification` both classify as `workPark` (`hookevents/workstate.go:77`); the test uses both so a future split of those two triggers fails here.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `working = false, want true` for "permission park mid-turn".

- [ ] **Step 3: Make the change**

In `internal/tui/workstate.go`, replace the `workPark` arm at lines 282-292:

```go
	case workPark:
		// Blocked waiting on the user. This deliberately does NOT clear
		// turnActive. Notification covers two situations: a permission prompt
		// (turn still running) and an idle-wait nudge (Stop already cleared
		// turnActive), so clearing it here was a no-op exactly when it was
		// right and wrong exactly when it was not — approving a Bash/Edit/Write
		// prompt fires no hook of its own, so the pane read as blocked-not-
		// working until the turn's Stop, sometimes for minutes.
		//
		// Consequence: `working` no longer falls here, so the falling edge
		// below does not set `unseen`. tabBlocked (model.go tabStyle) is what
		// carries a parked background pane to the tab bar instead — the two
		// must not be separated.
		pane.blockedSince = time.Now()
		// Data["tool"] is set by the claude hook only for PermissionRequest
		// and PostToolUse. Notification and opencode's permission.ask may
		// carry no tool, so the reason is genuinely optional — render a bare
		// marker rather than inventing one.
		pane.blockedReason = data["tool"]
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS. If a pre-existing test asserts that a park sets `unseen` or clears `working`, it is asserting the bug — update it to the new behaviour and say so in its comment.

- [ ] **Step 5: Run the full suite**

Run: `./scripts/dev.sh test`
Expected: 28/28 packages ok.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/workstate.go internal/tui/workstate_test.go
git commit -m "fix(tui): keep the spinner running while a pane is parked"
```

---

### Task 3: focusing a pane clears its blocked mark

> **SUPERSEDED — do not implement this task as written.** The behaviour below
> was reversed during the same effort and never shipped. On master, focusing a
> pane clears `unseen` ONLY; `blockedSince` and `blockedReason` survive focus,
> and `paneRow` suppresses the glyph for the focused pane instead. Real input
> (`answerBlockedByInput`) is what clears the blocked state.
>
> Why the reversal: `ackFocusedPane` runs before Update's switch on EVERY
> message, and the 1 s size poll guarantees one — so clearing the mark on focus
> erased it roughly one tick after it was set, which is indistinguishable from
> a mark that was never set at all. It also withdrew the desktop toast raised
> on that mark's rising edge before the user could act on it.
>
> See `internal/tui/pane.go:136` (the `blockedSince` field comment),
> `Model.ackFocusedPane` in `internal/tui/model.go`, and
> `internal/tui/ctxmenu.go:140`. Kept below as the historical record of what
> was planned, not as an instruction.

Item 6.2.

**Files:**
- Modify: `internal/tui/workstate.go:341-363` (`ackFocusedPane`)
- Test: `internal/tui/attention_clear_test.go`

**Interfaces:**
- Consumes: `tabBlocked` (Task 1) for the tab-level assertion.
- Produces: no new symbols. `ackFocusedPane` now clears `blockedSince` + `blockedReason` in addition to `unseen`.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/attention_clear_test.go`:

```go
// TestAckFocusedPane_ClearsBlocked pins item 6.2. Focusing the pane is the
// acknowledgement — you are looking straight at the prompt. pinnedAttention
// deliberately SURVIVES: it is the explicit "don't let me forget" mark, and
// auto-clearing it would leave no way to express that.
func TestAckFocusedPane_ClearsBlocked(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1)
	tab := m.curTabs()[0]
	pane := tab.Leaves()[0]
	tab.ActivePane = pane.ID
	pane.unseen = true
	pane.blockedSince = time.Now()
	pane.blockedReason = "Bash"
	pane.pinnedAttention = true

	m.ackFocusedPane()

	if pane.unseen {
		t.Error("unseen should be cleared")
	}
	if !pane.blockedSince.IsZero() {
		t.Error("blockedSince should be cleared by focus")
	}
	if pane.blockedReason != "" {
		t.Errorf("blockedReason = %q, want empty", pane.blockedReason)
	}
	if !pane.pinnedAttention {
		t.Error("pinnedAttention must survive focus")
	}
}

// TestAckFocusedPane_TabMarkSurvivesOtherBlockedPane pins the level rule the
// user asked for: visiting one pane clears that pane, and clears the TAB only
// when no sibling still holds a mark.
func TestAckFocusedPane_TabMarkSurvivesOtherBlockedPane(t *testing.T) {
	t.Parallel()
	m := newTestModelWithTabs(t, 1)
	tab := m.curTabs()[0]
	panes := tab.Leaves()
	if len(panes) < 2 {
		t.Skip("fixture needs a two-pane tab")
	}
	tab.ActivePane = panes[0].ID
	panes[0].blockedSince = time.Now()
	panes[1].blockedSince = time.Now()

	m.ackFocusedPane()

	if !panes[0].blockedSince.IsZero() {
		t.Error("focused pane should be cleared")
	}
	if panes[1].blockedSince.IsZero() {
		t.Error("unfocused sibling must keep its mark")
	}
	if !m.tabBlocked(0) {
		t.Error("tab must stay marked while a sibling is still blocked")
	}
}
```

If the fixture builder produces single-pane tabs, extend it to take a pane count rather than adding a second builder, and update its existing callers.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — "blockedSince should be cleared by focus".

- [ ] **Step 3: Make the change**

In `internal/tui/workstate.go`, in the loop body of `ackFocusedPane` (lines 357-362):

```go
	for _, p := range tab.Leaves() {
		if p != nil && p.ID == tab.ActivePane {
			p.unseen = false
			// Focus also answers the blocked mark: you are looking at the
			// prompt. With workPark no longer clearing turnActive the spinner
			// keeps running, so a pane cleared here still reads as working
			// rather than idle. pinnedAttention is deliberately untouched —
			// only ctxActAttention / ctxActClearAttention own that flag.
			p.blockedSince = time.Time{}
			p.blockedReason = ""
			return
		}
	}
```

Also extend the function's doc comment (line 341) to name the blocked mark alongside the unseen one.

- [ ] **Step 4: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/workstate.go internal/tui/attention_clear_test.go
git commit -m "fix(tui): clear a pane's blocked mark when it takes focus"
```

---

### Task 4: render the pinned-attention mark in the sidebar

Item 6.3. `pinnedAttention` is fully implemented and drives the pane border, but `paneRow` never showed it.

**Files:**
- Modify: `internal/tui/sidebar.go:405-427` (glyph + style vars), `sidebar.go:616-643` (`paneRow` switch)
- Modify: `internal/tui/sidebar_glyphwidth_test.go:38-41` (glyph map)
- Test: `internal/tui/sidebar_test.go`

**Interfaces:**
- Consumes: `PaneModel.pinnedAttention` (existing, `pane.go:109`).
- Produces: `glyphPinned = "◆"` and `sidebarPinnedStyle`, referenced by no later task.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/sidebar_test.go`:

```go
// TestPaneRow_RendersPinnedAttention pins item 6.3. pinnedAttention already
// drove the pane border and the tab label; the sidebar row — the one place
// that lists every pane at once — never showed it.
func TestPaneRow_RendersPinnedAttention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(p *PaneModel)
		wantSub string
	}{
		{"pinned alone", func(p *PaneModel) { p.pinnedAttention = true }, glyphPinned},
		{"pinned and blocked keeps the blocked glyph", func(p *PaneModel) {
			p.pinnedAttention = true
			p.blockedSince = time.Now()
		}, glyphBlocked},
		{"pinned and blocked still shows the pin", func(p *PaneModel) {
			p.pinnedAttention = true
			p.blockedSince = time.Now()
		}, glyphPinned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &PaneModel{ID: "p1", Name: "agent"}
			tt.setup(pane)
			got := paneRow(pane, false, 30)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("paneRow = %q, want it to contain %q", got, tt.wantSub)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `undefined: glyphPinned`.

- [ ] **Step 3: Add the glyph and style**

In `internal/tui/sidebar.go`, in the glyph const block (after line 412):

```go
	glyphPinned  = "◆" // attention pinned by hand — never auto-cleared
```

and in the style var block (after line 426):

```go
	sidebarPinnedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
```

U+25C6 is not emoji-capable and measures one cell — both required by the sidebar glyph rule.

- [ ] **Step 4: Render it in `paneRow`**

In `internal/tui/sidebar.go`, add a case to the switch (between the `pane.working` case ending at line 638 and the `pane.unseen` case at line 639):

```go
	case pane.pinnedAttention:
		glyph, style = glyphPinned, sidebarPinnedStyle
```

and immediately after the switch closes (line 643), before the label is built:

```go
	// A pin outranked by a live state must still be visible — it is the mark
	// that deliberately survives focus, so losing it to a transient blocked or
	// working state would make "don't let me forget" forgettable.
	if pane.pinnedAttention && glyph != glyphPinned {
		suffix += " " + glyphPinned
	}
```

- [ ] **Step 5: Add the glyph to the emoji-capability test**

In `internal/tui/sidebar_glyphwidth_test.go`, extend the map at lines 38-41:

```go
		"glyphPinned":  glyphPinned,
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS, including `TestSidebarGlyphs_OneCellAndNotEmojiCapable` with the new entry.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_glyphwidth_test.go internal/tui/sidebar_test.go
git commit -m "feat(tui): show pinned attention on sidebar pane rows"
```

---

### Task 5: sidebar tab headings get an ordinal and a readable colour

Item 2.

**Files:**
- Modify: `internal/tui/sidebar.go:546-557` (`sidebarTabHeading`), `sidebar.go:186` (its one call site), style var block
- Test: `internal/tui/sidebar_test.go`

**Interfaces:**
- Consumes: `TabModel.Color` (existing string field, same value the tab bar uses as a background at `model.go:4634`).
- Produces: `func sidebarTabHeading(name string, idx int, active bool, color string, w int) string` — signature change; `sidebar.go:186` is the only caller in non-test code. Existing tests calling the 3-arg form must be updated.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/sidebar_test.go`:

```go
// TestSidebarTabHeading_OrdinalAndColor pins item 2. The heading and the idle
// pane rows beneath it both painted with sidebarDimStyle (color 243), so the
// grouping the PANES section exists to show was invisible.
func TestSidebarTabHeading_OrdinalAndColor(t *testing.T) {
	t.Parallel()
	got := sidebarTabHeading("build", 1, false, "", 22)
	if !strings.Contains(got, "2:build") {
		t.Errorf("heading = %q, want the 1-based ordinal %q", got, "2:build")
	}
	if strings.Contains(got, "243") {
		t.Errorf("inactive heading still uses the dim colour shared with idle pane rows: %q", got)
	}
}

// TestSidebarTabHeading_ElidesNameKeepsOrdinal pins that the ordinal survives a
// narrow strip — it is the part that maps the row to Alt+1..9, so truncating it
// away would cost the row its only navigational value.
func TestSidebarTabHeading_ElidesNameKeepsOrdinal(t *testing.T) {
	t.Parallel()
	got := sidebarTabHeading("a-very-long-tab-name-indeed", 0, false, "", 14)
	if !strings.Contains(got, "1:") {
		t.Errorf("heading = %q, want it to keep the %q prefix", got, "1:")
	}
	if w := lipgloss.Width(got); w > 14 {
		t.Errorf("heading width = %d, want <= 14", w)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — too many arguments to `sidebarTabHeading`.

- [ ] **Step 3: Add the default style**

In `internal/tui/sidebar.go`, in the style var block:

```go
	sidebarTabNameStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
```

- [ ] **Step 4: Rewrite `sidebarTabHeading`**

Replace `internal/tui/sidebar.go:546-557`:

```go
// sidebarTabHeading renders one tab's name above its panes. The active tab
// carries the same ▸ marker as the active project, in the same column, so the
// two read as one vocabulary rather than two conventions.
//
// White by default rather than dim: the heading shared sidebarDimStyle with an
// idle pane row, which made a tab heading indistinguishable from the panes
// under it. A user-chosen tab colour is applied as the FOREGROUND here — the
// tab bar uses the same value as a background, but a 22-column strip painting
// full-width colour blocks reads as noise rather than grouping.
//
// The 1-based ordinal matches the tab bar's "%d:%s" and the Alt+1..9 keys, and
// is placed before the name so a narrow strip elides the name and keeps the
// number.
func sidebarTabHeading(name string, idx int, active bool, color string, w int) string {
	marker := "  "
	style := sidebarTabNameStyle
	if color != "" {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	}
	if active {
		marker = "▸ "
		style = sidebarActiveStyle
		if color != "" {
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color))
		}
	}
	ordinal := fmt.Sprintf("%d:", idx+1)
	avail := w - lipgloss.Width(marker) - lipgloss.Width(ordinal)
	if avail < 1 {
		avail = 1
	}
	return style.Render(truncateCells(marker+ordinal+elideMiddle(name, avail), w))
}
```

- [ ] **Step 5: Update the call site**

`internal/tui/sidebar.go:186`:

```go
		rows = append(rows, sidebarRow{text: sidebarTabHeading(sanitizeRemoteText(tab.Name), ti, onTab, tab.Color, w)})
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS. Existing row-layout fixtures in `sidebar_test.go` that hard-code heading text will need their expected strings updated to carry the `N:` prefix — that is the documented cost of changing a sidebar row.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_test.go
git commit -m "feat(tui): number and brighten sidebar tab headings"
```

---

### Task 6: right-click a sidebar pane row opens its context menu

Item 4. Two parts — the missing branch, and the silent no-op it would otherwise expose.

**Files:**
- Modify: `internal/tui/model.go:1181-1188` (the `tea.MouseRight` branch inside the sidebar swallow)
- Modify: `internal/tui/ctxmenu.go:511+` (`executeCtxMenuItem` tab resolution)
- Test: `internal/tui/ctxmenu_dispatch_test.go`

**Interfaces:**
- Consumes: `Model.sidebarHit(x, y) (string, int)`, `sidebarRowPane`, `Model.findPaneAndTab(paneID) (*PaneModel, *ProjectModel, int)` (`workstate.go:55`), the existing pane context-menu opener.
- Produces: no new exported symbols.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/ctxmenu_dispatch_test.go`:

```go
// TestSidebarRightClick_OpensPaneCtxMenu pins item 4. The MouseRight branch
// inside the sidebar swallow tested only sidebarRowProject, so a right-click on
// a pane row fell through to `return m, nil` — no menu, no feedback.
//
// Driven through Update, not by calling the handler: the bug IS a branch the
// call site never enters, and a direct-call test would pass against it.
func TestSidebarRightClick_OpensPaneCtxMenu(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	x, y := sidebarPaneRowCoords(t, &m, 0)

	updated, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	got := updated.(Model)

	if !got.ctxMenu.open() {
		t.Fatal("right-click on a sidebar pane row should open the pane context menu")
	}
}

// TestSidebarRightClick_BackgroundTabPaneMenuActs pins the wrinkle. Opening the
// menu is not enough: executeCtxMenuItem resolved its target with
// activeTabModel(), so a pane living in a BACKGROUND tab produced a menu whose
// every item silently did nothing.
func TestSidebarRightClick_BackgroundTabPaneMenuActs(t *testing.T) {
	t.Parallel()
	m := newTestModelWithSidebar(t)
	// A pane row belonging to a tab that is not the active one.
	pane, _, tabIdx := m.findPaneAndTab(backgroundTabPaneID(t, &m))
	if pane == nil || tabIdx == m.activeTabIdx() {
		t.Fatal("fixture must provide a pane on a background tab")
	}
	m.openCtxMenu(pane, 0, 0)

	updated, _ := m.executeCtxMenuItem(ctxMenuItem{id: ctxActAttention, enabled: true})
	got := updated.(Model)

	target, _, _ := got.findPaneAndTab(pane.ID)
	if target == nil || !target.pinnedAttention {
		t.Error("a menu opened on a background-tab pane must act on that pane")
	}
}
```

`executeCtxMenuItem` switches on `item.id` and returns early unless `item.enabled` — both fields are required in the literal. Match the existing `ctxmenu_dispatch_test.go` fixtures for how a `ctxMenuItem` is built there. `newTestModelWithSidebar` / `sidebarPaneRowCoords` / `backgroundTabPaneID` are helpers: `sidebar_test.go` already hit-tests pane rows around lines 175-186 — reuse those fixtures rather than adding new ones.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — "right-click on a sidebar pane row should open the pane context menu".

- [ ] **Step 3: Accept pane rows in the right-click branch**

Replace `internal/tui/model.go:1181-1188`:

```go
			case tea.MouseRight:
				// Rename/Destroy for a project row, and the pane menu for a
				// pane row. The pane context menu further down never reaches
				// here — the sidebar swallow returns first — so both kinds
				// need their own open call.
				switch kind, idx := m.sidebarHit(msg.X, msg.Y); kind {
				case sidebarRowProject:
					if idx >= 0 && idx < len(m.projects) {
						m.openProjectCtxMenu(m.projects[idx], msg.X, msg.Y)
					}
				case sidebarRowPane:
					// Deliberately does NOT focus the pane first. Mirroring
					// left-click would also fix the background-tab problem
					// below, but a right-click that silently switches tabs is
					// a surprise; executeCtxMenuItem resolves the owning tab
					// instead.
					if row, ok := m.sidebarRowAt(msg.X, msg.Y); ok && row.paneID != "" {
						if pane, _, _ := m.findPaneAndTab(row.paneID); pane != nil {
							m.openCtxMenu(pane, msg.X, msg.Y)
						}
					}
				}
			}
```

`openCtxMenu(pane *PaneModel, anchorX, anchorY int)` already exists (`ctxmenu.go:348`) and is the same opener the in-pane right-click uses — do not add a second one. `sidebarRowAt` is used rather than `sidebarHit` because only the row carries `paneID`; `sidebarHit` returns a pane *ordinal*.

- [ ] **Step 4: Resolve the owning tab in `executeCtxMenuItem`**

In `internal/tui/ctxmenu.go`, replace lines 537-552 — from `tab := m.activeTabModel()` through the `pane.Active = true` block:

```go
	// The menu may have been opened from the SIDEBAR on a pane in a background
	// tab, so the active tab is not necessarily the owner. Resolving by pane id
	// is what stops such a menu from rendering fully and then doing nothing.
	//
	// The tab comes from the project findPaneAndTab returned, NOT curTabs():
	// that helper spans every project while curTabs() is the active project's
	// slice alone, so indexing curTabs() with a foreign tabIdx would act on an
	// unrelated tab — or panic.
	pane, proj, tabIdx := m.findPaneAndTab(paneID)
	if pane == nil || proj == nil || tabIdx < 0 || tabIdx >= len(proj.tabs) {
		return m, nil // target vanished between open and execute
	}
	tab := proj.tabs[tabIdx]
	if tab == nil || tab.Root == nil || tab.Root.FindLeaf(paneID) == nil {
		return m, nil
	}
	// Sync the Active bool alongside ActivePane — mirrors the mouse-release
	// pane-focus path (model.go) and NavigateDirection (tab.go). Leaving
	// the old pane's Active flag set would keep its purple border while
	// the real target renders inactive; ActivePaneModel() only heals a
	// stale ID, never a stale flag.
	if old := tab.ActivePaneModel(); old != nil {
		old.Active = false
	}
	tab.ActivePane = paneID
	pane.Active = true
```

Note this sets `tab.ActivePane` on the OWNING tab, which is what makes the menu act on the right pane. It does not switch the active tab — the user stays where they were, which is the point of not focusing on right-click.

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/ctxmenu.go internal/tui/ctxmenu_dispatch_test.go
git commit -m "fix(tui): open the pane context menu from sidebar rows"
```

---

### Task 7: scroll the PANES section

Item 5, core. PROJECTS stays pinned; the PANES body windows at a scroll offset.

**Files:**
- Modify: `internal/tui/sidebar.go:131-207` (`sidebarRows` returns the section boundary), `sidebar.go:243-266` (`sidebarVisibleRows`), `sidebar.go:222-241` (`renderSidebar` call), `sidebar.go:273-288` (`sidebarRowAt` call)
- Modify: `internal/tui/model.go` (add the `sidebarScroll` field to `Model`)
- Test: `internal/tui/sidebar_test.go`

**Interfaces:**
- Consumes: `padOrTrunc`, `sidebarDimStyle`, `Model.sidebarContentHeight()`.
- Produces:
  - `func (m *Model) sidebarRows(w int) ([]sidebarRow, int)` — **signature change**, second value is the index of the first row after the `PANES` heading.
  - `func clampSidebarScroll(off, bodyLen, bodyH int) int`
  - `func maxSidebarScrollFor(bodyLen, bodyH int) int`
  - `const minPaneRows = 3`
  - `Model.sidebarScroll int`
  Tasks 8 and 9 consume all of these.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/sidebar_test.go`:

```go
// TestSidebarVisibleRows_PanesScrollProjectsPinned pins item 5. Paint and hit
// test both call sidebarVisibleRows with the same height, deliberately — so the
// offset must live INSIDE it. A cap or an offset applied at the render site is
// the row-drift bug ("click project 3, select project 2") in another form.
func TestSidebarVisibleRows_PanesScrollProjectsPinned(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8) // 3 projects, 8 panes on the active one
	w, height := 22, 12

	all, panesStart := m.sidebarRows(w)
	if len(all) <= height {
		t.Fatal("fixture must overflow the strip")
	}

	m.sidebarScroll = 0
	first := m.sidebarVisibleRows(w, height)
	if len(first) != height {
		t.Errorf("visible rows = %d, want exactly %d", len(first), height)
	}
	for i := 0; i < panesStart; i++ {
		if first[i].text != all[i].text {
			t.Errorf("row %d: PROJECTS block must be pinned, got %q want %q",
				i, first[i].text, all[i].text)
		}
	}

	m.sidebarScroll = 2
	scrolled := m.sidebarVisibleRows(w, height)
	for i := 0; i < panesStart; i++ {
		if scrolled[i].text != all[i].text {
			t.Errorf("row %d changed while scrolled — PROJECTS is not pinned", i)
		}
	}
	if scrolled[panesStart].text == first[panesStart].text {
		t.Error("the PANES body did not move when sidebarScroll changed")
	}
}

// TestSidebarVisibleRows_HitTestMatchesPaint is the assertion that matters:
// window the rows, then resolve a click at a screen row and check it lands on
// the pane actually painted there. Testing either alone cannot catch drift.
func TestSidebarVisibleRows_HitTestMatchesPaint(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13 // sidebarContentHeight() == 12
	m.sidebarScroll = 3

	w := m.projectSidebarWidth()
	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	for y, row := range rows {
		if row.kind != sidebarRowPane {
			continue
		}
		gotRow, ok := m.sidebarRowAt(1, y)
		if !ok || gotRow.paneID != row.paneID {
			t.Fatalf("screen row %d paints pane %q but hit-tests to %q",
				y, row.paneID, gotRow.paneID)
		}
	}
}

// TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation pins the
// degenerate case: when the pinned PROJECTS block alone would leave fewer than
// minPaneRows for the body, the whole strip reverts to the pre-scroll tail cap
// rather than hiding the PANES section outright.
func TestSidebarVisibleRows_ShortStripFallsBackToTailTruncation(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 8, 4)
	rows := m.sidebarVisibleRows(22, 6)
	if len(rows) != 6 {
		t.Fatalf("visible rows = %d, want 6", len(rows))
	}
	if !strings.Contains(rows[5].text, "…") {
		t.Errorf("last row = %q, want the overflow marker", rows[5].text)
	}
}
```

`newTestModelManyPanes(t, projects, panes)` builds a `Model` with N projects and M panes on the active project's tabs. Add it beside the existing sidebar fixtures if nothing equivalent exists.

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `m.sidebarRows(w)` returns one value, `m.sidebarScroll` undefined.

- [ ] **Step 3: Add the model field**

In `internal/tui/model.go`, in the `Model` struct beside the other sidebar fields (`sidebarWidth`, `sidebarDragW`):

```go
	// sidebarScroll is the PANES section's scroll offset in rows. The PROJECTS
	// block is pinned and never scrolls. Written only by scrollSidebar /
	// scrollSidebarToPane; sidebarVisibleRows clamps a local copy, because it
	// runs on the render path and a render that writes model state is how a
	// paint and a hit test come to disagree.
	sidebarScroll int
```

- [ ] **Step 4: Return the section boundary from `sidebarRows`**

In `internal/tui/sidebar.go`, change the signature at line 135 and capture the boundary right after the `PANES` heading is appended (line 164):

```go
func (m *Model) sidebarRows(w int) ([]sidebarRow, int) {
```

```go
	rows = append(rows, sidebarRow{}, sidebarRow{text: sidebarHeading("PANES", w)})
	// Everything from here on is the scrollable body. The builder is the only
	// thing that knows where the section starts; deriving it later by matching
	// the heading text would be a second source of truth for one boundary.
	panesStart := len(rows)
```

and return `rows, panesStart` at line 206.

- [ ] **Step 5: Add the clamp helpers**

In `internal/tui/sidebar.go`, above `sidebarVisibleRows`:

```go
// minPaneRows is the floor of PANES rows sidebarVisibleRows will leave. When
// the pinned PROJECTS block alone would push the body below it, the whole strip
// reverts to the pre-scroll tail cap: a strip showing eight projects and no
// panes is worse than one showing a truncated list of both.
const minPaneRows = 3

// maxSidebarScrollFor is the largest offset that still paints content. One row
// of the window goes to the "N above" marker as soon as the body is scrolled at
// all, so the last page holds bodyH-1 rows.
func maxSidebarScrollFor(bodyLen, bodyH int) int {
	if bodyH <= 1 || bodyLen <= bodyH {
		return 0
	}
	if max := bodyLen - (bodyH - 1); max > 0 {
		return max
	}
	return 0
}

// clampSidebarScroll bounds an offset into a body of bodyLen rows shown through
// a bodyH-row window. Pure — callers that own the stored offset write it back
// themselves.
func clampSidebarScroll(off, bodyLen, bodyH int) int {
	if off < 0 {
		return 0
	}
	if max := maxSidebarScrollFor(bodyLen, bodyH); off > max {
		return max
	}
	return off
}
```

- [ ] **Step 6: Window the body in `sidebarVisibleRows`**

Replace `internal/tui/sidebar.go:258-266` (keep the existing doc comment above it, extended with the pinned-head rule):

```go
func (m *Model) sidebarVisibleRows(w, height int) []sidebarRow {
	rows, panesStart := m.sidebarRows(w)
	if height <= 0 || len(rows) <= height {
		return rows
	}
	head, body := rows[:panesStart], rows[panesStart:]

	// Degenerate strip: the pinned head alone would starve the body. Fall back
	// to the pre-scroll behaviour for the WHOLE list.
	if panesStart > height-minPaneRows {
		out := append([]sidebarRow(nil), rows[:height]...)
		out[height-1] = sidebarRow{text: sidebarDimStyle.Render(padOrTrunc(" …", w))}
		return out
	}

	bodyH := height - len(head)
	off := clampSidebarScroll(m.sidebarScroll, len(body), bodyH)

	// Markers cost a row each and appear only on the side that has more. They
	// carry no kind, so sidebarHit treats them as inert chrome.
	avail := bodyH
	top := off > 0
	if top {
		avail--
	}
	bottom := off+avail < len(body)
	if bottom {
		avail--
	}
	if avail < 0 {
		avail = 0
	}
	end := off + avail
	if end > len(body) {
		end = len(body)
	}

	out := make([]sidebarRow, 0, height)
	out = append(out, head...)
	if top {
		out = append(out, sidebarRow{text: sidebarDimStyle.Render(
			padOrTrunc(fmt.Sprintf(" %s %d above", glyphMore, off), w))})
	}
	out = append(out, body[off:end]...)
	if bottom {
		out = append(out, sidebarRow{text: sidebarDimStyle.Render(
			padOrTrunc(fmt.Sprintf(" %s %d below", glyphMore, len(body)-end), w))})
	}
	return out
}
```

Add the marker glyph to the glyph const block:

```go
	glyphMore    = "⋯" // more rows in this direction (U+22EF)
```

U+22EF is already used by `paneRow` for the subagent count, so it is proven in this file. Deliberately **not** `▲`/`▼`: `▲` is `glyphBlocked`, and a scroll marker wearing the blocked glyph is the same confusion item 1 exists to remove.

- [ ] **Step 7: Fix the other `sidebarRows` caller**

`renderSidebar` (line 228) already calls `sidebarVisibleRows` and needs no change. Search for any remaining direct `sidebarRows(` call — including in tests — and give each the second return value.

Run: `grep -rn "sidebarRows(" internal/tui/`

- [ ] **Step 8: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/model.go internal/tui/sidebar_test.go
git commit -m "feat(tui): scroll the sidebar PANES section"
```

---

### Task 8: the mouse wheel drives the sidebar scroll

Item 5, input half. The wheel is already swallowed over the strip — this gives it an effect.

**Files:**
- Modify: `internal/tui/model.go:1464-1468` (the wheel swallow)
- Modify: `internal/tui/sidebar.go` (add `scrollSidebar`)
- Test: `internal/tui/sidebar_test.go`

**Interfaces:**
- Consumes: `clampSidebarScroll`, `maxSidebarScrollFor`, `minPaneRows`, `Model.sidebarScroll`, `Model.sidebarRows` (2-value), all from Task 7. `m.cfg.UI.MouseScrollLines` for the notch size (existing, defaults to 3 when < 1).
- Produces: `func (m *Model) scrollSidebar(up bool)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/sidebar_test.go`:

```go
// TestSidebarWheel_ScrollsPanesSection pins that a wheel notch over the strip
// moves the PANES body. The swallow at model.go:1466 already stopped the wheel
// reaching the pane beneath — it just did nothing with it.
func TestSidebarWheel_ScrollsPanesSection(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13

	updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
	down := updated.(Model)
	if down.sidebarScroll == 0 {
		t.Fatal("wheel down over the sidebar should scroll the PANES section")
	}

	updated, _ = down.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
	up := updated.(Model)
	if up.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after scrolling back up, want 0", up.sidebarScroll)
	}
}

// TestSidebarWheel_ClampsAtBothEnds pins that the offset cannot run past the
// content in either direction — an unclamped offset paints an empty strip that
// still hit-tests to rows nobody can see.
func TestSidebarWheel_ClampsAtBothEnds(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 8)
	m.width, m.height = 100, 13

	for i := 0; i < 50; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelDown})
		m = updated.(Model)
	}
	rows, panesStart := m.sidebarRows(m.projectSidebarWidth())
	want := maxSidebarScrollFor(len(rows)-panesStart, m.sidebarContentHeight()-panesStart)
	if m.sidebarScroll != want {
		t.Errorf("sidebarScroll = %d after over-scrolling, want the max %d", m.sidebarScroll, want)
	}

	for i := 0; i < 50; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{X: 5, Y: 5, Button: tea.MouseWheelUp})
		m = updated.(Model)
	}
	if m.sidebarScroll != 0 {
		t.Errorf("sidebarScroll = %d after over-scrolling up, want 0", m.sidebarScroll)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — "wheel down over the sidebar should scroll the PANES section".

- [ ] **Step 3: Add `scrollSidebar`**

In `internal/tui/sidebar.go`:

```go
// scrollSidebar moves the PANES section by one wheel notch. It owns the write
// to m.sidebarScroll — sidebarVisibleRows only ever clamps a local copy — and
// re-derives the body geometry from the same sidebarRows/height pair the paint
// uses, so the bound the user hits is the bound they can see.
func (m *Model) scrollSidebar(up bool) {
	w := m.projectSidebarWidth()
	if w <= 0 {
		return
	}
	height := m.sidebarContentHeight()
	rows, panesStart := m.sidebarRows(w)
	// Nothing to scroll, or the degenerate short strip that reverts to the
	// tail cap: in both cases the only correct offset is zero.
	if height <= 0 || len(rows) <= height || panesStart > height-minPaneRows {
		m.sidebarScroll = 0
		return
	}
	lines := m.cfg.UI.MouseScrollLines
	if lines < 1 {
		lines = 3
	}
	if up {
		lines = -lines
	}
	m.sidebarScroll = clampSidebarScroll(
		m.sidebarScroll+lines, len(rows)-panesStart, height-panesStart)
}
```

- [ ] **Step 4: Call it from the wheel handler**

Replace `internal/tui/model.go:1464-1468`:

```go
		// The project sidebar's reserved column scrolls its own PANES section;
		// the pane the wheel would otherwise scroll is not the one under the
		// cursor.
		if m.projectSidebarSwallowsMouse(msg.X, msg.Y) {
			m.scrollSidebar(msg.Button == tea.MouseWheelUp)
			return m, nil
		}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS. `sidebar_test.go:288` already sends a wheel event to assert the swallow — confirm it still passes and update its comment if it claimed the wheel was inert.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/model.go internal/tui/sidebar_test.go
git commit -m "feat(tui): scroll the sidebar with the mouse wheel"
```

---

### Task 9: scroll a focused pane into view

Item 5, optional polish. Drop this task without affecting any other if you want the smaller change.

**Files:**
- Modify: `internal/tui/sidebar.go` (add `scrollSidebarToPane`)
- Modify: `internal/tui/project.go:127` (`focusSidebarPane`) and the shared cross-tab jump entry point
- Test: `internal/tui/sidebar_test.go`

**Interfaces:**
- Consumes: everything Task 7 produced.
- Produces: `func (m *Model) scrollSidebarToPane(paneID string)`.

- [ ] **Step 1: Write the failing test**

```go
// TestScrollSidebarToPane_BringsOffscreenPaneIntoView pins that a pane reached
// from the palette, a hook jump or pane-history is not left below the cut.
func TestScrollSidebarToPane_BringsOffscreenPaneIntoView(t *testing.T) {
	t.Parallel()
	m := newTestModelManyPanes(t, 3, 12)
	m.width, m.height = 100, 13
	w := m.projectSidebarWidth()

	all, panesStart := m.sidebarRows(w)
	var last string
	for i := panesStart; i < len(all); i++ {
		if all[i].kind == sidebarRowPane {
			last = all[i].paneID
		}
	}
	if last == "" {
		t.Fatal("fixture must contain pane rows")
	}

	m.sidebarScroll = 0
	m.scrollSidebarToPane(last)

	rows := m.sidebarVisibleRows(w, m.sidebarContentHeight())
	found := false
	for _, r := range rows {
		if r.kind == sidebarRowPane && r.paneID == last {
			found = true
		}
	}
	if !found {
		t.Errorf("pane %q still off-screen at offset %d", last, m.sidebarScroll)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `m.scrollSidebarToPane undefined`.

- [ ] **Step 3: Implement it**

In `internal/tui/sidebar.go`:

```go
// scrollSidebarToPane moves the PANES section the minimum distance that brings
// paneID's row into the visible window, and does nothing when it is already
// there. The visible span is computed as bodyH-2 — the worst case, both markers
// present — so the target can never land underneath one.
func (m *Model) scrollSidebarToPane(paneID string) {
	if paneID == "" {
		return
	}
	w := m.projectSidebarWidth()
	if w <= 0 {
		return
	}
	height := m.sidebarContentHeight()
	rows, panesStart := m.sidebarRows(w)
	if height <= 0 || len(rows) <= height || panesStart > height-minPaneRows {
		m.sidebarScroll = 0
		return
	}
	idx := -1
	for i := panesStart; i < len(rows); i++ {
		if rows[i].kind == sidebarRowPane && rows[i].paneID == paneID {
			idx = i - panesStart
			break
		}
	}
	if idx < 0 {
		return
	}
	bodyLen, bodyH := len(rows)-panesStart, height-panesStart
	visible := bodyH - 2
	if visible < 1 {
		visible = 1
	}
	off := m.sidebarScroll
	switch {
	case idx < off:
		off = idx
	case idx >= off+visible:
		off = idx - visible + 1
	}
	m.sidebarScroll = clampSidebarScroll(off, bodyLen, bodyH)
}
```

- [ ] **Step 4: Call it when focus moves**

In `internal/tui/project.go`, at the end of `focusSidebarPane` (line 127) before it returns, and in the shared cross-tab jump helper (`jumpToPane`, the choke point for MCP `set_active_pane`, the notification sidebar, pane-history back and the palette):

```go
	m.scrollSidebarToPane(paneID)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/project.go internal/tui/sidebar_test.go
git commit -m "feat(tui): scroll a newly focused pane into sidebar view"
```

---

### Task 10: documentation

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `.claude/rules/projects.md` (the Sidebar section)
- Modify: `.claude/rules/hooks-and-sessions.md` (the Work state section)
- Modify: `docs/features.md` (sidebar entry, if it describes the rows)
- Modify: `internal/tui/sidebar.go` (two stale comments — see Step 2c)

**Four documentation defects found during Tasks 2, 5 and 7 — all in scope here:**

1. **`.claude/rules/hooks-and-sessions.md` teaches the inverted invariant.** Two
   places still say a park stops the spinner and sets the unseen mark: *"the
   park-for-input edges … (the agent is blocked on the user → stop spinner +
   unseen mark for attention)"* and *"Permission/option prompts park the spinner
   (stop + unseen mark)"*. Both are false after Task 2. This file **auto-loads for
   anyone opening `internal/tui/workstate.go`**, so it is actively teaching the
   opposite of the code.
2. **`.claude/rules/projects.md` now over-claims.** It records *"two-cell markers
   on both project and tab rows so the levels line up"* as a deliberate layout
   decision; after Task 7 the tab row's marker gives way below ~5 cells to keep
   the ordinal, while `projectRow`'s does not. Unreachable through the UI
   (`minSidebarWidth` is 12), but the rules file is the project's memory and now
   states an invariant with an unstated exception.
3. **Two comments in `internal/tui/sidebar.go` say `rows[y-1]` where the code does
   `rows[y]`** — around `truncateCells` (~`sidebar.go:882`) and in
   `renderSidebar`'s comment. `sidebarRowAt` returns `rows[y]` and its own doc
   says "screen row y is sidebar row y". This plan's own text inherited the wrong
   phrasing. Harmless before; actively misleading now that paint/hit-test row
   alignment is the subject.
4. **The right-click design reversed.** `docs/superpowers/specs/2026-08-08-sidebar-attention-fixes-design.md`
   has been amended already; make sure nothing else in the docs still says
   right-click leaves focus alone.

**Interfaces:**
- Consumes: the finished behaviour of Tasks 1-9.
- Produces: nothing code depends on.

- [ ] **Step 1: Add the changelog entry**

Under `## [Unreleased]`, in Keep a Changelog format:

```markdown
### Added
- Sidebar PANES section scrolls with the mouse wheel, with markers showing how
  many rows are hidden above and below. The PROJECTS list stays pinned.
- Sidebar tab headings show their 1-based number (matching Alt+1-9) and the
  tab's custom colour.
- Pinned attention is now visible on sidebar pane rows.
- Tabs holding a pane parked on a permission prompt are marked amber.

### Fixed
- A pane stayed marked "blocked on you" for the whole time an agent was working
  after a Bash/Edit/Write permission prompt was approved.
- Focusing a pane now clears its blocked mark, not just the unseen one.
- Right-clicking a pane row in the sidebar opens its context menu; the menu now
  acts on panes in background tabs instead of silently doing nothing.
```

- [ ] **Step 2a: Correct the hooks-and-sessions rule**

In `.claude/rules/hooks-and-sessions.md`, Work state section, replace both
statements that a park stops the spinner and marks the pane unseen. State what
is now true:

```markdown
Park-for-input edges (`hook.claude.Notification` / `PermissionRequest`,
`hook.opencode.permission.ask`) set `blockedSince` and do **NOT** clear
`turnActive`. `Notification` covers two situations — a permission prompt
(arrives mid-turn) and an idle-wait nudge (arrives after `Stop` already cleared
`turnActive`) — so clearing it was a no-op exactly when it was right and wrong
exactly when it was not: approving a Bash/Edit/Write prompt fires no hook of its
own, so the pane read as blocked-not-working until the turn's `Stop`.

Because `working` no longer falls on a park, the falling edge no longer sets
`unseen`. `tabBlocked` (`workstate.go`) + `blockedTabStyle` carry a parked
background pane to the tab bar instead, and `ackFocusedPane` clears
`blockedSince`/`blockedReason` alongside `unseen`. The two must not be separated.
```

- [ ] **Step 2b: Correct the projects rule's marker claim**

In `.claude/rules/projects.md`, the Sidebar section's layout-decisions sentence
about "two-cell markers on both project and tab rows so the levels line up" —
add the exception: `sidebarTabHeading` budgets ordinal → marker → name, so below
~5 cells the tab row's `▸ ` marker gives way to keep the ordinal, which is the
part that maps the row to `Alt+1..9`. `projectRow` is unchanged. Unreachable
through the UI (`minSidebarWidth` is 12); reachable only via a hand-edited
`sidebar_width`, since `sidebarWidth()` only falls back on `configured <= 0`.

- [ ] **Step 2c: Fix the two `rows[y-1]` comments**

In `internal/tui/sidebar.go`, the comment near `truncateCells` (~`:882`) and
`renderSidebar`'s comment both describe `sidebarRowAt` as mapping "screen row y
to `rows[y-1]`". It returns `rows[y]` — its own doc comment says so. Correct
both. Comment-only; change no code.

- [ ] **Step 2: Update the sidebar rule**

In `.claude/rules/projects.md`, in the `## Sidebar` section, record the two invariants a future change must not break:

```markdown
**The PANES section scrolls; PROJECTS is pinned.** `sidebarRows` returns the
section boundary as a second value rather than having anyone re-derive it from
the heading text, and the offset is applied INSIDE `sidebarVisibleRows` — the
same reason the cap is: paint and hit test share that one function, and a window
applied at the render site is the row-drift bug in another form.
`sidebarVisibleRows` stays PURE and clamps a local copy; `scrollSidebar` and
`scrollSidebarToPane` own the write to `m.sidebarScroll`. When the pinned head
would leave fewer than `minPaneRows` for the body the whole strip reverts to the
old tail cap — a strip showing every project and no panes is worse than a
truncated list of both.

**A park no longer clears `turnActive`.** `Notification` covers a permission
prompt (turn running) and an idle-wait nudge (Stop already fired), so clearing
it was a no-op exactly when it was right. The consequence is load-bearing: the
falling edge no longer fires, so a park does not set `unseen`, and `tabBlocked`
is what carries a parked background pane to the tab bar. The two must not be
separated.
```

- [ ] **Step 3: Verify the docs-size gate still passes**

Run: `./scripts/dev.sh docs-size`
Expected: PASS — `.claude/CLAUDE.md` under 100,000 bytes, each rule file under 140,000.

- [ ] **Step 4: Run the full suite one final time**

Run: `./scripts/dev.sh test`
Expected: 28/28 packages ok.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md .claude/rules/projects.md docs/features.md
git commit -m "docs: record the sidebar and attention-model fixes"
```

---

## Verification before calling this done

- [ ] `./scripts/dev.sh test` — 28/28 packages ok
- [ ] `./scripts/dev.sh vet` — clean
- [ ] `./scripts/dev.sh build` — all six binaries (close any running dev TUI first; the build refuses while a binary it would write is held)
- [ ] Launch `./quil-dev.exe`, confirm `[dev]` in the status bar, then check by hand:
  - a pane parked on a Bash permission prompt shows `▲` **and** keeps its spinner; its tab is amber
  - focusing that pane hides its `▲` but the tab stays amber, the project badge keeps counting it, and `Alt+Shift+A` still offers it; leaving the pane brings the `▲` back with no further hook event (revised item 6.2 — focus no longer clears the mark)
  - **answering** the prompt (approve the Bash prompt with a keystroke) clears the mark everywhere immediately — tab back to normal, badge counts it as working, gone from `Alt+Shift+A` — without waiting for the turn's `Stop`
  - scrolling the parked pane with the wheel, or dragging a text selection across it, does **not** clear the mark
  - a pinned pane keeps `◆`
  - sidebar tab headings read `1:name`, `2:name`, white or tab-coloured
  - right-click a pane row → menu opens; pick an item on a background-tab pane → it acts
  - shrink the terminal until the pane list overflows → `⋯ N below`, wheel scrolls, PROJECTS stays put
  - resize the terminal TALLER while scrolled to the bottom of a long pane list → the very next wheel-up notch moves the strip (no dead plateau)
  - `Alt+Shift+A` onto a pane below the fold → the sidebar scrolls it into view
- [ ] **Observe, do not assume: does Claude re-fire `Notification` while a permission prompt is still outstanding?** Park a pane on a prompt, leave it unanswered for several minutes, and watch whether further `hook.claude.Notification` events arrive (the daemon log, or `get_notifications`). Nothing in the code or the rules establishes this either way, and the answer changes how long a parked pane can go unmarked in the edge cases the revised 6.2 does not cover — if the idle nudge repeats, a mark lost to any other path returns on its own; if it fires once per park, it does not. Record the answer here.
