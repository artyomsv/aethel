package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/sandbox"
)

// sandboxFixture builds a daemon whose QUIL_HOME is a temp dir, plus a repo
// and a pane tree with one harvestable object. It returns the pane id.
//
// t.Setenv("QUIL_HOME", …) is not optional: config.QuilDir() otherwise answers
// the real ~/.quil, and these tests write alternates files into whatever it
// names.
func sandboxFixture(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIL_HOME", home)

	repo := filepath.Join(t.TempDir(), "main", ".git")
	if err := os.MkdirAll(filepath.Join(repo, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}

	paneID := "pane1"
	paneRoot := filepath.Join(home, "sandbox", "panes", paneID)
	if err := os.MkdirAll(filepath.Join(paneRoot, "objects", "ab"), 0o755); err != nil {
		t.Fatalf("mkdir pane: %v", err)
	}
	obj := filepath.Join(paneRoot, "objects", "ab",
		"0123456789abcdef0123456789abcdef012345")
	if err := os.WriteFile(obj, []byte("blob"), 0o444); err != nil {
		t.Fatalf("write object: %v", err)
	}

	// The event queue is not optional: the repair pass emits a card when it
	// strips a stale line, and New() always builds one.
	d := &Daemon{session: NewSessionManager(1024), events: newEventQueue(50)}
	d.sandboxReg = newSandboxRegistry(home)
	d.sandboxReg.put(paneID, repo)
	return d, paneID, repo
}

// stubDockerRemove records the order teardown does things in, so the test can
// assert the sequence rather than only the end state.
func stubDockerRemove(t *testing.T, order *[]string) {
	t.Helper()
	prev := sandboxRemoveFn
	sandboxRemoveFn = func(context.Context, string) error {
		*order = append(*order, "remove-container")
		return nil
	}
	t.Cleanup(func() { sandboxRemoveFn = prev })
}

// The ordering rule the whole lifecycle rests on. Harvesting after the line is
// gone loses the commits; deleting the tree while the container lives lets the
// container recreate it.
func TestTeardownSandbox_HarvestsBeforeRemovingAnything(t *testing.T) {
	d, paneID, repo := sandboxFixture(t)
	var order []string
	m, _ := d.sandboxMappingFor(paneID)

	// The stub records the state of the world AT the container removal, which
	// is what makes this test position-dependent. The first version only ever
	// appended "remove-container", so a mutation moving the removal AFTER the
	// tree delete passed — and that ordering lets a live container recreate
	// the tree, leaking it forever.
	prev := sandboxRemoveFn
	sandboxRemoveFn = func(context.Context, string) error {
		order = append(order, "remove-container")
		if _, err := os.Stat(m.HostPaneRoot); err == nil {
			order = append(order, "tree-still-present")
		}
		if _, err := os.Stat(sandbox.AlternatesPath(m)); err == nil {
			order = append(order, "alternates-still-present")
		}
		return nil
	}
	t.Cleanup(func() { sandboxRemoveFn = prev })

	if err := sandbox.AddAlternate(m); err != nil {
		t.Fatalf("AddAlternate: %v", err)
	}

	d.teardownSandbox(context.Background(), paneID)

	// Both must still exist when the container is removed: the tree is
	// deleted after, and the alternates line is removed after.
	if len(order) != 3 {
		t.Errorf("at container removal the world was %v — want the tree and the "+
			"alternates line both still present, i.e. removal happens BEFORE them", order)
	}

	// The object reached the repository.
	harvested := filepath.Join(repo, "objects", "ab",
		"0123456789abcdef0123456789abcdef012345")
	if _, err := os.Stat(harvested); err != nil {
		t.Errorf("object was not harvested before teardown deleted the store: %v", err)
	}
	// The line is gone.
	if _, err := os.Stat(sandbox.AlternatesPath(m)); !os.IsNotExist(err) {
		t.Errorf("alternates file survived teardown: %v", err)
	}
	// The pane's tree is gone.
	if _, err := os.Stat(m.HostPaneRoot); !os.IsNotExist(err) {
		t.Errorf("per-pane tree survived teardown: %v", err)
	}
	// And the container was removed exactly once.
	var removals int
	for _, s := range order {
		if s == "remove-container" {
			removals++
		}
	}
	if removals != 1 {
		t.Errorf("container removal happened %d times, want 1: %v", removals, order)
	}
}

// A pane that never got a registry entry — one whose prepare failed early —
// must still have any container it managed to start removed.
func TestTeardownSandbox_UnknownPaneStillRemovesTheContainer(t *testing.T) {
	d, _, _ := sandboxFixture(t)
	var order []string
	stubDockerRemove(t, &order)

	d.teardownSandbox(context.Background(), "never-prepared")

	if len(order) != 1 {
		t.Errorf("container removal ran %d times, want 1 — a leaked container is forever", len(order))
	}
}

// Teardown must leave no registry entry behind, or the repair pass keeps
// checking a repository for a pane that is gone.
func TestTeardownSandbox_DropsTheRegistryEntry(t *testing.T) {
	d, paneID, _ := sandboxFixture(t)
	stubDockerRemove(t, &[]string{})

	d.teardownSandbox(context.Background(), paneID)

	if _, ok := d.sandboxReg.get(paneID); ok {
		t.Error("registry entry survived teardown")
	}
}

// A dangling line makes EVERY git command in the repository fail. The way that
// happens is not a crash — reset-daemon wipes QUIL_HOME while the lines are
// still in place.
func TestRepairSandboxAlternates_StripsLinesWhoseStoreIsGone(t *testing.T) {
	d, paneID, repo := sandboxFixture(t)
	m, _ := d.sandboxMappingFor(paneID)
	if err := sandbox.AddAlternate(m); err != nil {
		t.Fatalf("AddAlternate: %v", err)
	}
	// Simulate the wipe: the store is gone, the line is not.
	if err := os.RemoveAll(m.HostPaneRoot); err != nil {
		t.Fatalf("rm: %v", err)
	}

	d.repairSandboxAlternates()

	path := filepath.Join(repo, "objects", "info", "alternates")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		body, _ := os.ReadFile(path)
		t.Errorf("stale line survived repair: %q", body)
	}
}

