package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// A client launched with no console attached — `quil.exe --version` from a
// non-interactive shell is the observed case — gets a 1x1 geometry from Bubble
// Tea. Every resize fan-out computed from it floors at 1x1 (paneVTSize does so
// deliberately, for genuinely narrow SPLIT panes), so the daemon reflowed every
// PTY in the workspace to one column and each child permanently re-wrapped its
// whole transcript. Observed in ~/.quil/quild.log as
// `attach: client connected (1x1), tabs=48, restored=true` on 2026-08-25 and
// again on 2026-09-05.
//
// View() already refuses to paint below minTermWidth x minTermHeight, so a
// geometry under that describes nothing on screen and must not reach a PTY.
//
// These tests drive Model.Update rather than the fan-out functions directly:
// the bug is that the CALL SITES invoke them unconditionally, so a test that
// calls resizeAllPanes/diffResizes/overlayResizeCmd itself would stay green
// against a fix the call site makes unreachable.

// tinyTermModel builds a one-project / one-tab / one-pane Model wired to a
// fakeConn, with no size reported yet.
func tinyTermModel(t *testing.T) (Model, *fakeConn) {
	t.Helper()
	pane := NewPaneModel("pane-1", testRingBufSize)
	t.Cleanup(pane.Dispose)
	tab := NewTabModel("tab-1", "Shell")
	tab.Root = NewLeaf(pane)
	tab.ActivePane = "pane-1"
	conn := newFakeConn()
	m := Model{
		cfg:            config.Default(),
		client:         conn,
		tabDragFromIdx: -1,
		projects: []*ProjectModel{{
			ID: "proj-1", Name: "Default", tabs: []*TabModel{tab},
		}},
	}
	return m, conn
}

// countResizes reports how many MsgResizePane frames reached the wire.
func countResizes(t *testing.T, conn *fakeConn) int {
	t.Helper()
	n := 0
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type == ipc.MsgResizePane {
			n++
		}
	}
	return n
}

func clearSent(conn *fakeConn) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.sent = nil
}

// The first WindowSizeMsg applies immediately (no debounce), so a degenerate
// launch geometry reaches every pane in one pass.
func TestUpdate_FirstWindowSizeBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — a 1x1 terminal is below "+
			"minTermWidth(%d)xminTermHeight(%d) and View() refuses to paint it, "+
			"so no PTY may be resized to it", n, minTermWidth, minTermHeight)
	}
}

// The guard must be a floor, not a ban: the smallest terminal the TUI agrees to
// paint still has to size its panes.
func TestUpdate_FirstWindowSizeAtMinimum_ShipsPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: minTermWidth, Height: minTermHeight})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 1 {
		t.Fatalf("MsgResizePane count = %d, want 1 — %dx%d is exactly the "+
			"minimum the TUI paints, so its panes must still be sized",
			n, minTermWidth, minTermHeight)
	}
}

// The debounced arm is the second fan-out: a terminal that SHRINKS below the
// minimum after a healthy start reaches the panes through resizeTickMsg, and an
// open overlay is resized from there too.
func TestUpdate_ResizeTickBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	// A healthy first size, so the model is sized and later resizes debounce.
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 172, Height: 48})
	runCmd(cmd)
	m = next.(Model)
	if countResizes(t, conn) == 0 {
		t.Fatal("setup is wrong: a healthy first resize shipped nothing")
	}

	overlay := NewPaneModel("overlay-1", testRingBufSize)
	t.Cleanup(overlay.Dispose)
	tab := m.projects[0].tabs[0]
	tab.overlayPane = overlay
	tab.overlayVisible = true
	clearSent(conn)

	// Shrink. The tea.Tick is deliberately not run — the arm under test is the
	// resizeTickMsg one, delivered here directly through Update.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	m = next.(Model)
	_, tickCmd := m.Update(resizeTickMsg{seq: m.resizeSeq})
	runCmd(tickCmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — the debounced arm must "+
			"honour the same minimum as the first resize, for tree panes and "+
			"for the overlay pane", n)
	}
}

// The third fan-out: every daemon broadcast diffs the pane sizes and pushes
// what disagrees. At 1x1 that is every pane in the workspace, on every
// broadcast, which is what reached 48 tabs.
func TestUpdate_WorkspaceStateBelowMinimum_ShipsNoPaneResize(t *testing.T) {
	m, conn := tinyTermModel(t)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 1, Height: 1})
	runCmd(cmd)
	m = next.(Model)
	clearSent(conn)

	// Receive() must not park the listen command the broadcast arm re-arms.
	close(conn.recv)

	_, cmd = m.Update(WorkspaceStateMsg{
		Dest:          "",
		ActiveProject: "proj-1",
		ActiveTab:     "tab-1",
		Projects:      []ProjectInfo{{ID: "proj-1", Name: "Default", TabIDs: []string{"tab-1"}}},
		Tabs:          []TabInfo{{ID: "tab-1", Name: "Shell", ProjectID: "proj-1", Panes: []string{"pane-1"}}},
		Panes:         []PaneInfo{{ID: "pane-1", TabID: "tab-1", Type: "terminal"}},
	})
	runCmd(cmd)

	if n := countResizes(t, conn); n != 0 {
		t.Fatalf("MsgResizePane count = %d, want 0 — a broadcast must not push "+
			"pane sizes derived from a terminal the TUI refuses to paint", n)
	}
}
