# Sidebar Width Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user set the project sidebar's width from the Settings dialog and by dragging its right edge with the mouse.

**Architecture:** The width is already a single config value (`ui.sidebar_width`) read through a single accessor (`Model.projectSidebarWidth()` → the pure `sidebarWidth(total, open, configured)`), which already clamps so `minTermWidth` columns always remain for panes. Nothing needs to be found or rewired. Task 1 adds a Settings row. Tasks 2–4 add a mouse drag on the boundary column that previews with a highlighted rule and commits the width on release, following `finishSplitDrag`'s deferred-resize shape.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2.

## Global Constraints

- Go files use tabs. `gofmt` is mandatory. The working copy is CRLF on Windows — judge formatting from `git show`'d blobs, never from `gofmt -l` on the checkout.
- Build and test through Docker only: `./scripts/dev.sh test`, `./scripts/dev.sh vet`, `./scripts/dev.sh test-race`. Go is not installed on the host.
- Never touch `~/.quil/` or the production daemon. Dev mode only.
- `sidebarWidth(total, open, configured)` in `internal/tui/sidebar.go:32` is the **single source of truth** for how much screen the sidebar takes. Every new path must route its value through it rather than clamping independently.
- A width change must be followed by `m.resizeTabs()` **before** `m.resizeAllPanes()`, then `tea.Batch(tea.ClearScreen, m.resizeAllPanes())`. `toggleProjectSidebar` (`model.go:2633`) documents why: `resizeAllPanes` reads `pane.Width`/`tab.CanvasW`, it does not compute them, and only `tab.Resize` writes them.
- Any setter that changes persistent config must set `m.configChanged = true`, or the edit is silently dropped on exit.
- Mid-drag, **`m.sidebarWidth` must not change.** `View()` calls `tab.Resize(m.paneAreaWidth()-notesW, tabH)` on every frame, and `ResizeVT`'s contract pairs every emulator resize with a PTY redraw — unpaired intermediate-width rewraps permanently garble pane content at the narrowest width crossed (the 2026-07-15 corruption bug). The drag stores a *pending* width and commits it on release.
- New drag state joins `Model.clearDragState()`, which `TestModel_ClearDragState` pins.

---

### Task 1: Settings row for the sidebar width

**Files:**
- Modify: `internal/tui/dialog.go:183-233` (`settingsFields`, insert after the "Page scroll lines" entry)
- Test: `internal/tui/sidebar_width_settings_test.go` (create)

**Interfaces:**
- Consumes: `sidebarWidth(total int, open bool, configured int) int` (`sidebar.go:32`), `defaultSidebarWidth = 22` (`sidebar.go:15`), `minWidthForSidebar = 100`.
- Produces: nothing later tasks depend on. The row is self-contained.

**Context an implementer cannot infer:** the Settings dialog's own doc comment says live re-application is intentionally NOT done there ("changes take effect on the next launch"). This row is a deliberate exception and the comment must say so: log level needs a file handle re-plumbed through `main.go`, which the Model does not own, whereas the sidebar width is read from `m.sidebarWidth` on every render — a visible layout control that did nothing until relaunch reads as a broken dialog. Applying it live is one assignment plus the resize sequence the toggle already performs.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// The Settings row must apply the width LIVE, not only on the next launch.
// A layout control the user is looking at that does nothing until relaunch
// reads as a broken dialog — see the exception noted on the field itself.
func TestSettingsField_SidebarWidth_AppliesLiveAndPersists(t *testing.T) {
	var field *settingsField
	for i, f := range settingsFields() {
		if f.label == "Sidebar width" {
			field = &settingsFields()[i]
			break
		}
	}
	if field == nil {
		t.Fatal("no \"Sidebar width\" row in settingsFields()")
	}

	cfg := config.Default()
	m := &Model{cfg: cfg, width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}

	if got := field.get(m); got != "22" {
		t.Errorf("get() = %q, want \"22\"", got)
	}

	field.set(m, "34")
	if m.sidebarWidth != 34 {
		t.Errorf("m.sidebarWidth = %d, want 34 (live application)", m.sidebarWidth)
	}
	if m.cfg.UI.SidebarWidth != 34 {
		t.Errorf("cfg.UI.SidebarWidth = %d, want 34 (persisted)", m.cfg.UI.SidebarWidth)
	}
	if !m.configChanged {
		t.Error("configChanged not set — the edit would be dropped on exit")
	}
}

