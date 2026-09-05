package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// NotificationCenter manages the notification sidebar state.
//
// It stores EVERY event it is given and filters at READ time. Storing the
// filtered set instead would make turning a group back on silently useless —
// the events that arrived while it was off would already be gone — and the
// filter is a display preference, not a subscription.
type NotificationCenter struct {
	events []ipc.PaneEventPayload
	// cursor indexes visibleEvents(), NOT events. Every read path resolves
	// through the same accessor so none of them can disagree about which
	// event position N is.
	cursor int
	// scroll is the first visible LINE of the card viewport. Lines, not
	// cards: cards vary in height, so a card-indexed window cannot show a
	// partially-scrolled one.
	scroll    int
	visible   bool
	focused   bool
	width     int
	maxEvents int
	// groups is the display filter. NIL shows everything, which is what a
	// Model built directly by a test gets.
	groups eventGroupFilter
	// showAll is the sidebar's 'a' override: display every stored event
	// regardless of the configured groups. Session-only and never persisted —
	// a debugging affordance, not a setting.
	showAll bool
}

// NewNotificationCenter creates a notification center with the given sidebar width and max events.
func NewNotificationCenter(width, maxEvents int) *NotificationCenter {
	if width <= 0 {
		width = 30
	}
	if maxEvents <= 0 {
		maxEvents = 50
	}
	return &NotificationCenter{width: width, maxEvents: maxEvents}
}

// SetGroups installs the display filter. Called at construction and again
// whenever the user toggles a group in F1 -> Settings -> Notifications, so the
// change applies live — a visible control that did nothing until relaunch reads
// as a broken dialog, the same rule the Sidebar width row states.
//
// The cursor is clamped rather than reset: hiding a group must not throw away
// a selection that is still visible.
func (nc *NotificationCenter) SetGroups(f eventGroupFilter) {
	nc.groups = f
	nc.clampCursor()
}

// visibleEvents returns the events the configured groups allow, newest first.
//
// EVERY read path resolves through this — cursor, selection, dismissal, the
// status-bar badge and the renderer — so none of them can disagree about which
// event position N is.
func (nc *NotificationCenter) visibleEvents() []ipc.PaneEventPayload {
	if nc.showAll || nc.groups == nil {
		return nc.events
	}
	out := make([]ipc.PaneEventPayload, 0, len(nc.events))
	for _, e := range nc.events {
		if nc.groups.shows(e.Type) {
			out = append(out, e)
		}
	}
	return out
}

// clampCursor keeps the cursor inside the visible list after anything that can
// shrink it: a filter change, a dismissal, an eviction at maxEvents.
func (nc *NotificationCenter) clampCursor() {
	if n := len(nc.visibleEvents()); nc.cursor >= n {
		nc.cursor = n - 1
	}
	if nc.cursor < 0 {
		nc.cursor = 0
	}
}

// AddEvent prepends an event. When an event with the same ID is already
// queued, the entry is updated in place AND moved to the front — this is
// the echo of the daemon's eventQueue.Push aggregation, where a repeat
// (PaneID, Title) event reuses the prior event's ID and bumps Data["count"].
// Without the move-to-front the sidebar would silently drop bumps and the
// user would never see the ×N count grow.
//
// Cursor invariant: the cursor follows the LOGICAL event the user is on,
// not the index. If the move-to-front shifts other events past the cursor's
// position, we rewrite cursor to point at the event with the same ID it had
// before — so a user staring at "claude-code (×3)" does not silently jump to
// a different card when "claude-code (×4)" arrives.
func (nc *NotificationCenter) AddEvent(e ipc.PaneEventPayload) {
	for i, existing := range nc.events {
		if existing.ID != e.ID {
			continue
		}
		// Capture the cursor's current event ID so we can chase it through
		// the move-to-front. The aggregated event itself is allowed to move
		// — what we protect is selection of OTHER events.
		//
		// Read from and written back to the VISIBLE list, because that is what
		// the cursor indexes. Using the stored slice on either side was
		// correct only while the two lists were the same one, and would now
		// silently jump the selection to an unrelated card whenever anything
		// is hidden.
		var cursorID string
		if vis := nc.visibleEvents(); nc.cursor >= 0 && nc.cursor < len(vis) {
			cursorID = vis[nc.cursor].ID
		}

		nc.events = append(nc.events[:i], nc.events[i+1:]...)
		nc.events = append([]ipc.PaneEventPayload{e}, nc.events...)

		// Restore cursor onto the same logical event. When the aggregated
		// event WAS the cursor, follow it to its new position (the visual
		// equivalent of "stay on the card you were looking at"). When the
		// cursor was on a different event, find its new index.
		if cursorID != "" {
			for j, ev := range nc.visibleEvents() {
				if ev.ID == cursorID {
					nc.cursor = j
					break
				}
			}
		}
		nc.clampCursor()
		return
	}
	nc.events = append([]ipc.PaneEventPayload{e}, nc.events...)
	if len(nc.events) > nc.maxEvents {
		nc.events = nc.events[:nc.maxEvents]
	}
	// The eviction above can drop the event the cursor was on, and with a
	// filter installed the visible list can shrink by more than one.
	nc.clampCursor()
	// Deliberately do NOT shift the cursor on a fresh prepend. The legacy
	// contract is "cursor 0 = newest event"; a fresh event landing at index
	// 0 should become the new selection by default. Only the aggregation
	// move-to-front above chases the logical event by ID, because that's the
	// case where the user is actively reading a card that's about to bump.
}

