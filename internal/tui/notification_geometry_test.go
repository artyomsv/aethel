package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

// liveLoc reports every pane as alive, in project "proj", tab "tab".
func liveLoc(paneID string) (string, bool) { return "proj · tab", true }

// deadLoc reports every pane as gone.
func deadLoc(paneID string) (string, bool) { return "", false }

func geomEvent(id, title, message string) ipc.PaneEventPayload {
	return ipc.PaneEventPayload{
		ID: id, PaneID: "pane-" + id, PaneName: "shell",
		Type: "process_exit", Title: title, Message: message,
		Severity: "info", Timestamp: 0,
	}
}

// linesText extracts the text of each rendered line.
func linesText(lines []renderedLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
	}
	return out
}

func renderedText(lines []renderedLine) string {
	return ansi.Strip(strings.Join(linesText(lines), "\n"))
}

// A card with no Message is one line shorter than one with it. This is the
// whole reason the viewport is indexed in LINES rather than cards.
func TestNotificationLines_VariableCardHeight(t *testing.T) {
	withExcerpt := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "the last output line")},
		28, 0, false, liveLoc,
	)
	without := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		28, 0, false, liveLoc,
	)
	if len(withExcerpt) != len(without)+1 {
		t.Errorf("card heights: with excerpt %d lines, without %d; want exactly one more",
			len(withExcerpt), len(without))
	}
}

// Every line names the event it belongs to, or -1 for chrome. This is what the
// mouse hit test reads.
func TestNotificationLines_EventIndexPerLine(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{
			geomEvent("TEST-1", "first", "excerpt one"),
			geomEvent("TEST-2", "second", ""),
		},
		28, 0, false, liveLoc,
	)

	seen := map[int]int{}
	for _, l := range lines {
		seen[l.eventIdx]++
	}
	if seen[0] != 4 {
		t.Errorf("event 0 lines: got %d, want 4 (name, title, location, excerpt)", seen[0])
	}
	if seen[1] != 3 {
		t.Errorf("event 1 lines: got %d, want 3 (name, title, location)", seen[1])
	}
	if seen[-1] < 2 {
		t.Errorf("separator lines: got %d, want at least 2 (one per card plus the tail)", seen[-1])
	}
}

// The location line names project and tab, so a click's destination is visible
// before the click.
func TestNotificationLines_ShowsLocation(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		40, 0, false, liveLoc,
	)
	if joined := renderedText(lines); !strings.Contains(joined, "proj · tab") {
		t.Errorf("rendered card has no location line:\n%s", joined)
	}
}

// A card whose pane is gone must not advertise a jump it cannot perform.
func TestNotificationLines_DeadPaneIsMarked(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Pane closed", "")},
		40, 0, false, deadLoc,
	)
	if joined := renderedText(lines); !strings.Contains(joined, "(closed)") {
		t.Errorf("dead pane card has no (closed) marker:\n%s", joined)
	}
}

// A nil locator is what a test that does not care passes. It must render, and
// must not claim every pane is closed.
func TestNotificationLines_NilLocatorRenders(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{geomEvent("TEST-1", "Process exited", "")},
		40, 0, false, nil,
	)
	joined := renderedText(lines)
	if !strings.Contains(joined, "Process exited") {
		t.Errorf("nil locator lost the card:\n%s", joined)
	}
	if strings.Contains(joined, "(closed)") {
		t.Errorf("nil locator marked a live pane closed:\n%s", joined)
	}
}

func TestScrollBy_ClampsAtBothEnds(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.ScrollBy(-5, height)
	if nc.scroll != 0 {
		t.Errorf("scroll after scrolling up from the top: got %d, want 0", nc.scroll)
	}

	nc.ScrollBy(10000, height)
	total := len(notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil))
	want := total - notifyViewportHeight(height)
	if want < 0 {
		want = 0
	}
	if nc.scroll != want {
		t.Errorf("scroll after scrolling past the end: got %d, want %d", nc.scroll, want)
	}
}

// Reading history is impossible on a busy workspace if every arriving event
// yanks the viewport back to the top.
func TestAddEvent_DoesNotResetScroll(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	nc.ScrollBy(6, 20)
	before := nc.scroll
	if before == 0 {
		t.Fatal("fixture did not scroll; the test cannot detect a reset")
	}

	nc.AddEvent(geomEvent("TEST-new", "arriving", "excerpt"))
	if nc.scroll != before {
		t.Errorf("scroll after a new event: got %d, want %d (unchanged)", nc.scroll, before)
	}
}

func TestEventIndexAtRow(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(geomEvent("TEST-1", "first", "excerpt"))
	nc.AddEvent(geomEvent("TEST-2", "second", "excerpt"))
	const height = 20

	// Row 0 is the tab bar, 1 the box top border, 2 the title — all chrome.
	for _, y := range []int{0, 1, 2} {
		if got := nc.eventIndexAtRow(y, height, liveLoc); got != -1 {
			t.Errorf("eventIndexAtRow(%d) = %d, want -1 (chrome)", y, got)
		}
	}
	// Viewport line 0 (screen row 3) is the newest card's separator; line 1
	// (screen row 4) is its name line.
	if got := nc.eventIndexAtRow(3, height, liveLoc); got != -1 {
		t.Errorf("eventIndexAtRow(3) = %d, want -1 (separator)", got)
	}
	if got := nc.eventIndexAtRow(4, height, liveLoc); got != 0 {
		t.Errorf("eventIndexAtRow(4) = %d, want 0 (newest card's name line)", got)
	}
	// Far below the viewport.
	if got := nc.eventIndexAtRow(height+5, height, liveLoc); got != -1 {
		t.Errorf("eventIndexAtRow past the viewport = %d, want -1", got)
	}
}

// The hit test must follow the scroll, or a click lands on the card that was
// there before the user scrolled.
func TestEventIndexAtRow_FollowsScroll(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 10; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	atTop := nc.eventIndexAtRow(4, height, liveLoc)
	nc.ScrollBy(5, height)
	afterScroll := nc.eventIndexAtRow(4, height, liveLoc)

	if atTop == afterScroll {
		t.Errorf("eventIndexAtRow(4) is %d both before and after scrolling; the hit test ignores scroll", atTop)
	}
}

// SelectIndex must bring an off-screen card into view.
func TestSelectIndex_RevealsCursor(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(geomEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.SelectIndex(15, height)

	if nc.cursor != 15 {
		t.Fatalf("cursor: got %d, want 15", nc.cursor)
	}
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil)
	last := -1
	for i, l := range lines {
		if l.eventIdx == 15 {
			last = i
		}
	}
	if last < nc.scroll || last >= nc.scroll+notifyViewportHeight(height) {
		t.Errorf("selected card's last line %d is outside the viewport [%d, %d)",
			last, nc.scroll, nc.scroll+notifyViewportHeight(height))
	}
}

// The empty state must say the filter is why, when a filter is installed.
func TestView_EmptyStateMentionsFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle"})

	out := ansi.Strip(nc.View(20, nil))
	if !strings.Contains(out, "show all") {
		t.Errorf("filtered empty state does not mention the override:\n%s", out)
	}
}
