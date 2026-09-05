package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/charmbracelet/x/ansi"
)

// Named keys travel in Code, printable ones in Text — that is how the rest of
// this package's dialog tests build them (dialog_test.go:112, :138, :213), and
// it is what msg.String() reads. "enter" in Text does NOT produce
// String() == "enter".
var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
)

// newModelForDialogTest builds a Model carrying the shipped defaults, so these
// tests exercise the same config a user gets.
//
// A struct literal rather than NewModel, matching this package's other Model
// fixtures: NewModel takes six arguments and reads QUIL_HOME, and none of that
// affects a dialog's key handling.
func newModelForDialogTest(t *testing.T) Model {
	t.Helper()
	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         120,
		height:        40,
		notifications: NewNotificationCenter(30, 50),
	}
	m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
	return m
}

// notifyRowIndex finds a row by label so a test does not hard-code an index
// that a later row insertion would silently invalidate.
func notifyRowIndex(t *testing.T, label string) int {
	t.Helper()
	for i, r := range notifySettingsRows() {
		if r.label == label && !r.heading {
			return i
		}
	}
	t.Fatalf("no notify settings toggle row labelled %q", label)
	return -1
}

func TestNotifySettingsRows_CoverEveryToastAndGroup(t *testing.T) {
	want := []string{
		"Enabled", "On blocked", "On done",
		"Agent turns", "Agent blocked", "Agent subagents", "Agent session",
		"Process", "Pane", "MCP", "System", "Commands", "Idle",
	}
	var got []string
	for _, r := range notifySettingsRows() {
		if !r.heading {
			got = append(got, r.label)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("toggle rows: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNotifySettings_ToggleAppliesLiveAndMarksConfigChanged(t *testing.T) {
	m := newModelForDialogTest(t)
	m.dialog = dialogNotifySettings
	m.dialogCursor = notifyRowIndex(t, "Idle")
	if m.cfg.Notification.Events.Idle {
		t.Fatal("fixture: idle is already on")
	}

	next, _ := m.Update(keyEnter)
	nm := next.(Model)

	if !nm.cfg.Notification.Events.Idle {
		t.Error("idle not enabled after Enter")
	}
	if !nm.configChanged {
		t.Error("configChanged not set; the edit would be lost on exit")
	}
	if !nm.notifications.groups.shows("output_idle") {
		t.Error("filter not re-applied live; the sidebar still hides idle events")
	}
}

func TestNotifySettings_ToggleDesktopToast(t *testing.T) {
	m := newModelForDialogTest(t)
	m.dialog = dialogNotifySettings
	m.dialogCursor = notifyRowIndex(t, "On blocked")
	if !m.cfg.Notification.Desktop.Blocked {
		t.Fatal("fixture: blocked toasts already off")
	}

	next, _ := m.Update(keyEnter)
	nm := next.(Model)

	if nm.cfg.Notification.Desktop.Blocked {
		t.Error("blocked toasts still on after Enter")
	}
	if !nm.configChanged {
		t.Error("configChanged not set")
	}
}

func TestNotifySettings_EscReturnsToSettings(t *testing.T) {
	m := newModelForDialogTest(t)
	m.dialog = dialogNotifySettings

	next, _ := m.Update(keyEsc)
	if got := next.(Model).dialog; got != dialogSettings {
		t.Errorf("dialog after Esc: got %v, want dialogSettings", got)
	}
}

// Headings are inert. The cursor must step over them in both directions, so
// Enter can never land on one.
func TestNotifySettings_CursorSkipsHeadings(t *testing.T) {
	rows := notifySettingsRows()
	m := newModelForDialogTest(t)
	m.dialog = dialogNotifySettings
	m.dialogCursor = notifyRowIndex(t, "On done") // last row before the second heading

	next, _ := m.Update(keyDown)
	nm := next.(Model)
	if rows[nm.dialogCursor].heading {
		t.Errorf("cursor landed on heading %q after Down", rows[nm.dialogCursor].label)
	}
	if rows[nm.dialogCursor].label != "Agent turns" {
		t.Errorf("cursor after Down: got %q, want %q", rows[nm.dialogCursor].label, "Agent turns")
	}

	back, _ := nm.Update(keyUp)
	bm := back.(Model)
	if rows[bm.dialogCursor].heading {
		t.Errorf("cursor landed on heading %q after Up", rows[bm.dialogCursor].label)
	}
	if rows[bm.dialogCursor].label != "On done" {
		t.Errorf("cursor after Up: got %q, want %q", rows[bm.dialogCursor].label, "On done")
	}
}

// The screen opens with the cursor on a toggle, never on a heading.
func TestSettings_NotificationsRowOpensSubmenu(t *testing.T) {
	m := newModelForDialogTest(t)
	m.dialog = dialogSettings
	fields := settingsFields()
	found := -1
	for i, f := range fields {
		if f.submenu && f.label == "Notifications" {
			found = i
		}
	}
	if found < 0 {
		t.Fatal(`no submenu row labelled "Notifications" in settingsFields()`)
	}
	m.dialogCursor = found

	next, _ := m.Update(keyEnter)
	nm := next.(Model)
	if nm.dialog != dialogNotifySettings {
		t.Fatalf("dialog after Enter on the submenu row: got %v, want dialogNotifySettings", nm.dialog)
	}
	rows := notifySettingsRows()
	if nm.dialogCursor < 0 || nm.dialogCursor >= len(rows) || rows[nm.dialogCursor].heading {
		t.Errorf("opening cursor at %d lands on a heading or out of range", nm.dialogCursor)
	}
}

// The old top-level row is gone; its toggle lives in the new screen.
func TestSettings_DesktopNotificationsRowRemoved(t *testing.T) {
	for _, f := range settingsFields() {
		if f.label == "Desktop notifications" {
			t.Error(`"Desktop notifications" is still a top-level Settings row`)
		}
	}
}

func TestRenderNotifySettingsDialog_ShowsBothSections(t *testing.T) {
	m := newModelForDialogTest(t)
	out := ansi.Strip(m.renderNotifySettingsDialog())
	for _, want := range []string{
		"Desktop toasts", "Sidebar events", "Agent turns", "Idle",
		"Hidden events still reach MCP agents",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered dialog is missing %q:\n%s", want, out)
		}
	}
}
