package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sandbox"
)

// The lifecycle half: what has to happen, and in what order, around a sandbox
// pane's container.
//
// One ordering rule governs everything here and it is not negotiable:
//
//	harvest objects → kill the container → remove the alternates line →
//	remove the per-pane tree → remove the worktree
//
// Every step depends on the one before it. Harvesting after the line is gone
// loses the commits; removing the line before harvesting loses them too;
// deleting the tree while the container lives lets the container recreate it
// (leaking it forever, and failing outright on Windows); and removing the
// worktree while the container holds the bind mount fails, with only three
// retries at 250ms to spare.

// The docker seams. Package vars so the lifecycle tests — which are about
// ORDERING, and about never reaping another install's container — need no
// Docker daemon. `dev.sh test` runs inside a container that has none.
var (
	sandboxRemoveFn = sandbox.RemoveForce
	sandboxKillFn   = sandbox.Kill
	sandboxListFn   = sandbox.ListPaneIDs
)

// sandboxRegistryName is the file recording which repository each sandbox pane
// wrote an alternates line into.
//
// It exists because teardown and the startup repair both need a fact that is
// not derivable from a pane id: WHICH repository holds the line. A stale line
// makes every git command in that repository fail, so knowing where to look is
// the difference between a repair and a user editing a file by hand.
const sandboxRegistryName = "alternates.json"

// sandboxRegistry maps pane id to the repository .git that holds its line.
type sandboxRegistry struct {
	mu      sync.Mutex
	path    string
	Entries map[string]string `json:"entries"`
}

func newSandboxRegistry(quilDir string) *sandboxRegistry {
	return &sandboxRegistry{
		path:    filepath.Join(sandboxRoot(quilDir), sandboxRegistryName),
		Entries: map[string]string{},
	}
}

func (r *sandboxRegistry) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := os.ReadFile(r.path)
	if err != nil {
		return // absent is the normal first-run state
	}
	var on struct {
		Entries map[string]string `json:"entries"`
	}
	if err := json.Unmarshal(b, &on); err != nil {
		log.Printf("sandbox: registry unreadable (%v); starting empty", err)
		return
	}
	if on.Entries != nil {
		r.Entries = on.Entries
	}
}

func (r *sandboxRegistry) save() {
	// Caller holds r.mu.
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		log.Printf("sandbox: registry dir: %v", err)
		return
	}
	b, err := json.MarshalIndent(struct {
		Entries map[string]string `json:"entries"`
	}{r.Entries}, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("sandbox: registry write: %v", err)
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		log.Printf("sandbox: registry rename: %v", err)
	}
}

func (r *sandboxRegistry) put(paneID, gitCommon string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Entries[paneID] = gitCommon
	r.save()
}

func (r *sandboxRegistry) get(paneID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.Entries[paneID]
	return v, ok
}

func (r *sandboxRegistry) drop(paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Entries, paneID)
	r.save()
}