// A value the clamp would reject outright is refused at the setter rather
// than written and silently corrected, so the dialog never displays a number
// the layout is not using.
func TestSettingsField_SidebarWidth_RejectsUnusableValues(t *testing.T) {
	var field *settingsField
	fields := settingsFields()
	for i := range fields {
		if fields[i].label == "Sidebar width" {
			field = &fields[i]
			break
		}
	}
	if field == nil {
		t.Fatal("no \"Sidebar width\" row in settingsFields()")
	}

	for _, val := range []string{"0", "-5", "abc", ""} {
		m := &Model{cfg: config.Default(), width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
		field.set(m, val)
		if m.sidebarWidth != 22 {
			t.Errorf("set(%q) changed the width to %d, want it left at 22", val, m.sidebarWidth)
		}
		if m.configChanged {
			t.Errorf("set(%q) flagged configChanged for a rejected value", val)
		}
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestSettingsField_SidebarWidth`
Expected: FAIL — `no "Sidebar width" row in settingsFields()`.

- [ ] **Step 3: Add the row**

Insert into `settingsFields()` immediately after the "Page scroll lines" entry (`dialog.go:233`):

```go
		{
			// The one Settings row that applies LIVE. The comment on
			// settingsFields says changes take effect on the next launch,
			// which is right for the log level (its file handle lives in
			// main.go and is not re-plumbed into the Model) and wrong here:
			// the sidebar width is read from m.sidebarWidth on every render,
			// so a visible layout control that did nothing until relaunch
			// would read as a broken dialog.
			label: "Sidebar width",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.UI.SidebarWidth) },
			set: func(m *Model, v string) {
				n, err := strconv.Atoi(v)
				// Refused rather than written-and-corrected: sidebarWidth()
				// clamps at render, so a stored 0 would display as 0 while
				// the layout used defaultSidebarWidth. The dialog must never
				// show a number the layout is not using.
				if err != nil || n <= 0 || n == m.cfg.UI.SidebarWidth {
					return
				}
				m.cfg.UI.SidebarWidth = n
				m.sidebarWidth = n
				m.configChanged = true
			},
		},
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestSettingsField_SidebarWidth`
Expected: PASS.

- [ ] **Step 5: Apply the resize after the edit commits**

The setter cannot return a `tea.Cmd` — `settingsField.set` has no return value, and widening the signature would touch every other row. The resize therefore fires where the Settings edit is committed. Find the call site (`dialog.go:579` or `dialog.go:1080`, whichever handles Enter on a non-bool field) and after `fields[idx].set(&m, val)` add:

```go
	// A sidebar-width edit changes paneAreaWidth() for every tab, so it owes
	// the same sequence as toggleProjectSidebar: resizeTabs FIRST (it is what
	// WRITES pane.Width/tab.CanvasW; resizeAllPanes only ships them), then a
	// ClearScreen because every column right of the strip shifts in one frame.
	if fields[idx].label == "Sidebar width" {
		m.resizeTabs()
		return m, tea.Batch(tea.ClearScreen, m.resizeAllPanes())
	}
```

- [ ] **Step 6: Verify the whole package and vet**

Run: `./scripts/dev.sh test ./internal/tui/` then `./scripts/dev.sh vet`
Expected: PASS, no vet findings.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/dialog.go internal/tui/sidebar_width_settings_test.go
git commit -F - <<'EOF'
feat(tui): set the project sidebar width from Settings

The width was configurable only by hand-editing ui.sidebar_width in
config.toml. Add it to the Settings dialog as the one row that applies
live, since it is a layout value the user is looking at while editing it.

Non-positive and unparseable values are refused at the setter rather than
stored and corrected at render, so the dialog never displays a number the
layout is not using.
EOF
```

---

### Task 2: Hit-test the sidebar's boundary column

**Files:**
- Modify: `internal/tui/sidebar.go` (add `hitTestSidebarEdge` beside `projectSidebarSwallowsMouse`)
- Test: `internal/tui/sidebar_drag_test.go` (create)

**Interfaces:**
- Consumes: `Model.projectSidebarWidth() int`, `Model.height int`.
- Produces: `func (m Model) hitTestSidebarEdge(x, y int) bool` — Task 3 arms the drag from it.

**Context an implementer cannot infer:** the hit zone is **two columns**, `w-1` and `w`. That mirrors the split-border convention of "the two drawn border glyphs", and it is needed for a concrete reason: `w-1` is the sidebar's own last column (inside `projectSidebarSwallowsMouse`'s strip) and `w` is the leftmost pane's left border. Neither is claimed by anything else — there is no split at the sidebar boundary, so `hitTestSplitBorder` never returns it — but a one-column zone on a boundary the user is aiming at with a mouse is unpleasantly precise.

