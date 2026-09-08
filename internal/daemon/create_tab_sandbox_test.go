package daemon

import (
	"context"
	"os"
	"path/filepath"

	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/sandbox"
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
	// The mapping is STUBBED, and that is not tidiness — without it this test
	// wrote into the developer's real repository. A temp QUIL_HOME is not
	// enough: prepareSandbox resolves the checkout from the pane's CWD, which
	// here is the process's own, so sandbox.AddAlternate appended two lines
	// pointing at a t.TempDir() to the REAL .git/objects/info/alternates. Once
	// those directories vanished at test exit, every git command in that
	// repository answered "unable to normalize alternate object path".
	//
	// It passed in CI only by accident: the golang:1.25-alpine image has no
	// git binary, so resolveRepo fails there and nothing is written. Anyone
	// running this package natively broke their own checkout.
	stubSandboxMapping(t)

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

// stubSandboxMapping points the sandbox mapping at temp directories so a test
// in this package can never reach the developer's real repository.
//
// It exists because a temp QUIL_HOME does NOT isolate this: prepareSandbox
// resolves the checkout from the pane's CWD, and sandbox.AddAlternate then
// writes into whatever repository that turns out to be. A test that reaches a
// real .git leaves alternates lines pointing at directories t.TempDir() is
// about to delete, and every later git command in that repository fails.
//
// Any new test in this package that drives a create with a Sandbox spec owes
// this call, or sandboxCallsiteFixture, which does the same thing.
func stubSandboxMapping(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)

	repo := filepath.Join(t.TempDir(), "main", ".git")
	if err := os.MkdirAll(filepath.Join(repo, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	m := sandbox.Mapping{
		PaneID:         "p1",
		Kind:           sandbox.KindWorktree,
		AdminName:      "wt",
		Slug:           "wt-abcd1234",
		HostWorktree:   filepath.Join(t.TempDir(), "wt"),
		HostGitCommon:  repo,
		HostPaneRoot:   filepath.Join(home, "sandbox", "panes", "p1"),
		HostOverlayDir: filepath.Join(home, "sandbox", "overlays", "p1"),
		HostEmptyDir:   filepath.Join(home, "sandbox", "empty"),
		HostEmptyFile:  filepath.Join(home, "sandbox", "empty-file"),
	}
	prev := sandboxMappingFn
	sandboxMappingFn = func(context.Context, string, string, string) (sandbox.Mapping, error) {
		return m, nil
	}
	t.Cleanup(func() { sandboxMappingFn = prev })
}
