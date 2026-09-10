package daemon

import apty "github.com/artyomsv/quil/internal/pty"

type terminalSize struct{ cols, rows int }

// newPaneSession gives a new child useful dimensions before its first paint.
// MCP can create panes in hidden tabs, so waiting for a TUI resize lets a child
// build its first screen for the default 80x24 terminal.
func (d *Daemon) newPaneSession(pane *Pane) apty.Session {
	cols, rows := 80, 24
	if size := d.clientSize.Load(); size != nil {
		cols, rows = size.cols, size.rows
	}
	for _, sibling := range d.session.Panes(pane.TabID) {
		if sibling.ID == pane.ID {
			continue
		}
		sibling.PluginMu.Lock()
		c, r, overlay := sibling.Cols, sibling.Rows, sibling.Overlay
		sibling.PluginMu.Unlock()
		if c > 0 && r > 0 && !overlay {
			cols, rows = c, r
			break
		}
	}
	pane.PluginMu.Lock()
	pane.Cols, pane.Rows = cols, rows
	pane.PluginMu.Unlock()
	return newSessionFn(cols, rows)
}