The last row is excluded (`y < m.height-1`) for the same reason `projectSidebarSwallowsMouse` excludes it: the status bar is still drawn full width beneath the sidebar. Row 0 is **included**, matching that function — the sidebar's own first row occupies row 0 in these columns.

- [ ] **Step 1: Write the failing test**

```go
package tui

import "testing"

func TestHitTestSidebarEdge(t *testing.T) {
	// width 200 is well above minWidthForSidebar, so projectSidebarWidth()
	// returns the configured 22 and the boundary sits at columns 21 and 22.
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}

	tests := []struct {
		name string
		x, y int
		want bool
	}{
		{"sidebar's last column", 21, 10, true},
		{"first pane column", 22, 10, true},
		{"one left of the zone", 20, 10, false},
		{"one right of the zone", 23, 10, false},
		{"row 0 is included", 21, 0, true},
		{"status bar row is excluded", 21, 49, false},
		{"negative y", 21, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.hitTestSidebarEdge(tt.x, tt.y); got != tt.want {
				t.Errorf("hitTestSidebarEdge(%d, %d) = %v, want %v", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

// A closed sidebar and a terminal too narrow to spare one both report width 0
// through the same accessor. Neither may offer an edge to grab: there is no
// strip on screen, so a press at column -1/0 would arm a drag on nothing.
func TestHitTestSidebarEdge_NoEdgeWithoutASidebar(t *testing.T) {
	closed := Model{width: 200, height: 50, sidebarOpen: false, sidebarWidth: 22}
	if closed.hitTestSidebarEdge(0, 10) || closed.hitTestSidebarEdge(21, 10) {
		t.Error("a closed sidebar offers a drag edge")
	}
	narrow := Model{width: minWidthForSidebar - 1, height: 50, sidebarOpen: true, sidebarWidth: 22}
	if narrow.hitTestSidebarEdge(0, 10) || narrow.hitTestSidebarEdge(21, 10) {
		t.Error("a sidebar suppressed by terminal width offers a drag edge")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestHitTestSidebarEdge`
Expected: FAIL to compile — `m.hitTestSidebarEdge undefined`.

- [ ] **Step 3: Implement the hit test**

Add to `internal/tui/sidebar.go`, directly after `projectSidebarSwallowsMouse`:

```go
// sidebarEdgeHitPadding widens the drag zone to the sidebar's own last column
// as well as the first pane column, mirroring the split border's "both drawn
// glyphs grab the line" rule. A one-column target on a boundary the user aims
// at with a mouse is needlessly precise, and neither column is claimed by
// anything else: there is no split at the sidebar boundary, so
// hitTestSplitBorder never returns it.
const sidebarEdgeHitPadding = 1

// hitTestSidebarEdge reports whether (x, y) lands on the project sidebar's
// draggable right edge.
//
// Width 0 — a closed sidebar, or a terminal below minWidthForSidebar — offers
// NO edge. Both states come through projectSidebarWidth() as 0, and answering
// true there would arm a drag against a strip that is not painted.
//
// Row 0 is included and the last row excluded, matching
// projectSidebarSwallowsMouse: the sidebar's first row occupies row 0 in these
// columns, while the status bar is still drawn full width beneath it.
func (m Model) hitTestSidebarEdge(x, y int) bool {
	w := m.projectSidebarWidth()
	if w <= 0 || y < 0 || y >= m.height-1 {
		return false
	}
	return x >= w-sidebarEdgeHitPadding && x <= w
}
```

