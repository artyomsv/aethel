package tui

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// A pane row containing an emoji must be measured the same way by the two
// authorities that see it: the VT emulator, which decides how many CELLS the
// glyph occupies in the grid, and ansi.StringWidth, which every frame join and
// every truncation measures the RENDERED row with.
//
// If those two disagree, quil's own frame is internally inconsistent — the row
// is padded to one width and measured as another, so the pane's right border
// and everything composited after it (the notification sidebar) land in the
// wrong column, and Bubble Tea's cell diff leaves the debris standing.
//
// The glyphs here are the ones a real codex pane prints on startup: its update
// banner leads with U+2728, and claude-code's header uses U+2733.
func TestPaneRow_EmojiWidthAgreesBetweenEmulatorAndMeasurer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		glyph string
		want  int // cells quil allots, per Unicode
	}{
		{"sparkles U+2728 (codex update banner)", "✨", 2},
		{"eight-spoked asterisk U+2733 (claude header)", "✳", 1},
		{"high voltage U+26A1", "⚡", 2},
		{"warning sign U+26A0", "⚠", 1},
		{"check mark U+2714", "✔", 1},
		{"CJK control (known-wide baseline)", "你", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := NewPaneModel("emoji", testRingBufSize)
			pane.ResizeVT(20, 4)
			pane.AppendOutput([]byte(tc.glyph + "X"))

			// What the emulator thinks: sum the per-cell widths it assigned.
			gridWidth := 0
			for x := 0; x < pane.vt.Width(); x++ {
				c := pane.vt.CellAt(x, 0)
				if c == nil {
					continue
				}
				if c.Content == " " || c.Content == "" {
					continue
				}
				gridWidth += c.Width
			}

			// What every join and truncation thinks about the rendered row.
			line := pane.styledCellLine(func(x int) *uv.Cell {
				return pane.vt.CellAt(x, 0)
			}, pane.vt.Width())
			rendered := ansi.StringWidth(line)

			if gridWidth != rendered {
				t.Errorf("emulator says %d cells, ansi.StringWidth says %d for %q+X — "+
					"quil pads the row to one width and measures it as the other",
					gridWidth, rendered, tc.glyph)
			}
			if gridWidth != tc.want+1 { // +1 for the trailing X
				t.Errorf("%q measures %d cells, want %d — a change here shifts every "+
					"column to the right of the glyph on that row",
					tc.glyph, gridWidth-1, tc.want)
			}
		})
	}
}
