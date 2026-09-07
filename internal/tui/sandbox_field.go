package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// The create dialog's sandbox row.
//
// One row that is both a switch and a text field: space toggles the sandbox on
// and off, and while it is on, typing edits the image. Two rows would be the
// obvious alternative and would cost a row of dialog height for a field that is
// meaningless when the switch is off — and this dialog is already one row from
// the bottom of a 24-row terminal.

// sandboxImageMax bounds what the field will hold. Docker's own reference limit
// is 255; anything longer is a paste accident.
const sandboxImageMax = 255

// renderSetupSandboxField draws the row.
func (m Model) renderSetupSandboxField(focused bool) string {
	box := "[ ]"
	if m.sandboxOn {
		box = "[x]"
	}
	prefix := "  "
	style := dialogNormal
	if focused {
		prefix = "> "
		style = dialogSelected
	}

	var b strings.Builder
	b.WriteString(prefix + style.Render(box+" Run in a Docker container") + "\n")

	if !m.sandboxOn {
		if focused {
			b.WriteString("    " + dialogSubtle.Render("space to enable — the agent is confined to this checkout"))
		}
		return b.String()
	}

	// The image is the ONE thing the user supplies, and there is no default:
	// Quil publishes no image, so a blank field has to say so rather than
	// silently substituting something.
	val := m.sandboxImage
	if val == "" {
		val = dialogSubtle.Render("(image required)")
	} else {
		val = truncateToWidth(sanitizeRemoteText(val), m.setupTextWidth()-10)
	}
	b.WriteString("    " + dialogValStyle.Render("image: ") + val)
	if focused {
		b.WriteString("\n    " + dialogSubtle.Render("type to edit — space toggles off"))
	}
	return b.String()
}

// handleSandboxFieldKey edits the row. Reports whether it consumed the key.
//
// It consumes printable text ONLY while the sandbox is on, so space keeps its
// toggle meaning when off and Tab/Enter always reach the dialog.
func (m *Model) handleSandboxFieldKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	// Both spellings: a real keypress arrives as "space" while a pasted or
	// synthesised one can arrive as " ". The toggle-row precedent in
	// dialog.go matches the same pair.
	case " ", "space":
		m.sandboxOn = !m.sandboxOn
		if m.sandboxOn && m.sandboxImage == "" {
			m.sandboxImage = m.cfg.Sandbox.DefaultImage
		}
		return true
	case "backspace":
		if m.sandboxOn && m.sandboxImage != "" {
			r := []rune(m.sandboxImage)
			m.sandboxImage = string(r[:len(r)-1])
		}
		return m.sandboxOn
	}
	if !m.sandboxOn {
		return false
	}
	// isPrintableText is the same gate the command palette uses, so a control
	// character or a bracketed-paste marker cannot reach the field.
	if t := msg.Text; t != "" && isPrintableText(t) {
		if len([]rune(m.sandboxImage))+len([]rune(t)) <= sandboxImageMax {
			m.sandboxImage += t
		}
		return true
	}
	return false
}

// sandboxSubmitError explains why the dialog cannot submit, or "" when it can.
//
// An empty image is refused rather than defaulted. There is no image Quil can
// honestly pick — it publishes none, and guessing one would run the user's
// agent in a container they never chose.
func (m Model) sandboxSubmitError() string {
	if !m.sandboxOn {
		return ""
	}
	if strings.TrimSpace(m.sandboxImage) == "" {
		return "Enter a container image, or turn the sandbox off"
	}
	return ""
}

// sandboxSpec is what the create payload carries, or nil.
//
// Nil unless the user turned the row on. That keeps every other create
// byte-identical on the wire and takes no new branch anywhere in the daemon —
// the same property the worktree spec has, and for the same reason.
func (m Model) sandboxSpec() *ipc.SandboxSpec {
	if !m.sandboxOn {
		return nil
	}
	image := strings.TrimSpace(m.sandboxImage)
	if image == "" {
		// Unreachable through the dialog: submitSetupDialog refuses first.
		// Returning nil rather than an empty spec means a future caller that
		// skips that check creates an ordinary pane instead of asking the
		// daemon to run `docker run ""`.
		return nil
	}
	return &ipc.SandboxSpec{Image: image}
}

// resetSandboxField clears the row's state.
//
// Called when the create dialog OPENS, never on a close path. Three exits skip
// the teardown — the step-0 Esc, the instance-delete detour, and the split
// step's early refusals — and a sandbox flag surviving one of them would put
// the next plain create in a container the user did not ask for. The dialog's
// other fields learned this the same way.
func (m *Model) resetSandboxField(dest string) {
	m.sandboxOn = false
	m.sandboxImage = m.cfg.Sandbox.DefaultImage
	// Pin the capability answer for the life of the dialog. It is fetched
	// asynchronously and re-probed on a timer, so reading it live would add
	// or remove a row under a cursor the key handler is already holding an
	// index into.
	m.sandboxDialogAvail = m.sandboxAvailableFor(dest)
}