- [ ] **Step 4: Run the test and watch it pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestHitTestSidebarEdge`
Expected: PASS (both tests, all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_drag_test.go
git commit -F - <<'EOF'
feat(tui): hit-test the project sidebar's right edge

Groundwork for dragging the sidebar boundary. The zone covers the
sidebar's last column and the first pane column, matching the split
border's two-glyph rule; a suppressed or closed sidebar offers no edge.
EOF
```

---

### Task 3: Arm, track and commit the drag

**Files:**
- Modify: `internal/tui/model.go` — add drag state to the `Model` struct beside `splitDragNode` (~line 544), extend `clearDragState()` (~line 2325), add the `MouseClickMsg` branch (before line 1110), the `MouseMotionMsg` branch, and the `MouseReleaseMsg` branch
- Test: `internal/tui/sidebar_drag_test.go` (extend)

**Interfaces:**
- Consumes: `Model.hitTestSidebarEdge(x, y) bool` (Task 2), `sidebarWidth(total, open, configured) int`, `Model.resizeTabs()`, `Model.resizeAllPanes() tea.Cmd`.
- Produces: `Model.sidebarDragging bool` and `Model.sidebarDragW int` — Task 4 renders the preview rule from them.

**Context an implementer cannot infer, and it is the whole design:** mid-drag, **do not assign `m.sidebarWidth`.** `View()` calls `tab.Resize(m.paneAreaWidth()-notesW, tabH)` on every frame (`model.go:3089`), and `ResizeVT`'s contract pairs every emulator resize with a PTY redraw — unpaired intermediate-width rewraps permanently garble pane content at the narrowest width crossed. That is the 2026-07-15 corruption bug, and a live-following sidebar reproduces it once per motion event. Store the pending width in `sidebarDragW`, paint a rule there (Task 4), and commit once on release. This is the same deferral `finishSplitDrag` makes for the same reason.

The `MouseClickMsg` branch must sit **before** the `projectSidebarSwallowsMouse` check at `model.go:1110`, because column `w-1` is inside that strip and would otherwise be swallowed as a row click.

Clamping goes through `sidebarWidth()` rather than a fresh `min`/`max` pair, so the drag cannot reach a width the renderer would silently correct.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/sidebar_drag_test.go`:

```go
// The drag must NOT move m.sidebarWidth while it is in flight. View() calls
// tab.Resize on every frame, and an emulator resize unpaired with a PTY
// redraw permanently garbles pane content at the narrowest width crossed
// (the 2026-07-15 corruption bug). Mid-drag only the pending value moves.
func TestSidebarDrag_DoesNotResizeMidDrag(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)

	if m.sidebarWidth != 22 {
		t.Errorf("m.sidebarWidth = %d mid-drag, want it pinned at 22", m.sidebarWidth)
	}
	if m.sidebarDragW != 41 {
		t.Errorf("sidebarDragW = %d, want 41 (column 40 is the last sidebar column)", m.sidebarDragW)
	}
	if !m.sidebarDragging {
		t.Error("sidebarDragging false while a drag is in flight")
	}
}

// Release is the single commit point: it moves the real width, persists it,
// and flags the config so the value survives exit.
func TestSidebarDrag_ReleaseCommitsTheWidth(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.cfg.UI.SidebarWidth = 22
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)
	m.finishSidebarDrag()

	if m.sidebarWidth != 41 {
		t.Errorf("m.sidebarWidth = %d after release, want 41", m.sidebarWidth)
	}
	if m.cfg.UI.SidebarWidth != 41 {
		t.Errorf("cfg.UI.SidebarWidth = %d, want 41", m.cfg.UI.SidebarWidth)
	}
	if !m.configChanged {
		t.Error("configChanged not set — the dragged width would be lost on exit")
	}
	if m.sidebarDragging {
		t.Error("sidebarDragging still set after release")
	}
}