// A live pane's line must not be stripped by the repair pass.
func TestRepairSandboxAlternates_KeepsLiveLines(t *testing.T) {
	d, paneID, repo := sandboxFixture(t)
	m, _ := d.sandboxMappingFor(paneID)
	if err := sandbox.AddAlternate(m); err != nil {
		t.Fatalf("AddAlternate: %v", err)
	}

	d.repairSandboxAlternates()

	body, err := os.ReadFile(filepath.Join(repo, "objects", "info", "alternates"))
	if err != nil {
		t.Fatalf("a live pane's line was removed: %v", err)
	}
	if len(body) == 0 {
		t.Error("alternates file emptied while the pane is live")
	}
}

// Docker labels are engine-wide and the dev and production daemons share one
// engine, so a sweep must never reap a container belonging to another install.
func TestSweepSandboxContainers_OnlyReapsUnknownPanes(t *testing.T) {
	d, paneID, _ := sandboxFixture(t)

	prevList := sandboxListFn
	sandboxListFn = func(context.Context, string) ([]string, error) {
		return []string{paneID, "orphan"}, nil
	}
	t.Cleanup(func() { sandboxListFn = prevList })

	var removed []string
	prevRemove := sandboxRemoveFn
	sandboxRemoveFn = func(_ context.Context, id string) error {
		removed = append(removed, id)
		return nil
	}
	t.Cleanup(func() { sandboxRemoveFn = prevRemove })

	d.sweepSandboxContainers(context.Background(), map[string]bool{paneID: true})

	for _, id := range removed {
		if id == paneID {
			t.Fatal("the sweep reaped a LIVE pane's container")
		}
	}
	var sawOrphan bool
	for _, id := range removed {
		if id == "orphan" {
			sawOrphan = true
		}
	}
	if !sawOrphan {
		t.Error("the orphan was not reaped")
	}
}

// Docker being absent is the normal state on most machines. The sweep is
// housekeeping and must not take the daemon down with it.
func TestSweepSandboxContainers_SurvivesNoDocker(t *testing.T) {
	d, _, _ := sandboxFixture(t)
	prev := sandboxListFn
	sandboxListFn = func(context.Context, string) ([]string, error) {
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { sandboxListFn = prev })

	d.sweepSandboxContainers(context.Background(), nil)
}

// QuilDir returns $QUIL_HOME verbatim, so a trailing slash or a case
// difference between a TUI-spawned and a manually started daemon must not make
// a daemon miss its own containers.
func TestQuilHomeLabel_IsCanonical(t *testing.T) {
	a := quilHomeLabel("/home/u/.quil")
	b := quilHomeLabel("/home/u/.quil/")
	if a != b {
		t.Errorf("a trailing slash changes the label: %q vs %q", a, b)
	}
	if quilHomeLabel("/home/u/.quil") == quilHomeLabel("/home/u/other") {
		t.Error("two different data directories share a label")
	}
}

// The return value is the worktree-removal gate, so it has to be false exactly
// when a container may still be holding the bind mount. Both close paths force
// the worktree removal after three attempts rather than failing on a busy
// mount, so a wrong answer here deletes a directory a live agent is writing to.
func TestTeardownSandbox_ReportsWhetherTheContainerIsGone(t *testing.T) {
	t.Run("removal failed", func(t *testing.T) {
		d, paneID, _ := sandboxFixture(t)
		prev := sandboxRemoveFn
		sandboxRemoveFn = func(context.Context, string) error {
			return errors.New("docker: context deadline exceeded")
		}
		t.Cleanup(func() { sandboxRemoveFn = prev })

		if d.teardownSandbox(context.Background(), paneID) {
			t.Error("reported the container gone after `docker rm` failed — the caller would " +
				"force-remove the worktree out from under a live agent")
		}
		// And nothing below the removal ran, so the next start can retry.
		m, _ := d.sandboxMappingFor(paneID)
		if _, err := os.Stat(m.HostPaneRoot); err != nil {
			t.Errorf("the pane tree was deleted despite the failure: %v", err)
		}
	})

	t.Run("removal succeeded", func(t *testing.T) {
		d, paneID, _ := sandboxFixture(t)
		var order []string
		stubDockerRemove(t, &order)

		if !d.teardownSandbox(context.Background(), paneID) {
			t.Error("reported the container still present after a clean removal — worktrees " +
				"would never be removed again")
		}
	})

	// A pane with no registry entry never had a container recorded, so nothing
	// holds its worktree. This must stay TRUE even when the removal errors:
	// on a machine with no docker installed that call fails for every ordinary
	// pane, and gating on it would stop removing worktrees for users who never
	// opted into a sandbox.
	t.Run("unknown pane, docker unavailable", func(t *testing.T) {
		d, _, _ := sandboxFixture(t)
		prev := sandboxRemoveFn
		sandboxRemoveFn = func(context.Context, string) error {
			return errors.New(`exec: "docker": executable file not found in $PATH`)
		}
		t.Cleanup(func() { sandboxRemoveFn = prev })

		if !d.teardownSandbox(context.Background(), "never-prepared") {
			t.Error("a pane that never had a container blocked its worktree removal — " +
				"this fires for every ordinary pane on a machine without docker")
		}
	})
}