// repositories returns every distinct .git the registry knows about.
func (r *sandboxRegistry) repositories() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, g := range r.Entries {
		seen[g] = true
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// mappingFor rebuilds enough of a mapping to tear a pane down.
//
// Teardown cannot re-derive one from the checkout: by the time it runs, the
// worktree may already be gone, and asking git about a deleted directory
// answers nothing. Everything needed — the pane's own tree and the repository
// that holds its line — is either derivable from the pane id or recorded in
// the registry at prepare time.
func (d *Daemon) sandboxMappingFor(paneID string) (sandbox.Mapping, bool) {
	gitCommon, ok := d.sandboxReg.get(paneID)
	if !ok {
		return sandbox.Mapping{}, false
	}
	quilDir := config.QuilDir()
	return sandbox.Mapping{
		PaneID:         paneID,
		HostGitCommon:  gitCommon,
		HostPaneRoot:   filepath.Join(sandboxRoot(quilDir), "panes", paneID),
		HostOverlayDir: filepath.Join(sandboxRoot(quilDir), "overlays", paneID),
		HostEmptyDir:   sandboxEmptyDir(quilDir),
	}, true
}

// harvestSandbox copies a pane's new git objects into its repository.
//
// Called on a timer as well as at teardown, and that is deliberate: a daemon
// that dies between two harvests should strand seconds of work, not a
// session's. The copy is cheap — only the objects this pane has written since
// the last pass — and idempotent, so a redundant call costs a readdir.
func (d *Daemon) harvestSandbox(paneID string) {
	m, ok := d.sandboxMappingFor(paneID)
	if !ok {
		return
	}
	n, err := sandbox.HarvestObjects(m)
	if err != nil {
		log.Printf("sandbox: pane %s: harvest: %v", paneID, err)
		return
	}
	if n > 0 {
		log.Printf("sandbox: pane %s: harvested %d objects", paneID, n)
	}
}

// teardownSandbox runs the ordered teardown for one pane.
//
// Returns only when the container is gone, because the caller must not remove
// the worktree before then — a live container holds the bind mount, and
// removeOwnedWorktrees gives up after three attempts at 250ms.
func (d *Daemon) teardownSandbox(ctx context.Context, paneID string) {
	m, ok := d.sandboxMappingFor(paneID)
	if !ok {
		// No registry entry: either not a sandbox pane, or one whose prepare
		// failed before it recorded anything. Removing any container it may
		// still have is cheap and idempotent.
		if err := sandboxRemoveFn(ctx, paneID); err != nil {
			log.Printf("sandbox: pane %s: remove container: %v", paneID, err)
		}
		return
	}

	// 1. Harvest FIRST. After this the objects live in the repository and the
	//    line is redundant; before it, removing the line loses the commits.
	if n, err := sandbox.HarvestObjects(m); err != nil {
		log.Printf("sandbox: pane %s: final harvest: %v", paneID, err)
	} else if n > 0 {
		log.Printf("sandbox: pane %s: harvested %d objects at teardown", paneID, n)
	}

	// 2. Kill the container. Everything below deletes files it may be holding.
	if err := sandboxRemoveFn(ctx, paneID); err != nil {
		log.Printf("sandbox: pane %s: remove container: %v", paneID, err)
	}

	// 3. Remove the line — and ONLY this pane's line. Another sandbox pane on
	//    the same repository has one too, as may the user.
	if err := sandbox.RemoveAlternate(m); err != nil {
		log.Printf("sandbox: pane %s: remove alternates line: %v", paneID, err)
	}

	// 4. Only now is the pane's tree safe to delete.
	for _, dir := range []string{m.HostPaneRoot, m.HostOverlayDir} {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("sandbox: pane %s: remove %s: %v", paneID, dir, err)
		}
	}
	d.sandboxReg.drop(paneID)
	// Drop the forwarder's read offset with the tree it points into, or a
	// later pane that draws the same id resumes from a stranger's position
	// and silently forwards nothing until it catches up.
	if d.spoolFwd != nil {
		d.spoolFwd.forget(paneID)
	}
}

// sweepSandboxContainers removes containers belonging to THIS daemon whose
// pane no longer exists.
//
// Scoped by both labels, always. Docker labels are engine-wide and the dev and
// production daemons share one engine, so a sweep on quil.pane alone would
// have a dev daemon classify every production sandbox container as an orphan
// and force-remove it — killing the owner's live agents, and breaking the
// project's one always-on isolation rule.
func (d *Daemon) sweepSandboxContainers(ctx context.Context, live map[string]bool) {
	ids, err := sandboxListFn(ctx, quilHomeLabel(config.QuilDir()))
	if err != nil {
		// Docker being absent is the normal case on most machines, and the
		// sweep is housekeeping. Log at a level that does not imply breakage.
		log.Printf("sandbox: sweep skipped: %v", err)
		return
	}
	for _, id := range ids {
		if live[id] {
			continue
		}
		log.Printf("sandbox: reaping orphaned container for pane %s", id)
		if err := sandboxRemoveFn(ctx, id); err != nil {
			log.Printf("sandbox: reap %s: %v", id, err)
		}
		d.teardownSandbox(ctx, id)
	}
}

