package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// The ordinary (no-worktree) arm of Ctrl+T, and the one that shipped broken.
//
// handleCreateTab does not pass the spec through; firstPanePayload hand-copies
// a fixed field list into a CreatePanePayload, and Sandbox was missing from it.
// The result was silent and total: `create_tab` with a sandbox spec and no
// worktree produced a claude pane running ON THE HOST, in the project root,
// with --dangerously-skip-permissions, no container, and no error in any log —
// the exact "a sandbox pane never falls back to a host spawn" refusal the
// feature is built around, defeated by an omission in a copy.
//
// Asserted through handleCreateTab rather than against firstPanePayload,
// because the defect WAS the copy: a test calling the helper and checking its
// return would have been written against the same missing line.
func TestHandleCreateTab_OrdinaryFirstPaneCarriesTheSandbox(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{
		Type:    "terminal",
		Sandbox: &ipc.SandboxSpec{Image: "quil-sandbox:latest"},
	}))

	p := newTabPane(t, d)
	p.PluginMu.Lock()
	image := p.SandboxImage
	p.PluginMu.Unlock()

	if image != "quil-sandbox:latest" {
		t.Errorf("SandboxImage = %q, want the requested image — the pane is running "+
			"on the host with no container and no error", image)
	}
}

// A create that asked for no sandbox must stay byte-identical to what it was
// before this feature existed, or every ordinary tab takes a new branch.
func TestHandleCreateTab_NoSandboxSpecLeavesTheFieldEmpty(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())

	d.handleCreateTab(nil, createTabMsg(t, &ipc.FirstPaneSpec{Type: "terminal"}))

	p := newTabPane(t, d)
	p.PluginMu.Lock()
	image := p.SandboxImage
	p.PluginMu.Unlock()

	if image != "" {
		t.Errorf("SandboxImage = %q on a plain create, want empty", image)
	}
}

// firstPanePayload drops the plugin fields when the type was DOWNGRADED,
// because they describe a different program. Sandbox follows them: a
// downgraded pane is the worktree placeholder, which spawns no child at all,
// and createFirstPaneWorktree carries the full set onto the real pane.
func TestFirstPanePayload_DowngradedTypeCarriesNoSandbox(t *testing.T) {
	d := overlayTestDaemon(t, config.Default())
	spec := ipc.FirstPaneSpec{
		Type:    "claude-code",
		Sandbox: &ipc.SandboxSpec{Image: "quil-sandbox:latest"},
	}

	got := d.firstPanePayload("tab-1", "terminal", spec)

	if got.Sandbox != nil {
		t.Errorf("a downgraded placeholder carried a sandbox spec: %+v", got.Sandbox)
	}
}