// DismissSelected removes the selected event and returns its ID.
//
// It resolves the cursor through visibleEvents() and then deletes BY ID from
// the stored slice. Slicing nc.events by the cursor directly — which is what
// this did before the filter existed — dismisses a different event than the one
// under the cursor as soon as anything is hidden.
func (nc *NotificationCenter) DismissSelected() string {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return ""
	}
	id := vis[nc.cursor].ID
	for i, e := range nc.events {
		if e.ID == id {
			nc.events = append(nc.events[:i], nc.events[i+1:]...)
			break
		}
	}
	nc.clampCursor()
	return id
}

// DismissAll removes all events.
//
// Every stored event, not just the visible ones: the key is documented as
// "dismiss all", the daemon-side MsgDismissEvent with an empty ID clears the
// whole queue, and leaving hidden events behind would make the client and the
// daemon disagree about what is still pending.
func (nc *NotificationCenter) DismissAll() {
	nc.events = nil
	nc.cursor = 0
	nc.scroll = 0
}

// SelectedEvent returns the currently selected event, or nil.
func (nc *NotificationCenter) SelectedEvent() *ipc.PaneEventPayload {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return nil
	}
	return &vis[nc.cursor]
}

// Count returns the number of events the user can currently see.
//
// The status-bar badge reads this, so a workspace holding nothing but hidden
// telemetry shows no badge — which is the point of the filter. Safe for the
// existing callers: all of them build a center or a Model without ever calling
// SetGroups, and a nil filter shows everything.
func (nc *NotificationCenter) Count() int {
	return len(nc.visibleEvents())
}

// HandleKey processes a key press when the sidebar is focused.
// Returns: action ("navigate", "dismiss", "dismiss_all", "unfocus", "none"),
// eventID (for dismiss), paneID (for navigate).
func (nc *NotificationCenter) HandleKey(key string) (action, eventID, paneID string) {
	switch key {
	case "up", "k":
		if nc.cursor > 0 {
			nc.cursor--
		}
		return "none", "", ""
	case "down", "j":
		if nc.cursor < len(nc.visibleEvents())-1 {
			nc.cursor++
		}
		return "none", "", ""
	case "a":
		// Reveal every stored event regardless of the configured groups, for
		// as long as the user wants it. Not persisted: F1 -> Settings ->
		// Notifications is where a lasting choice is made, and this is the
		// affordance for looking at what the filter is currently hiding.
		nc.showAll = !nc.showAll
		nc.clampCursor()
		return "none", "", ""
	case "enter":
		if e := nc.SelectedEvent(); e != nil {
			return "navigate", e.ID, e.PaneID
		}
		return "none", "", ""
	case "d":
		id := nc.DismissSelected()
		return "dismiss", id, ""
	case "D":
		nc.DismissAll()
		return "dismiss_all", "", ""
	case "esc", "escape":
		return "unfocus", "", ""
	default:
		return "none", "", ""
	}
}

// notifyViewportOffset is the screen row of the card viewport's first line.
//
//	row 0   tab bar (never the sidebar)
//	row 1   box top border
//	row 2   " Notifications " title
//	row 3   card viewport line 0   <- nc.scroll indexes from here
//
// The arithmetic: the sidebar box is composited onto tabContent by
// overlayRight, which aligns overlay line i with tabContent line i, and
// tabContent is joined BELOW the one-row tab bar. So box line 0 (its top
// border) lands on screen row 1, and the two interior rows above the viewport
// push its first line to row 3.
//
// Named rather than spelled 3 at each site: the renderer and the mouse hit test
// must agree about it, and a literal in two places is how they drift.
const notifyViewportOffset = 3

// paneLocator answers, for one pane id, where the pane lives and whether it
// still exists. Supplied by the Model, which owns the project/tab tree; the
// NotificationCenter deliberately does not reach into it.
//
// The two answers travel together because one lookup produces both, and
// because a card whose pane is gone must be rendered differently AND must not
// offer a jump — the same fact drives both decisions.
//
// A nil locator means "do not know": every pane reads as alive with no label,
// which is what a test that does not care about location wants.
type paneLocator func(paneID string) (label string, alive bool)