// repairSandboxAlternates strips alternates lines pointing at sandbox object
// stores that no longer exist.
//
// A dangling line makes EVERY git command in that repository fail, and the way
// it happens in practice is not a crash: reset-daemon wipes $QUIL_HOME while
// the lines are still in place. Best-effort by nature — the registry that says
// which repositories to check lives in $QUIL_HOME too, so a wipe defeats it
// and the docs carry the one-line manual fix.
func (d *Daemon) repairSandboxAlternates() {
	root := sandboxRoot(config.QuilDir())
	for _, gitCommon := range d.sandboxReg.repositories() {
		path := filepath.Join(gitCommon, "objects", "info", "alternates")
		n, err := sandbox.PruneAlternates(path, root)
		if err != nil {
			log.Printf("sandbox: repair %s: %v", path, err)
			continue
		}
		if n > 0 {
			log.Printf("sandbox: removed %d stale alternates line(s) from %s", n, path)
			d.emitEvent(PaneEvent{
				Type:     "sandbox_repaired",
				Title:    "Repaired a git repository",
				Message:  fmt.Sprintf("Removed %d stale sandbox object-store reference(s) from %s", n, gitCommon),
				Severity: "info",
			})
		}
	}
}

// killSandboxContainers stops every live sandbox container at daemon shutdown.
//
// `docker kill`, in PARALLEL, under one short deadline. Not `docker stop`: its
// grace is ten seconds per container and claude as PID 1 ignores SIGTERM (the
// kernel applies no default action to PID 1), while the daemon's whole
// shutdown budget is five seconds before it is SIGKILLed itself. Two sandbox
// panes would need twenty seconds, get none, and leave both containers
// orphaned plus a pid file behind — turning every restart into the crash path.
//
// The conversation survives regardless: it is in the pane's own config
// directory, and the harvest has already run.
func (d *Daemon) killSandboxContainers(paneIDs []string) {
	if len(paneIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sandboxShutdownBudget)
	defer cancel()

	var wg sync.WaitGroup
	for _, id := range paneIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := sandboxKillFn(ctx, id); err != nil {
				log.Printf("sandbox: kill %s: %v", id, err)
			}
		}(id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("sandbox: shutdown kill did not finish inside %s; the startup sweep will reap the rest",
			sandboxShutdownBudget)
	}
}

// sandboxStartupHousekeeping repairs and reaps what a previous daemon left.
//
// Both halves exist for the crash case: a clean shutdown kills its containers
// and a clean close tears its panes down, so anything found here outlived a
// daemon that did not get to finish. Repair runs first because the sweep's
// teardown writes to the same alternates files.
func (d *Daemon) sandboxStartupHousekeeping() {
	d.repairSandboxAlternates()

	live := map[string]bool{}
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			live[pane.ID] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.sweepSandboxContainers(ctx, live)
}

// harvestLoop copies every live sandbox pane's new git objects into its
// repository on a timer.
//
// Without it a daemon that dies mid-session strands everything committed since
// the pane opened: the objects are in the pane's own store, and the alternates
// line that made them reachable is removed by whichever cleanup runs next.
// With it, the loss is bounded by one interval.
func (d *Daemon) harvestLoop() {
	t := time.NewTicker(sandboxHarvestInterval)
	defer t.Stop()
	for {
		select {
		case <-d.shutdown:
			return
		case <-t.C:
			for _, id := range d.sandboxPaneIDs() {
				d.harvestSandbox(id)
			}
		}
	}
}

// sandboxPaneIDs lists the live panes that have a sandbox container.
func (d *Daemon) sandboxPaneIDs() []string {
	var out []string
	for _, tab := range d.session.Tabs() {
		for _, pane := range d.session.Panes(tab.ID) {
			pane.PluginMu.Lock()
			sandboxed := pane.SandboxImage != ""
			pane.PluginMu.Unlock()
			if sandboxed {
				out = append(out, pane.ID)
			}
		}
	}
	return out
}

// sandboxShutdownBudget is the whole time shutdown may spend on containers,
// for any number of them. Small because it is spent inside a five-second
// window the daemon does not control.
const sandboxShutdownBudget = 2 * time.Second

// sandboxHarvestInterval is how often a live pane's objects are copied into
// its repository.
//
// Frequent enough that a crash strands seconds of work rather than a session's,
// and cheap enough to ignore: the pass copies only objects written since the
// last one, and a pane that has committed nothing costs one readdir.
const sandboxHarvestInterval = 30 * time.Second
