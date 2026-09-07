package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

// promptsCWDPlugin is the shape that can show the row: the sandbox mounts the
// checkout the CWD step settles on.
func promptsCWDPlugin() *plugin.PanePlugin {
	return &plugin.PanePlugin{
		Name:    "claude-code",
		Command: plugin.CommandConfig{PromptsCWD: true},
	}
}

// The answer describes the DAEMON's machine — the container runs where the
// daemon runs. One shared answer would be the 2026-09-03 bug again, where a
// single registry served every destination and the last host to reply spoke
// for all of them.
func TestSandboxAvailableFor_IsPerDestination(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.applySandboxCap("remote", ipc.SandboxCapRespPayload{Error: "docker not available"})

	if !m.sandboxAvailableFor("") {
		t.Error("the local daemon's own answer was not filed")
	}
	if m.sandboxAvailableFor("remote") {
		t.Error("a remote daemon without docker reads as available")
	}
	// A destination that has never answered is unavailable, NOT a fallback to
	// some other machine's answer: the client cannot see any daemon's Docker
	// engine itself, and a wrong offer means a pane that dies at spawn.
	if m.sandboxAvailableFor("never-asked") {
		t.Error("a silent destination reads as available")
	}
}

// The row is gated on the PINNED answer, so a capability response landing
// mid-dialog cannot add or remove a row under a live cursor.
func TestShowSandboxField_UsesThePinnedAnswer(t *testing.T) {
	var m Model
	p := promptsCWDPlugin()

	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	if m.showSandboxField(p) {
		t.Error("the row appeared before the dialog pinned an answer")
	}
	m.resetSandboxField("")
	if !m.showSandboxField(p) {
		t.Error("the row is absent after pinning an available answer")
	}
	// A later answer must not change the open dialog.
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Error: "engine stopped"})
	if !m.showSandboxField(p) {
		t.Error("a late answer removed a row from an open dialog")
	}
}

// A plugin that never asks for a directory has nothing to mount.
func TestShowSandboxField_NeedsPromptsCWD(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")

	if m.showSandboxField(&plugin.PanePlugin{Name: "terminal"}) {
		t.Error("the row is offered for a plugin with no CWD step")
	}
}

// setupFieldCount and setupFieldKind must agree, or the cursor lands on a row
// the renderer never draws.
func TestSetupFieldKind_SandboxSitsBetweenWorktreeAndSession(t *testing.T) {
	var m Model
	m.applySandboxCap("", ipc.SandboxCapRespPayload{Available: true})
	m.resetSandboxField("")

	p := promptsCWDPlugin()
	p.Command.Sessions = "claude"

	var kinds []string
	for i := 0; i < m.setupFieldCount(p); i++ {
		k, _ := m.setupFieldKind(p, i)
		kinds = append(kinds, k)
	}
	want := []string{"cwd", "worktree", "sandbox", "session", "continue"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("field order = %v, want %v", kinds, want)
	}
}

// With the row hidden the walk must be exactly what it was before this
// feature: every existing dialog keeps its layout.
func TestSetupFieldKind_UnchangedWhenTheRowIsHidden(t *testing.T) {
	var m Model // nothing pinned: unavailable
	p := promptsCWDPlugin()
	p.Command.Sessions = "claude"

	var kinds []string
	for i := 0; i < m.setupFieldCount(p); i++ {
		k, _ := m.setupFieldKind(p, i)
		kinds = append(kinds, k)
	}
	want := []string{"cwd", "worktree", "session", "continue"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("field order = %v, want %v", kinds, want)
	}
}

func TestHandleSandboxFieldKey_SpaceToggles(t *testing.T) {
	var m Model
	if !m.handleSandboxFieldKey(tea.KeyPressMsg{Code: ' ', Text: " "}) {
		t.Fatal("space was not consumed")
	}
	if !m.sandboxOn {
		t.Error("space did not enable the sandbox")
	}
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: ' ', Text: " "})
	if m.sandboxOn {
		t.Error("space did not disable the sandbox")
	}
}

// Typing must not reach the field while the switch is off, or the row would
// swallow keys that mean nothing there.
func TestHandleSandboxFieldKey_TypingOnlyWhileOn(t *testing.T) {
	var m Model
	if m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) {
		t.Error("a keystroke was consumed while the sandbox is off")
	}
	if m.sandboxImage != "" {
		t.Errorf("image changed while off: %q", m.sandboxImage)
	}

	m.sandboxOn = true
	for _, r := range "alpine" {
		m.handleSandboxFieldKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if m.sandboxImage != "alpine" {
		t.Errorf("image = %q, want alpine", m.sandboxImage)
	}
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.sandboxImage != "alpin" {
		t.Errorf("after backspace image = %q, want alpin", m.sandboxImage)
	}
}

// Tab and Enter must always reach the dialog, or the row traps the cursor.
func TestHandleSandboxFieldKey_DoesNotSwallowNavigation(t *testing.T) {
	var m Model
	m.sandboxOn = true
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyEnter},
		{Code: tea.KeyEsc},
	} {
		if m.handleSandboxFieldKey(k) {
			t.Errorf("%v was consumed by the sandbox row", k)
		}
	}
}

func TestHandleSandboxFieldKey_BoundsTheImage(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxImage = strings.Repeat("x", sandboxImageMax)
	m.handleSandboxFieldKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(m.sandboxImage) != sandboxImageMax {
		t.Errorf("image grew past the cap: %d", len(m.sandboxImage))
	}
}

// There is no image Quil can honestly pick: it publishes none. Defaulting one
// would run the user's agent in a container they never chose.
func TestSandboxSubmitError_RefusesAnEmptyImage(t *testing.T) {
	var m Model
	if m.sandboxSubmitError() != "" {
		t.Error("an off sandbox reported an error")
	}
	m.sandboxOn = true
	if m.sandboxSubmitError() == "" {
		t.Error("an empty image was accepted")
	}
	m.sandboxImage = "   "
	if m.sandboxSubmitError() == "" {
		t.Error("a whitespace-only image was accepted")
	}
	m.sandboxImage = "alpine"
	if got := m.sandboxSubmitError(); got != "" {
		t.Errorf("a valid image was refused: %s", got)
	}
}

// nil keeps every other create byte-identical on the wire and takes no new
// branch anywhere in the daemon.
func TestSandboxSpec_NilUnlessEnabled(t *testing.T) {
	var m Model
	if m.sandboxSpec() != nil {
		t.Error("a spec was produced with the row off")
	}
	m.sandboxOn = true
	if m.sandboxSpec() != nil {
		t.Error("a spec was produced with no image")
	}
	m.sandboxImage = "  alpine:3  "
	spec := m.sandboxSpec()
	if spec == nil || spec.Image != "alpine:3" {
		t.Errorf("spec = %+v, want a trimmed alpine:3", spec)
	}
}

// Three dialog exits skip the teardown, so a flag surviving one would put the
// NEXT plain create in a container the user did not ask for.
func TestResetSandboxField_ClearsTheSwitch(t *testing.T) {
	var m Model
	m.sandboxOn = true
	m.sandboxImage = "left-over"
	m.resetSandboxField("")

	if m.sandboxOn {
		t.Error("the switch survived a reset")
	}
	if m.sandboxSpec() != nil {
		t.Error("a stale spec survived a reset")
	}
}
