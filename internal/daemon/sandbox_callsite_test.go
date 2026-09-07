package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	apty "github.com/artyomsv/quil/internal/pty"
	"github.com/artyomsv/quil/internal/sandbox"
)

// Call-site tests.
//
// The direct-call versions of these all passed against code where the call had
// been DELETED — the trap this repo has hit before. A helper being correct
// says nothing about whether anything invokes it, and for both rules below the
// consequence of nobody invoking it is silent: an un-isolated agent, or a
// deleted worktree.

// sandboxCallsiteFixture points QUIL_HOME at a temp dir, stubs the mapping so
// no git repository is needed, and reports Docker as available.
func sandboxCallsiteFixture(t *testing.T) (*Daemon, *Pane, sandbox.Mapping) {
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
	prevMap := sandboxMappingFn
	sandboxMappingFn = func(context.Context, string, string, string) (sandbox.Mapping, error) {
		return m, nil
	}
	t.Cleanup(func() { sandboxMappingFn = prevMap })

	// No hook binary is fetched: the override points at a file that exists.
	fake := filepath.Join(home, "quild-linux-amd64")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake quild: %v", err)
	}
	t.Setenv(sandboxQuildEnv, fake)

	prevRemove := sandboxRemoveFn
	sandboxRemoveFn = func(context.Context, string) error { return nil }
	t.Cleanup(func() { sandboxRemoveFn = prevRemove })

	d := &Daemon{session: NewSessionManager(1024), events: newEventQueue(50)}
	d.sandboxReg = newSandboxRegistry(home)
	pane := &Pane{ID: "p1", CWD: m.HostWorktree, SandboxImage: "img:1"}
	return d, pane, m
}

// The overlays are written by prepareSandbox, not merely by writeOverlays.
// Deleting the call is the mutation that survived a direct-call test, and it
// re-opens a measured data-loss bug: without the admin overlay, a
// container-side `git worktree prune` deletes the host's worktree
// registration, uncommitted work included.
func TestPrepareSandbox_WritesTheOverlays(t *testing.T) {
	d, pane, m := sandboxCallsiteFixture(t)

	got, err := d.prepareSandbox(context.Background(), pane, "claude-code", "img:1")
	if err != nil {
		t.Fatalf("prepareSandbox: %v", err)
	}
	for _, p := range []string{got.HostDotGitOverlay(), got.HostAdminGitdirOverlay()} {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("overlay %s was not written: %v", filepath.Base(p), err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("overlay %s is empty", filepath.Base(p))
		}
	}
	// And the alternates line, for the same reason: written by the call site
	// or the host cannot see anything the container commits.
	if _, err := os.Stat(sandbox.AlternatesPath(m)); err != nil {
		t.Errorf("the alternates line was not registered: %v", err)
	}
	if _, ok := d.sandboxReg.get("p1"); !ok {
		t.Error("the repository was not recorded; the startup repair cannot find it")
	}
}

// fakeSpawnSession is a PTY that records whether anything was started.
type fakeSpawnSession struct {
	apty.Session
	started bool
	cmd     string
}

func (f *fakeSpawnSession) Start(cmd string, _ ...string) error {
	f.started = true
	f.cmd = cmd
	return nil
}
func (f *fakeSpawnSession) SetEnv([]string) {}
func (f *fakeSpawnSession) SetCWD(string)   {}
func (f *fakeSpawnSession) Read([]byte) (int, error) {
	return 0, errors.New("closed")
}
func (f *fakeSpawnSession) Close() error { return nil }

// The refusal that must never soften. A pane the user asked to isolate must
// not quietly run unisolated because Docker was not running — and the mutation
// turning this into a log-and-continue was green, because the sandbox branch
// of spawnPane had no test at all.
func TestSpawnPane_RefusesWhenTheSandboxIsUnavailable(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"

	// Docker reports itself unavailable.
	prev := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		return sandbox.Info{}, errors.New("cannot connect to the docker daemon")
	}
	t.Cleanup(func() { sandboxProbeFn = prev })

	pty := &fakeSpawnSession{}
	err := d.spawnPane(pane, pty, false)
	if err == nil {
		t.Fatal("spawnPane succeeded with no container engine — the agent would be " +
			"running on the HOST, un-isolated, which is the one outcome this feature must prevent")
	}
	if pty.started {
		t.Errorf("a process was started anyway: %q", pty.cmd)
	}
}

// The positive half, so the refusal test cannot pass by the sandbox branch
// never running at all: with an engine available the command becomes docker.
func TestSpawnPane_SandboxPaneRunsDocker(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"

	prev := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		return sandbox.Info{ServerVersion: "29.6.2", OSType: "linux", Arch: "amd64"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prev })

	pty := &fakeSpawnSession{}
	if err := d.spawnPane(pane, pty, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if !pty.started {
		t.Fatal("nothing was started")
	}
	if filepath.Base(pty.cmd) != "docker" && filepath.Base(pty.cmd) != "docker.exe" {
		t.Errorf("started %q, want the docker CLI — the pane is not in a container", pty.cmd)
	}
	// The host CWD is kept: the git subsystem and the close dialog read it.
	// The agent's directory comes from `docker run -w`.
	if pane.CWD == "" {
		t.Error("the pane's host CWD was cleared")
	}
}

// A host pane must be untouched by any of this.
func TestSpawnPane_HostPaneIsUnaffected(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"
	pane.SandboxImage = "" // not sandboxed

	pty := &fakeSpawnSession{}
	if err := d.spawnPane(pane, pty, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	if filepath.Base(pty.cmd) == "docker" || filepath.Base(pty.cmd) == "docker.exe" {
		t.Error("an ordinary pane was wrapped in a container")
	}
	_ = config.QuilDir()
}