// The clamp is sidebarWidth()'s, not a second copy: a drag past the right
// edge must land on the same value the renderer would have used, or the
// sidebar silently disagrees with where the user let go.
func TestSidebarDrag_ClampsThroughSidebarWidth(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(500) // far past the terminal's right edge
	m.finishSidebarDrag()

	want := sidebarWidth(200, true, 500)
	if m.sidebarWidth != want {
		t.Errorf("m.sidebarWidth = %d, want %d (sidebarWidth's own clamp)", m.sidebarWidth, want)
	}
	if m.sidebarWidth >= m.width {
		t.Errorf("m.sidebarWidth = %d leaves no room for panes at width %d", m.sidebarWidth, m.width)
	}
}

// A drag to the far left must not produce a zero or negative width: that
// would make the sidebar vanish with no way to grab it back.
func TestSidebarDrag_NeverCollapsesToZero(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(-10)
	m.finishSidebarDrag()

	if m.sidebarWidth < minSidebarWidth {
		t.Errorf("m.sidebarWidth = %d, want at least minSidebarWidth (%d)", m.sidebarWidth, minSidebarWidth)
	}
}

// clearDragState zeroes every mutually-exclusive drag flag in one place, so a
// new drag mode is added by extending the helper rather than auditing each
// click handler. TestModel_ClearDragState pins the invariant; this pins that
// the sidebar drag joined it.
func TestClearDragState_ClearsTheSidebarDrag(t *testing.T) {
	m := Model{width: 200, height: 50, sidebarOpen: true, sidebarWidth: 22}
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)
	m.clearDragState()

	if m.sidebarDragging || m.sidebarDragW != 0 {
		t.Errorf("clearDragState left sidebarDragging=%v sidebarDragW=%d", m.sidebarDragging, m.sidebarDragW)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestSidebarDrag`
Expected: FAIL to compile — `m.beginSidebarDrag undefined`, `minSidebarWidth undefined`.

- [ ] **Step 3: Add the floor constant**

In `internal/tui/sidebar.go`, extend the existing const block (`sidebar.go:13-21`):

```go
	// minSidebarWidth is the narrowest the drag may take the strip. Below
	// this the rows carry no information — minPaneLabelCells alone is 8, and
	// the two-cell markers and state glyph sit beside it — while the edge
	// stays grabbable, so a user who drags too far can drag back. Zero is
	// deliberately unreachable by drag: that is what the toggle is for, and a
	// sidebar dragged to nothing leaves no edge to grab it back by.
	minSidebarWidth = 12
```

- [ ] **Step 4: Add the drag state to Model**

In `internal/tui/model.go`, beside the split-drag fields (~line 544):

```go
	// Project-sidebar edge drag. sidebarDragging is set while a drag is in
	// flight; sidebarDragW is the PENDING width, painted as a preview rule
	// and committed to sidebarWidth only on release.
	//
	// The split is not cosmetic. View() calls tab.Resize on every frame, and
	// ResizeVT's contract pairs every emulator resize with a PTY redraw — so
	// moving the real width per motion event replays the 2026-07-15
	// corruption bug, where unpaired intermediate-width rewraps permanently
	// garble content at the narrowest width crossed. Same deferral, same
	// reason, as finishSplitDrag.
	sidebarDragging bool
	sidebarDragW    int
```

- [ ] **Step 5: Implement the three drag verbs**

Add to `internal/tui/model.go`, next to `finishSplitDrag`:

```go
// beginSidebarDrag arms an edge drag, seeding the pending width from the
// current one so a click with no motion commits no change.
func (m *Model) beginSidebarDrag() {
	m.clearDragState()
	m.sidebarDragging = true
	m.sidebarDragW = m.projectSidebarWidth()
	m.selection = nil
}

// trackSidebarDrag moves the pending width to follow the cursor. Column x
// becomes the sidebar's LAST column, so the width is x+1.
//
// Clamped through sidebarWidth() rather than a second min/max pair: that
// function is the single source of truth for how much screen the strip may
// take, and a private clamp here could land on a width the renderer would
// silently correct — the sidebar would then not stop where the user let go.
func (m *Model) trackSidebarDrag(x int) {
	if !m.sidebarDragging {
		return
	}
	w := x + 1
	if w < minSidebarWidth {
		w = minSidebarWidth
	}
	m.sidebarDragW = sidebarWidth(m.width, m.sidebarOpen, w)
}

// finishSidebarDrag commits the pending width: this is the single point where
// the layout actually moves.
//
// The sequence matches toggleProjectSidebar and is not optional. resizeTabs
// runs FIRST because it is what WRITES pane.Width/Height and tab.CanvasW/H —
// resizeAllPanes only reads and ships them, so without it every background tab
// keeps its pre-drag PTY size. ClearScreen because every column right of the
// strip shifts in one frame, which is the shift Bubble Tea's cell diff
// mis-tracks.
func (m *Model) finishSidebarDrag() tea.Cmd {
	if !m.sidebarDragging {
		return nil
	}
	w := m.sidebarDragW
	m.sidebarDragging = false
	m.sidebarDragW = 0
	if w <= 0 || w == m.sidebarWidth {
		return nil
	}
	m.sidebarWidth = w
	m.cfg.UI.SidebarWidth = w
	m.configChanged = true
	m.resizeTabs()
	return tea.Batch(tea.ClearScreen, m.resizeAllPanes())
}
```

- [ ] **Step 6: Extend clearDragState**

In `clearDragState()` (`model.go:~2325`), alongside the split-drag reset:

```go
	m.sidebarDragging = false
	m.sidebarDragW = 0
```

- [ ] **Step 7: Wire the three mouse branches**

In `MouseClickMsg`, **immediately before** the `projectSidebarSwallowsMouse` branch at `model.go:1110`:

```go
		// Checked BEFORE projectSidebarSwallowsMouse: the zone's left column
		// is the sidebar's own last column, which that branch would otherwise
		// swallow as a row click.
		if m.hitTestSidebarEdge(msg.X, msg.Y) {
			m.beginSidebarDrag()
			return m, nil
		}
```

In `MouseMotionMsg`, alongside the `splitDragNode` check:

```go
		if m.sidebarDragging {
			m.trackSidebarDrag(msg.X)
			return m, nil
		}
```

In `MouseReleaseMsg`, alongside the `splitDragNode` release:

```go
		if m.sidebarDragging {
			return m, m.finishSidebarDrag()
		}
```

- [ ] **Step 8: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run "TestSidebarDrag|TestClearDragState"`
Expected: PASS.

- [ ] **Step 9: Run the full package plus race**

Run: `./scripts/dev.sh test ./internal/tui/` then `./scripts/dev.sh test-race ./internal/tui/`
Expected: PASS. `TestModel_ClearDragState` must still pass — if it enumerates fields, extend it rather than weakening it.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/model.go internal/tui/sidebar.go internal/tui/sidebar_drag_test.go
git commit -F - <<'EOF'
feat(tui): drag the project sidebar's edge to resize it

Press the boundary column and drag; the width commits on release.

The pending width is deliberately kept out of m.sidebarWidth until then.
View() calls tab.Resize every frame and ResizeVT pairs each emulator
resize with a PTY redraw, so following the cursor live would replay the
unpaired-rewrap corruption that split-border drag already defers around.

Clamping goes through sidebarWidth() so the strip stops where the user
let go rather than at a value the renderer silently corrects.
EOF
```

---

### Task 4: Paint the drag preview

**Files:**
- Modify: `internal/tui/model.go` — `View()`, at the sidebar join (~line 3127)
- Test: `internal/tui/sidebar_drag_test.go` (extend)

**Interfaces:**
- Consumes: `Model.sidebarDragging`, `Model.sidebarDragW` (Task 3).
- Produces: nothing.

**Context an implementer cannot infer:** the preview cannot move the sidebar's rendered content (that is the whole point of Task 3's deferral). It draws a **rule** — a full-height vertical line in the split-drag highlight colour, 39, already registered in `renderKey` — at screen column `sidebarDragW - 1`. When the pending width is wider than the current one the rule lands over the pane area; when narrower, over the sidebar itself. Both are correct: the rule shows where the edge *will* be.

Use the same colour as `splitDragHighlight` so the two drags read as one gesture vocabulary.

- [ ] **Step 1: Write the failing test**

```go
// The preview must be a RULE, not a moved sidebar: the rendered strip keeps
// its committed width mid-drag (see TestSidebarDrag_DoesNotResizeMidDrag),
// so the only thing that may move is the indicator.
func TestSidebarDragPreview_DrawsARuleWithoutMovingTheStrip(t *testing.T) {
	m := newTestModelWithSidebar(t, 120, 30)
	m.beginSidebarDrag()
	m.trackSidebarDrag(40)

	out := stripANSI(m.View().String())
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("View produced %d lines, want a full frame", len(lines))
	}
	// Row 1 is inside the sidebar's body. The rule sits at the pending
	// edge's last column, and the strip itself has not moved.
	row := []rune(lines[1])
	if len(row) <= m.sidebarDragW-1 {
		t.Fatalf("row 1 is %d cells, too short to hold the rule at %d", len(row), m.sidebarDragW-1)
	}
	if row[m.sidebarDragW-1] != sidebarDragRule {
		t.Errorf("column %d = %q, want the drag rule %q", m.sidebarDragW-1, row[m.sidebarDragW-1], sidebarDragRule)
	}
}