// renderedLine is one screen line of the card viewport, tagged with the index
// (into the VISIBLE event list) of the card it belongs to. eventIdx is -1 for
// chrome — the separators between cards.
type renderedLine struct {
	text     string
	eventIdx int
}

// notificationLines renders the card viewport to a flat line list.
//
// It is the SINGLE source of truth for sidebar geometry: View slices its output
// by nc.scroll, and eventIndexAtRow looks up by index. Two independent answers
// to "which card owns screen row N" is how a click lands on the wrong card the
// first time a card changes height.
//
// Pure — no NotificationCenter receiver, no Model — so its tests assert against
// fixed expected output rather than against the other caller. A test that
// compares two callers of a shared helper is a self-comparison and stays green
// on broken geometry.
func notificationLines(events []ipc.PaneEventPayload, innerW, cursor int, focused bool, loc paneLocator) []renderedLine {
	if innerW < 5 {
		return nil
	}
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	separator := sepStyle.Render(truncateRunes(strings.Repeat("·", innerW), innerW))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	var out []renderedLine
	for i, e := range events {
		selected := i == cursor && focused

		label, alive := "", true
		if loc != nil {
			label, alive = loc(e.PaneID)
		}

		out = append(out, renderedLine{text: separator, eventIdx: -1})

		// Line 1: pane name (severity-coloured, or grey when the pane is gone)
		// + right-aligned relative age.
		name := e.PaneName
		if name == "" {
			name = e.PaneID
			if len(name) > 12 {
				name = name[:12]
			}
		}
		name = truncateRunes(sanitizeRemoteText(name), innerW)
		nameStyle := severityNameStyle(e.Severity)
		if !alive {
			// A card that cannot be jumped to must not wear an urgency colour.
			nameStyle = dim
		}
		if selected {
			nameStyle = nameStyle.Bold(true).Reverse(true)
		}
		age := relativeTime(time.UnixMilli(e.Timestamp))
		gap := innerW - len([]rune(name)) - len([]rune(age))
		if gap < 1 {
			gap = 1
		}
		out = append(out, renderedLine{
			text:     nameStyle.Render(name) + strings.Repeat(" ", gap) + dim.Render(age),
			eventIdx: i,
		})

		// Line 2: title + optional ×N aggregation badge.
		//
		// sanitizeRemoteText runs BEFORE truncation, and that order is
		// load-bearing: truncateRunes slices runes with no idea what an escape
		// is, so sanitising afterwards would leave a cut sequence swallowing
		// the styling bytes that follow it. A title comes from a pane's own
		// child via the hook spool and reaches the terminal with no VT
		// emulator in between, so U+202E — printable, and therefore past any
		// C0-only filter — would reverse the rendered line.
		titleBody := "  " + sanitizeRemoteText(e.Title)
		if e.Data != nil {
			if n, err := strconv.Atoi(e.Data["count"]); err == nil && n > 1 {
				titleBody += "  ×" + e.Data["count"]
			}
		}
		titleText := truncateRunes(titleBody, innerW)
		if selected {
			titleText = lipgloss.NewStyle().Reverse(true).Render(titleText)
		} else {
			titleText = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(titleText)
		}
		out = append(out, renderedLine{text: titleText, eventIdx: i})

		// Line 3: where the pane lives, so a click's destination is visible
		// before the click. A pane that is gone says so instead — the sidebar
		// carries events that outlive their pane (pane_destroyed is one), and
		// offering a jump that silently does nothing is worse than saying why.
		locText := "  " + sanitizeRemoteText(label)
		if !alive {
			locText = "  (closed)"
		}
		locStyle := dim
		if selected {
			locStyle = dim.Reverse(true)
		}
		out = append(out, renderedLine{
			text:     locStyle.Render(truncateRunes(locText, innerW)),
			eventIdx: i,
		})

		// Line 4: excerpt — EMITTED ONLY WHEN THERE IS ONE. The old renderer
		// always emitted it, blank or not, to keep every card four lines so
		// the card-indexed pagination arithmetic stayed simple. Line-based
		// scrolling removes that constraint, and dropping the blank line is
		// most of the extra events now on screen.
		if e.Message != "" {
			preview := truncateRunes("  "+sanitizeRemoteText(firstNonEmptyLine(e.Message)), innerW)
			st := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			if selected {
				st = st.Reverse(true)
			}
			out = append(out, renderedLine{text: st.Render(preview), eventIdx: i})
		}
	}
	if len(out) > 0 {
		out = append(out, renderedLine{text: separator, eventIdx: -1})
	}
	return out
}

// notifyViewportHeight is how many card lines fit: the box interior less the
// title row and the hints row.
func notifyViewportHeight(height int) int {
	h := height - 2 /* borders */ - 2 /* title + hints */
	if h < 1 {
		h = 1
	}
	return h
}

