package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type PaneModel struct {
	ID     string
	Name   string
	Output strings.Builder
	Width  int
	Height int
	Active bool
}

func NewPaneModel(id string) *PaneModel {
	return &PaneModel{
		ID:   id,
		Name: id,
	}
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.Output.Write(data)
}

func (p *PaneModel) View() string {
	style := inactivePaneBorder
	if p.Active {
		style = activePaneBorder
	}

	// Calculate inner dimensions (subtract border)
	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.visibleContent(innerW, innerH)

	return style.
		Width(innerW).
		Height(innerH).
		Render(content)
}

func (p *PaneModel) visibleContent(width, height int) string {
	raw := p.Output.String()
	lines := strings.Split(raw, "\n")

	// Show only the last `height` lines
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}

	// Truncate long lines
	for i, line := range lines {
		if len(line) > width {
			lines[i] = line[:width]
		}
	}

	// Pad to fill height
	for len(lines) < height {
		lines = append(lines, "")
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
