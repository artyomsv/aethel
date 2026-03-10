package tui

import (
	"github.com/charmbracelet/lipgloss"
)

type SplitDir int

const (
	SplitHorizontal SplitDir = iota // panes side-by-side
	SplitVertical                   // panes stacked
)

type TabModel struct {
	ID         string
	Name       string
	Panes      []*PaneModel
	ActivePane int
	Split      SplitDir
	Width      int
	Height     int
}

func NewTabModel(id, name string) *TabModel {
	return &TabModel{
		ID:   id,
		Name: name,
	}
}

func (t *TabModel) AddPane(pane *PaneModel) {
	t.Panes = append(t.Panes, pane)
}

func (t *TabModel) ActivePaneModel() *PaneModel {
	if len(t.Panes) == 0 {
		return nil
	}
	if t.ActivePane >= len(t.Panes) {
		t.ActivePane = 0
	}
	return t.Panes[t.ActivePane]
}

func (t *TabModel) NextPane() {
	if len(t.Panes) > 0 {
		t.Panes[t.ActivePane].Active = false
		t.ActivePane = (t.ActivePane + 1) % len(t.Panes)
		t.Panes[t.ActivePane].Active = true
	}
}

func (t *TabModel) PrevPane() {
	if len(t.Panes) > 0 {
		t.Panes[t.ActivePane].Active = false
		t.ActivePane = (t.ActivePane - 1 + len(t.Panes)) % len(t.Panes)
		t.Panes[t.ActivePane].Active = true
	}
}

func (t *TabModel) Resize(w, h int) {
	t.Width = w
	t.Height = h

	if len(t.Panes) == 0 {
		return
	}

	switch t.Split {
	case SplitHorizontal:
		paneW := w / len(t.Panes)
		for i, pane := range t.Panes {
			pane.Width = paneW
			pane.Height = h
			if i == len(t.Panes)-1 {
				pane.Width = w - paneW*(len(t.Panes)-1)
			}
		}
	case SplitVertical:
		paneH := h / len(t.Panes)
		for i, pane := range t.Panes {
			pane.Width = w
			pane.Height = paneH
			if i == len(t.Panes)-1 {
				pane.Height = h - paneH*(len(t.Panes)-1)
			}
		}
	}
}

func (t *TabModel) View() string {
	if len(t.Panes) == 0 {
		return ""
	}

	views := make([]string, len(t.Panes))
	for i, pane := range t.Panes {
		views[i] = pane.View()
	}

	switch t.Split {
	case SplitVertical:
		return lipgloss.JoinVertical(lipgloss.Left, views...)
	default:
		return lipgloss.JoinHorizontal(lipgloss.Top, views...)
	}
}
