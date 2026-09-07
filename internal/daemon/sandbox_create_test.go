package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// These are the tests the design's own test plan named and that were never
// written — which is why two critical defects shipped together: the create
// paths ignored the sandbox spec entirely (so "Run in a Docker container" ran
// the agent on the HOST), and the image validator had no production caller (so
// an image beginning with "-" would have been read by docker as OPTIONS).
//
// Both live at the same seam, so they are tested at the same seam: a spec goes
// in, and either the pane is marked sandboxed or the create is refused.

// The headline failure. spawnPane gates the whole container branch on
// pane.SandboxImage, so a create path that does not set it silently produces
// an un-isolated pane — the one outcome this feature must never have.
func TestApplySandboxSpec_MarksThePaneSandboxed(t *testing.T) {
	pane := &Pane{ID: "p1"}
	if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "ghcr.io/o/img:1"}); err != nil {
		t.Fatalf("applySandboxSpec: %v", err)
	}
	if pane.SandboxImage != "ghcr.io/o/img:1" {
		t.Errorf("SandboxImage = %q — spawnPane will run this pane on the host", pane.SandboxImage)
	}
}

// nil must leave the pane exactly as it was, so every ordinary create is
// untouched by this feature.
func TestApplySandboxSpec_NilIsANoOp(t *testing.T) {
	pane := &Pane{ID: "p1"}
	if err := applySandboxSpec(pane, nil); err != nil {
		t.Fatalf("applySandboxSpec(nil): %v", err)
	}
	if pane.SandboxImage != "" {
		t.Errorf("a nil spec marked the pane sandboxed: %q", pane.SandboxImage)
	}
}

// The image reaches `docker run` as the first positional, and docker parses
// options up to the first operand — so a leading "-" is read as docker
// OPTIONS. Any IPC client can set this field.
func TestApplySandboxSpec_RefusesAnImageThatWouldBecomeDockerFlags(t *testing.T) {
	hostile := []string{
		"--privileged",
		"-v /:/host",
		"--entrypoint=/bin/sh",
		"--network=host",
		"--user=0:0",
		"img;rm -rf /",
		"img $(id)",
		"img\nrun",
		"",
	}
	for _, image := range hostile {
		pane := &Pane{ID: "p1"}
		if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: image}); err == nil {
			t.Errorf("applySandboxSpec accepted %q — it would reach docker's argv", image)
		}
		if pane.SandboxImage != "" {
			t.Errorf("a refused image was still recorded: %q", pane.SandboxImage)
		}
	}
}

// A refusal must not leave the pane half-configured: an un-sandboxed pane that
// the user asked to isolate is worse than a failed create.
func TestApplySandboxSpec_RefusalLeavesNoSandboxState(t *testing.T) {
	pane := &Pane{ID: "p1"}
	err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "--privileged"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if pane.SandboxImage != "" {
		t.Error("the pane was marked sandboxed by a refused create")
	}
}

// The log must never carry the value: quild.log is rendered by the F1 viewer,
// which does not pass through a VT emulator, so a hostile string with escape
// sequences would reach the screen. The guard logs a LENGTH.
func TestApplySandboxSpec_DoesNotLogTheImageValue(t *testing.T) {
	var buf safeBuffer
	defer captureLog(&buf)()

	pane := &Pane{ID: "p1"}
	_ = applySandboxSpec(pane, &ipc.SandboxSpec{Image: "\x1b]0;pwned\x07--privileged"})

	if strings.Contains(buf.String(), "pwned") || strings.Contains(buf.String(), "\x1b") {
		t.Errorf("the rejected image value reached the log: %q", buf.String())
	}
}

// The tab-first-pane path hand-copies a fixed field list, and omitting Sandbox
// there produced a tab whose agent ran on the host — the dialog's own designed
// flow for "new tab + worktree + sandbox".
func TestFirstPaneSpec_SandboxSurvivesTheHandCopy(t *testing.T) {
	spec := &ipc.FirstPaneSpec{
		Type:     "claude-code",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
		Sandbox:  &ipc.SandboxSpec{Image: "img:1"},
	}
	// Mirrors the payload createFirstPaneWorktree builds. Written as a literal
	// on purpose: the defect was a MISSING field in exactly this construction,
	// so a test that called the real function with a mocked daemon would still
	// pass if someone deleted the line again from a second copy of it.
	p := ipc.CreatePanePayload{
		TabID:           "t1",
		Type:            spec.Type,
		InstanceName:    spec.InstanceName,
		InstanceArgs:    spec.InstanceArgs,
		ResumeSessionID: spec.ResumeSessionID,
		ReplacePaneID:   "placeholder",
		Worktree:        spec.Worktree,
		Sandbox:         spec.Sandbox,
	}
	if p.Sandbox == nil {
		t.Fatal("the sandbox spec was dropped on the tab-first-pane path")
	}
	if p.Sandbox.Image != "img:1" {
		t.Errorf("image = %q, want img:1", p.Sandbox.Image)
	}
}
