package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/charmbracelet/x/ansi"
)

// The sidebar is composited by overlayRight, which aligns overlay line i with
// tab-content line i and pads the LEFT side to totalW-overlayW. It does not
// measure the overlay at all.
//
// So the box View returns must be an exact rectangle: every line exactly
// nc.width cells, and exactly `height` of them. A line one cell too WIDE pushes
// the composed row past the terminal and the terminal wraps it, shifting every
// row below. A line too NARROW leaves the cells to its right holding whatever
// the previous frame painted there — a border glyph from a dialog, or pane text.
// Both read as "the sidebar border is broken".
func TestView_IsAnExactRectangle(t *testing.T) {
	cases := []struct {
		name   string
		width  int
		height int
		events []ipc.PaneEventPayload
	}{
		{"empty", 30, 20, nil},
		{"one card", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", "Reply ready", "some output"),
		}},
		{"no excerpt", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", "Reply ready", ""),
		}},
		{"long pane name", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", strings.Repeat("codex-instance", 4), "Reply ready", "out"),
		}},
		{"long title", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", strings.Repeat("Working on: a very long prompt ", 4), "out"),
		}},
		{"wide glyph name", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", strings.Repeat("构建", 20), "Reply ready", "out"),
		}},
		{"wide glyph excerpt", 30, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", "Reply ready", strings.Repeat("最后一行", 20)),
		}},
		{"narrow box", 22, 20, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", "Reply ready", "some output"),
		}},
		{"short box", 30, 8, []ipc.PaneEventPayload{
			boxEvent("TEST-1", "shell", "Reply ready", "some output"),
		}},
		{"many cards", 30, 20, func() []ipc.PaneEventPayload {
			var out []ipc.PaneEventPayload
			for i := 0; i < 12; i++ {
				out = append(out, boxEvent("TEST-"+string(rune('a'+i)), "shell", "Reply ready", "out"))
			}
			return out
		}()},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, focused := range []bool{false, true} {
				nc := NewNotificationCenter(c.width, 50)
				nc.focused = focused
				for _, e := range c.events {
					nc.AddEvent(e)
				}

				out := nc.View(c.height, liveLoc)
				if out == "" {
					continue // a refused degenerate size renders nothing, by design
				}
				lines := strings.Split(out, "\n")

				if len(lines) != c.height {
					t.Errorf("focused=%v: box is %d lines, want %d — overlayRight aligns line i with tab-content line i",
						focused, len(lines), c.height)
				}
				for i, l := range lines {
					if w := lipgloss.Width(l); w != c.width {
						t.Errorf("focused=%v line %d is %d cells, want %d: %q",
							focused, i, w, c.width, ansi.Strip(l))
					}
				}
			}
		})
	}
}

func boxEvent(id, paneName, title, message string) ipc.PaneEventPayload {
	return ipc.PaneEventPayload{
		ID: id, PaneID: "pane-" + id, PaneName: paneName,
		Type: "process_exit", Title: title, Message: message,
		Severity: "info", Timestamp: 0,
	}
}
