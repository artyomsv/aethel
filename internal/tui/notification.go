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

// View renders the sidebar at the given height.
func (nc *NotificationCenter) View(height int) string {
	innerW := nc.width - 2
	innerH := height - 2
	if innerW < 5 || innerH < 3 {
		return ""
	}

	var lines []string

	// Title
	title := " Notifications "
	title = truncateRunes(title, innerW)
	lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Render(title))

	separator := lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render(
		truncateRunes(strings.Repeat("·", innerW), innerW),
	)

	if len(nc.events) == 0 {
		lines = append(lines, separator)
		noEvents := "No notifications"
		noEvents = truncateRunes(noEvents, innerW)
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(noEvents))
	} else {
		// Each event = separator + name/time + title + excerpt = 4 lines.
		// The excerpt line is always emitted (blank if Message is empty) so
		// every event has the same height — keeps pagination math predictable.
		const linesPerEvent = 4
		maxVisible := (innerH - 3) / linesPerEvent
		if maxVisible < 1 {
			maxVisible = 1
		}
		start := 0
		if nc.cursor >= maxVisible {
			start = nc.cursor - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(nc.events) {
			end = len(nc.events)
		}

		for i := start; i < end; i++ {
			e := nc.events[i]
			selected := i == nc.cursor && nc.focused

			// Separator
			lines = append(lines, separator)

			// Pane name or ID
			name := e.PaneName
			if name == "" {
				name = e.PaneID
				if len(name) > 12 {
					name = name[:12]
				}
			}

			// Relative time (right-aligned)
			age := relativeTime(time.UnixMilli(e.Timestamp))

			// Line 1: colored name + right-aligned time
			nameStyle := severityNameStyle(e.Severity)
			if selected {
				nameStyle = nameStyle.Bold(true).Reverse(true)
			}
			styledName := nameStyle.Render(name)

			// Pad between name and time
			nameLen := len([]rune(name))
			ageLen := len([]rune(age))
			gap := innerW - nameLen - ageLen
			if gap < 1 {
				gap = 1
			}
			line1 := styledName + strings.Repeat(" ", gap) + lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(age)

			// Line 2: title (indented), with optional ×N badge for
			// daemon-side aggregation. count > 1 means this card already
			// absorbed N repeats of the same (PaneID, Title).
			// Sanitised for the same reason every pane-sourced string in
			// sidebar.go is: a card title comes from a pane's own child via the
			// hook spool, and this card is composited straight into the frame
			// with no VT emulator in between — so an unfiltered title is one of
			// the few strings in Quil that reaches the terminal with nothing
			// parsing it first. Escapes could clear the screen or write the
			// clipboard; U+202E is printable, so it survives a C0-only filter
			// and reverses the rendered line.
			//
			// It runs BEFORE the truncation below, and that order is
			// load-bearing: truncateRunes slices runes with no idea what an
			// escape is, so sanitising afterwards would still leave a cut
			// sequence swallowing the styling bytes that follow it.
			titleBody := "  " + sanitizeRemoteText(e.Title)
			countBadge := ""
			if e.Data != nil {
				if n, err := strconv.Atoi(e.Data["count"]); err == nil && n > 1 {
					countBadge = "  ×" + e.Data["count"]
				}
			}
			titleText := truncateRunes(titleBody+countBadge, innerW)
			if selected {
				titleText = lipgloss.NewStyle().Reverse(true).Render(titleText)
			} else {
				titleText = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(titleText)
			}

			// Line 3: excerpt — first non-empty line of Message (the
			// triggering output). Dim grey so it visually subordinates to
			// the title; blank line preserved if no excerpt so the
			// per-event height stays constant.
			excerptLine := ""
			if e.Message != "" {
				preview := "  " + firstNonEmptyLine(e.Message)
				preview = truncateRunes(preview, innerW)
				if selected {
					excerptLine = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Reverse(true).Render(preview)
				} else {
					excerptLine = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(preview)
				}
			}

			lines = append(lines, line1)
			lines = append(lines, titleText)
			lines = append(lines, excerptLine)
		}

		// Trailing separator
		if len(lines) < innerH-1 {
			lines = append(lines, separator)
		}
	}

	// Pad to fill height
	for len(lines) < innerH-1 {
		lines = append(lines, "")
	}

	// Key hints at bottom
	hints := "^!N Focus  Enter Go"
	if nc.focused {
		hints = "Up/Dn  Enter  d/D  Esc"
	}
	hints = truncateRunes(hints, innerW)
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(hints))

	content := strings.Join(lines, "\n")

	borderColor := lipgloss.Color("63")
	if nc.focused {
		borderColor = lipgloss.Color("57")
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(nc.width).
		Height(height).
		Render(content)
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