// With no drag in flight the frame must be byte-identical to the undragged
// one — the preview may not leave a residue.
func TestSidebarDragPreview_AbsentWhenNotDragging(t *testing.T) {
	m := newTestModelWithSidebar(t, 120, 30)
	before := m.View().String()

	m.beginSidebarDrag()
	m.trackSidebarDrag(40)
	m.finishSidebarDrag()
	m.sidebarWidth = 22 // undo the commit; only the preview is under test
	m.cfg.UI.SidebarWidth = 22

	if got := m.View().String(); got != before {
		t.Error("a finished drag left a preview residue in the frame")
	}
}
```

Note: `newTestModelWithSidebar` and `stripANSI` — reuse the package's existing helpers. If no sidebar-bearing constructor exists, add `newTestModelWithSidebar` beside the other test helpers rather than inlining struct literals in both tests.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestSidebarDragPreview`
Expected: FAIL — `sidebarDragRule undefined`.

- [ ] **Step 3: Implement the rule**

In `internal/tui/sidebar.go`:

```go
// sidebarDragRule is the glyph painted down the pending edge while the
// sidebar is being dragged. A box-drawing vertical is used rather than a
// block so it reads as a boundary rather than as content, and it is a single
// cell with no emoji presentation — the same requirement the state glyphs
// carry, for the same reason (an emoji-capable codepoint can be drawn two
// cells wide while advancing one, painting over its neighbour).
const sidebarDragRule = '│'
```

