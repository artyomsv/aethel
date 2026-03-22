package tui

// TabModel represents a single tab containing a tree of panes.
type TabModel struct {
	ID         string
	Name       string
	Color      string
	Root       *LayoutNode // binary split tree (nil = empty tab)
	ActivePane string      // pane ID of the active pane
	Width      int
	Height     int
	focusMode  bool // true = active pane fills entire tab (F11)
}

func NewTabModel(id, name string) *TabModel {
	return &TabModel{
		ID:   id,
		Name: name,
	}
}

// ActivePaneModel returns the currently active pane, or nil.
func (t *TabModel) ActivePaneModel() *PaneModel {
	if t.Root == nil {
		return nil
	}
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return nil
	}
	for _, p := range leaves {
		if p.ID == t.ActivePane {
			return p
		}
	}
	// Fallback: if ActivePane is stale, use first leaf.
	t.ActivePane = leaves[0].ID
	leaves[0].Active = true
	return leaves[0]
}

// NextPane advances focus to the next pane (in-order traversal order).
func (t *TabModel) NextPane() {
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return
	}
	idx := t.activeIndex(leaves)
	leaves[idx].Active = false
	next := (idx + 1) % len(leaves)
	leaves[next].Active = true
	t.ActivePane = leaves[next].ID
}

// PrevPane moves focus to the previous pane.
func (t *TabModel) PrevPane() {
	leaves := t.Root.Leaves()
	if len(leaves) == 0 {
		return
	}
	idx := t.activeIndex(leaves)
	leaves[idx].Active = false
	prev := (idx - 1 + len(leaves)) % len(leaves)
	leaves[prev].Active = true
	t.ActivePane = leaves[prev].ID
}

// activeIndex finds the index of the active pane in leaves. Defaults to 0.
func (t *TabModel) activeIndex(leaves []*PaneModel) int {
	for i, p := range leaves {
		if p.ID == t.ActivePane {
			return i
		}
	}
	return 0
}

// Resize recomputes dimensions for the entire layout tree.
func (t *TabModel) Resize(w, h int) {
	t.Width = w
	t.Height = h
	if t.focusMode {
		if pane := t.ActivePaneModel(); pane != nil {
			pane.Width = w
			pane.Height = h
			cols := w - 2
			rows := h - 2
			if cols < 1 {
				cols = 1
			}
			if rows < 1 {
				rows = 1
			}
			pane.ResizeVT(cols, rows)
		}
		return
	}
	if t.Root != nil {
		resizeNode(t.Root, w, h)
	}
}

// View renders the entire pane layout.
func (t *TabModel) View() string {
	if t.Root == nil {
		return ""
	}
	if t.focusMode {
		if pane := t.ActivePaneModel(); pane != nil {
			return pane.View()
		}
	}
	return renderNode(t.Root)
}

// ToggleFocus toggles pane focus mode on/off.
func (t *TabModel) ToggleFocus() {
	t.focusMode = !t.focusMode
}

// FocusMode returns whether focus mode is active.
func (t *TabModel) FocusMode() bool {
	return t.focusMode
}

// SplitAtPane splits the pane with the given ID, inserting a placeholder
// for the new pane. Returns the placeholder node (caller fills Pane later).
func (t *TabModel) SplitAtPane(paneID string, dir SplitDir) *LayoutNode {
	if t.Root == nil {
		return nil
	}
	return t.Root.SplitLeaf(paneID, dir)
}

// RemovePane removes the pane with the given ID, promoting its sibling.
// If the removed pane was active, focus moves to the first leaf.
func (t *TabModel) RemovePane(paneID string) {
	if t.Root == nil {
		return
	}
	// If the root is a single leaf with this ID, clear the tree.
	if t.Root.IsLeaf() && t.Root.Pane.ID == paneID {
		t.Root = nil
		t.ActivePane = ""
		return
	}
	if !t.Root.RemoveLeaf(paneID) {
		return
	}
	// If we removed the active pane, pick the first leaf.
	if t.ActivePane == paneID {
		leaves := t.Root.Leaves()
		if len(leaves) > 0 {
			t.ActivePane = leaves[0].ID
			leaves[0].Active = true
		}
	}
}