// ScrollBy moves the card viewport by delta lines, clamped to the content.
//
// height is passed in rather than stored because the sidebar is drawn at the
// tab area's height, which changes with the terminal — a stored copy would be
// one frame stale exactly when the user resizes and scrolls together.
func (nc *NotificationCenter) ScrollBy(delta, height int) {
	nc.scroll += delta
	nc.clampScroll(height, nil)
}

// clampScroll bounds nc.scroll to the rendered content.
//
// loc may be nil: the locator changes a line's TEXT, never how many lines a
// card occupies, so the line count is the same either way.
func (nc *NotificationCenter) clampScroll(height int, loc paneLocator) {
	total := len(notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, loc))
	maxScroll := total - notifyViewportHeight(height)
	if maxScroll < 0 {
		maxScroll = 0
	}
	if nc.scroll > maxScroll {
		nc.scroll = maxScroll
	}
	if nc.scroll < 0 {
		nc.scroll = 0
	}
}

// eventIndexAtRow maps a screen row to the index (into visibleEvents()) of the
// card drawn there, or -1 for chrome and out-of-range rows.
func (nc *NotificationCenter) eventIndexAtRow(y, height int, loc paneLocator) int {
	vy := y - notifyViewportOffset
	if vy < 0 || vy >= notifyViewportHeight(height) {
		return -1
	}
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, loc)
	idx := vy + nc.scroll
	if idx < 0 || idx >= len(lines) {
		return -1
	}
	return lines[idx].eventIdx
}

// SelectIndex moves the cursor to a visible-list index and brings it into view.
func (nc *NotificationCenter) SelectIndex(i, height int) {
	if i < 0 || i >= len(nc.visibleEvents()) {
		return
	}
	nc.cursor = i
	nc.revealCursor(height)
}

// revealCursor scrolls the minimum distance needed to show the selected card
// whole.
//
// Called after keyboard navigation and after a click, never when an event
// arrives: a new event landing at index 0 must not yank a user reading history
// back to the top.
func (nc *NotificationCenter) revealCursor(height int) {
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil)
	first, last := -1, -1
	for i, l := range lines {
		if l.eventIdx != nc.cursor {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return
	}
	vh := notifyViewportHeight(height)
	if first < nc.scroll {
		nc.scroll = first
	} else if last >= nc.scroll+vh {
		nc.scroll = last - vh + 1
	}
	nc.clampScroll(height, nil)
}

// View renders the sidebar at the given height.
//
// loc resolves each card's project/tab label and liveness; pass nil in a test
// that does not care, and every pane then reads as alive with an empty label.
func (nc *NotificationCenter) View(height int, loc paneLocator) string {
	innerW := nc.width - 2
	innerH := height - 2
	if innerW < 5 || innerH < 3 {
		return ""
	}

	out := make([]string, 0, innerH)
	out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).
		Render(truncateRunes(" Notifications ", innerW)))

	lines := notificationLines(nc.visibleEvents(), innerW, nc.cursor, nc.focused, loc)

	if len(lines) == 0 {
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("238")).
			Render(truncateRunes(strings.Repeat("·", innerW), innerW)))
		// Naming the override matters only when something is actually being
		// hidden: on an unfiltered center there is nothing for it to reveal.
		empty := "No notifications"
		if !nc.showAll && nc.groups != nil && len(nc.events) > 0 {
			empty = "None shown (a: show all)"
		}
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
			Render(truncateRunes(empty, innerW)))
	} else {
		nc.clampScroll(height, loc)
		vh := notifyViewportHeight(height)
		for i := nc.scroll; i < nc.scroll+vh && i < len(lines); i++ {
			out = append(out, lines[i].text)
		}
	}

	for len(out) < innerH-1 {
		out = append(out, "")
	}

	hints := "^!N Focus  Click Go"
	if nc.focused {
		hints = "↑↓ ⏎go d/D a:all Esc"
	}
	if nc.showAll {
		hints = "SHOWING ALL  a:filter"
	}
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
		Render(truncateRunes(hints, innerW)))

	borderColor := lipgloss.Color("63")
	if nc.focused {
		borderColor = lipgloss.Color("57")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(nc.width).
		Height(height).
		Render(strings.Join(out, "\n"))
}

// firstNonEmptyLine returns the first non-empty trimmed line of s, or "".
// Used by the sidebar to render a one-line preview of a multi-line excerpt.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateRunes truncates a string to maxWidth runes.
func truncateRunes(s string, maxWidth int) string {
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	return string(runes[:maxWidth])
}

// severityNameStyle returns a style for the pane name colored by severity.
func severityNameStyle(severity string) lipgloss.Style {
	switch severity {
	case "error":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	case "warning":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // orange
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	}
}

func relativeTime(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