In `internal/tui/model.go`, in `View()` after the sidebar is joined onto the pane area (~line 3127), overlay the rule:

```go
			// Drag preview: a rule at the PENDING edge. The strip itself must
			// not move mid-drag — see the sidebarDragging field comment — so
			// the indicator is the only thing that follows the cursor.
			if m.sidebarDragging && m.sidebarDragW > 0 {
				frame = overlayColumn(frame, m.sidebarDragW-1, m.height-1, sidebarDragRule, splitDragHighlightColor)
			}
```

Add `overlayColumn(s string, col, rows int, glyph rune, color lipgloss.Color) string` next to `overlayRight` in `internal/tui/compose.go`. It must replace exactly one cell per line for the first `rows` lines and leave lines shorter than `col+1` untouched, so a short line cannot be padded into a wider frame — the bug `overlayRight`'s own comment records.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestSidebarDragPreview`
Expected: PASS.

- [ ] **Step 5: Verify the whole package, race, and vet**

Run: `./scripts/dev.sh test ./internal/tui/`, `./scripts/dev.sh test-race ./internal/tui/`, `./scripts/dev.sh vet`
Expected: PASS, no findings.

- [ ] **Step 6: Manual check in dev mode**

```bash
./scripts/dev.sh build
./scripts/quil-dev.ps1
```

