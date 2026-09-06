package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
)

// Closing a dialog swaps a centred box for the whole frame, and the box is
// almost never the width of what replaces it — the create-pane setup step
// floors at 70 (setupDialogWidth), the split step is dialogWidth 60, the
// processes list 92, shortcuts 100. Bubble Tea v2's cell diff leaves border
// columns of the wider box standing on rows the new frame happens to paint
// identically, until something else forces a full repaint.
//
// The notification sidebar is where that shows up most, because it is NOT drawn
// while a dialog is open (sidebarOverlayWidth returns 0 for m.dialog !=
// dialogNone) — so its columns are exactly the ones making the transition, and
// the debris lands on its border.
//
// This is the same rule TestAboutMenu_OpeningAWiderDialogClearsTheScreen states
// for the About sub-dialogs, applied to the CLOSING edge, which had it only on
// the paths whose author happened to think of it: submitSetupDialog returned
// tea.ClearScreen, and handleCreatePaneSplit — the step that actually closes
// the create-pane flow — did not.
//
// The guarantee belongs in handleDialogKey rather than in each arm: that is the
// one place that sees the open→closed edge (it already runs promptNextUpgrade
// there), so no future dialog can be added without it.
func TestDialogClose_AlwaysClearsTheScreen(t *testing.T) {
	for _, tc := range []struct {
		name string
		open dialogScreen
		key  tea.KeyPressMsg
	}{
		{"create-pane, Esc at step 0", dialogCreatePane, tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"about, Esc", dialogAbout, tea.KeyPressMsg{Code: tea.KeyEscape}},
		{"shortcuts is NOT a close", dialogShortcuts, tea.KeyPressMsg{Code: tea.KeyEscape}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{width: 120, height: 40, dialog: tc.open, cfg: config.Default()}
			(&m).initKeymap()

			updated, cmd := m.handleDialogKey(tc.key)
			next := updated.(Model)

			if next.dialog != dialogNone {
				// Esc from Shortcuts goes BACK to About, not to none. It is in
				// the table as the control: a rule that fired on every key,
				// rather than on the closing edge, would clear here too and the
				// distinction this test draws would be meaningless.
				return
			}
			if !hasClearScreen(cmd) {
				t.Errorf("closing %v must emit tea.ClearScreen — the box it replaces "+
					"is a different size from the frame, and the diff leaves its border standing",
					tc.open)
			}
		})
	}
}

// The create-pane flow's real exit. handleCreatePaneSplit is reached through
// handleDialogKey in production (dispatchDialogKey -> handleCreatePaneKey ->
// handleCreatePaneSplit), and it is the step that sets dialogNone — so the
// clear has to survive that whole chain, not just the outermost arm.
func TestCreatePaneDialog_SubmitClearsTheScreen(t *testing.T) {
	m := Model{
		width: 120, height: 40,
		dialog:         dialogCreatePane,
		createPaneStep: 3, // the split-placement step
		dialogCursor:   0,
		cfg:            config.Default(),
	}
	(&m).initKeymap()

	updated, cmd := m.handleDialogKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	next := updated.(Model)

	if next.dialog != dialogNone {
		t.Fatalf("dialog = %v after submitting the split step, want dialogNone", next.dialog)
	}
	if !hasClearScreen(cmd) {
		t.Error("submitting the create-pane dialog must emit tea.ClearScreen — " +
			"the setup step is >= 70 columns, the split step 60, and the frame neither")
	}
}
