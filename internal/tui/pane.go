package tui

import (
	"github.com/charmbracelet/x/vt"
)

type PaneModel struct {
	ID     string
	Name   string
	vt     *vt.SafeEmulator
	Width  int
	Height int
	Active bool
}

func NewPaneModel(id string) *PaneModel {
	return &PaneModel{
		ID:   id,
		Name: id,
		vt:   vt.NewSafeEmulator(80, 24),
	}
}

func (p *PaneModel) AppendOutput(data []byte) {
	p.vt.Write(data)
}

func (p *PaneModel) ResizeVT(cols, rows int) {
	if cols > 0 && rows > 0 && (cols != p.vt.Width() || rows != p.vt.Height()) {
		p.vt.Resize(cols, rows)
	}
}

func (p *PaneModel) View() string {
	style := inactivePaneBorder
	if p.Active {
		style = activePaneBorder
	}

	innerW := p.Width - 2
	innerH := p.Height - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	content := p.vt.Render()

	return style.
		Width(innerW).
		Height(innerH).
		Render(content)
}