Confirm `[dev]` in the status bar, then: drag the sidebar edge right and left, release, confirm panes reflow and no content garbles; reopen and confirm the width persisted; check F1 → Settings shows the new width.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/model.go internal/tui/sidebar.go internal/tui/compose.go internal/tui/sidebar_drag_test.go
git commit -F - <<'EOF'
feat(tui): preview the sidebar edge while dragging it

Draw a rule down the pending edge in the split-drag highlight colour, so
the two drags share one gesture vocabulary. The strip keeps its committed
width until release; only the indicator follows the cursor.
EOF
```

---

### Task 5: Document the width control

**Files:**
- Modify: `docs/configuration.md` (the `[ui]` section), `docs/keybindings.md` (mouse section), `.claude/rules/projects.md` (sidebar section)
- Test: none — documentation.

- [ ] **Step 1: Document `ui.sidebar_width` in `docs/configuration.md`**

Record the default (22), that Settings edits it live, that dragging the edge writes it, the drag floor (`minSidebarWidth` = 12), and that the value is clamped so `minTermWidth` columns always remain for panes.

- [ ] **Step 2: Add the drag to `docs/keybindings.md`**

Beside the split-border drag entry, one line: drag the project sidebar's right edge to resize it; the width persists.

- [ ] **Step 3: Add the invariant to `.claude/rules/projects.md`**

Append to the Sidebar section:

> **The sidebar edge drag must never move `m.sidebarWidth` mid-drag.** `View()` calls `tab.Resize(m.paneAreaWidth()…)` every frame and `ResizeVT` pairs every emulator resize with a PTY redraw, so a live-following strip replays the 2026-07-15 unpaired-rewrap corruption once per motion event. `sidebarDragW` holds the pending value, a rule is painted at it, and `finishSidebarDrag` is the single commit point — the same deferral `finishSplitDrag` makes. Clamping goes through `sidebarWidth()` and nowhere else, so the edge stops where the user let go rather than at a value the renderer silently corrects.

- [ ] **Step 4: Check the docs-size gate**

Run: `./scripts/dev.sh docs-size`
Expected: PASS — `.claude/CLAUDE.md` under 100,000 bytes, each rule file under 140,000.

- [ ] **Step 5: Commit**

```bash
git add docs/configuration.md docs/keybindings.md .claude/rules/projects.md
git commit -F - <<'EOF'
docs: describe the sidebar width control

Cover the config key, the Settings row, the edge drag, and the invariant
that the drag defers its resize to release.
EOF
```

---

## Self-Review

**Spec coverage.** Two requests: the width as a Quil option (Task 1 — Settings row over the config key that already existed) and interactive resize (Tasks 2–4). Both covered. The original worry about finding every pane-width call site is answered by `projectSidebarWidth()` being the single accessor — no sweep task is needed, and none is included.

**Placeholder scan.** Every code step carries real code. The one deliberate deferral is `overlayColumn`'s body in Task 4 Step 3, specified by contract (replace one cell per line, leave short lines untouched, never pad) because its exact form depends on `compose.go`'s existing helpers, which the implementer will have open.

**Type consistency.** `hitTestSidebarEdge` returns `bool` in Task 2 and is consumed as `bool` in Task 3. `finishSidebarDrag` returns `tea.Cmd` in Task 3 and is returned as a `tea.Cmd` from `MouseReleaseMsg`. `sidebarDragW` is written by `trackSidebarDrag`, read by `finishSidebarDrag` and by `View()`. `minSidebarWidth` is introduced in Task 3 Step 3 and referenced by the Task 3 Step 1 test — the test is written first and fails on the missing constant, which is the intended red.
