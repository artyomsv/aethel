package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// mouseTestModel builds a Model with a visible, sized notification sidebar and
// a handful of events.
//
// It drives everything through Update, never by calling a handler directly: a
// direct-call test can pass against code the call site makes unreachable.
//
// A struct literal rather than NewModel, matching this package's other Model
// fixtures — NewModel takes six arguments and reads QUIL_HOME, and none of that
// affects mouse routing. newFakeConn (router_test.go) satisfies the client
// interface, which the dismiss path needs: it calls m.client.Send unguarded.
func mouseTestModel(t *testing.T) Model {
	t.Helper()
	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         120,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
	}
	m.notifications.visible = true
	m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
	for i := 0; i < 8; i++ {
		suffix := string(rune('a' + i))
		m.notifications.AddEvent(ipc.PaneEventPayload{
			ID:       "TEST-" + suffix,
			PaneID:   "pane-test-" + suffix,
			PaneName: "shell",
			Type:     "process_exit",
			Title:    "Process exited",
			Message:  "an excerpt line",
			Severity: "info",
		})
	}
	return m
}

// sidebarX is a column inside the sidebar overlay.
func sidebarX(m Model) int { return m.width - 2 }

func TestWheelOverSidebar_Scrolls(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.scroll

	next, _ := m.Update(tea.MouseWheelMsg{X: sidebarX(m), Y: 6, Button: tea.MouseWheelDown})
	got := next.(Model).notifications.scroll
	if got <= before {
		t.Errorf("scroll after a wheel-down over the sidebar: got %d, want > %d", got, before)
	}
}

// One column left of the strip belongs to the pane area, so the sidebar must
// not claim it.
func TestWheelOutsideSidebar_DoesNotScrollIt(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.scroll

	next, _ := m.Update(tea.MouseWheelMsg{
		X: m.width - m.notifications.width - 1, Y: 6, Button: tea.MouseWheelDown,
	})
	if got := next.(Model).notifications.scroll; got != before {
		t.Errorf("scroll after a wheel notch outside the strip: got %d, want %d", got, before)
	}
}

// A horizontal notch (trackpad, shift-scroll) aimed at the strip must be
// swallowed, not fall through to the pane beneath.
func TestHorizontalWheelOverSidebar_IsSwallowed(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.scroll

	next, _ := m.Update(tea.MouseWheelMsg{X: sidebarX(m), Y: 6, Button: tea.MouseWheelLeft})
	if got := next.(Model).notifications.scroll; got != before {
		t.Errorf("scroll after a horizontal notch: got %d, want %d (unchanged)", got, before)
	}
}

func TestClickOnCard_SelectsAndFocuses(t *testing.T) {
	m := mouseTestModel(t)
	m.notifications.cursor = 3

	// Screen row 4 = viewport line 1 = the newest card's name line.
	next, _ := m.Update(tea.MouseClickMsg{X: sidebarX(m), Y: 4, Button: tea.MouseLeft})
	nm := next.(Model)

	if !nm.sidebarFocused {
		t.Error("sidebar not focused after a click on a card")
	}
	if nm.notifications.cursor != 0 {
		t.Errorf("cursor: got %d, want 0 (the card under the pointer)", nm.notifications.cursor)
	}
}

func TestClickOnChrome_FocusesWithoutSelecting(t *testing.T) {
	m := mouseTestModel(t)
	m.notifications.cursor = 3

	// Screen row 2 is the " Notifications " title — chrome.
	next, _ := m.Update(tea.MouseClickMsg{X: sidebarX(m), Y: 2, Button: tea.MouseLeft})
	nm := next.(Model)

	if !nm.sidebarFocused {
		t.Error("sidebar not focused after a click on chrome")
	}
	if nm.notifications.cursor != 3 {
		t.Errorf("cursor moved on a chrome click: got %d, want 3", nm.notifications.cursor)
	}
}

func TestRightClickOnCard_Dismisses(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.Count()

	next, _ := m.Update(tea.MouseClickMsg{X: sidebarX(m), Y: 4, Button: tea.MouseRight})
	if got := next.(Model).notifications.Count(); got != before-1 {
		t.Errorf("Count after a right-click on a card: got %d, want %d", got, before-1)
	}
}

// A right-click on chrome selects nothing, so it must dismiss nothing.
func TestRightClickOnChrome_DismissesNothing(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.Count()

	next, _ := m.Update(tea.MouseClickMsg{X: sidebarX(m), Y: 2, Button: tea.MouseRight})
	if got := next.(Model).notifications.Count(); got != before {
		t.Errorf("Count after a right-click on chrome: got %d, want %d", got, before)
	}
}

// A click on a card whose pane no longer exists must not navigate, and must
// not push navigation history for a jump that never happened.
func TestClickOnCard_DeadPaneDoesNotNavigate(t *testing.T) {
	m := mouseTestModel(t)
	// No projects at all, so findPaneAndTab resolves nothing.
	m.projects = nil
	before := len(m.paneHistory)

	next, _ := m.Update(tea.MouseClickMsg{X: sidebarX(m), Y: 4, Button: tea.MouseLeft})
	nm := next.(Model)

	if len(nm.paneHistory) != before {
		t.Errorf("paneHistory grew for a jump that could not land: got %d, want %d",
			len(nm.paneHistory), before)
	}
	if nm.notifications.cursor != 0 {
		t.Errorf("cursor: got %d, want 0 (selection still moves)", nm.notifications.cursor)
	}
}
