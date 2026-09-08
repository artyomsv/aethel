package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// objectsFixture builds a mapping over real temp directories, so the harvest
// and alternates tests exercise actual file operations. No git binary and no
// container are involved: everything under test is file arithmetic.
func objectsFixture(t *testing.T) Mapping {
	t.Helper()
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	pane := filepath.Join(root, "quil", "sandbox", "panes", "p1")
	if err := os.MkdirAll(filepath.Join(common, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pane, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return Mapping{
		PaneID:        "p1",
		Kind:          KindWorktree,
		HostWorktree:  filepath.Join(root, "wt"),
		HostGitCommon: common,
		HostPaneRoot:  pane,
		Slug:          "wt-abcd1234",
	}
}

// writeObj creates a loose object with a well-formed name.
func writeObj(t *testing.T, store, shard, name, body string) {
	t.Helper()
	dir := filepath.Join(store, shard)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o444); err != nil {
		t.Fatalf("write: %v", err)
	}
}

const objName = "0123456789abcdef0123456789abcdef012345" // 38 hex chars

func TestHarvestObjects_CopiesLooseObjectsAndPacks(t *testing.T) {
	m := objectsFixture(t)
	writeObj(t, m.HostObjects(), "ab", objName, "blob")
	packDir := filepath.Join(m.HostObjects(), "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, n := range []string{"pack-deadbeef.pack", "pack-deadbeef.idx", "pack-deadbeef.rev"} {
		if err := os.WriteFile(filepath.Join(packDir, n), []byte("p"), 0o444); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	n, err := HarvestObjects(m)
	if err != nil {
		t.Fatalf("HarvestObjects: %v", err)
	}
	if n != 4 {
		t.Errorf("copied %d, want 4 (one loose object + three pack files)", n)
	}
	dst := filepath.Join(m.HostGitCommon, "objects")
	if _, err := os.Stat(filepath.Join(dst, "ab", objName)); err != nil {
		t.Errorf("loose object not harvested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "pack", "pack-deadbeef.pack")); err != nil {
		t.Errorf("pack not harvested: %v", err)
	}
}

// The finding that would have silently corrupted the user's repository: the
// agent controls its store's info/ directory, git reads info/commit-graph as
// trusted ancestry and info/alternates transitively, and a plain copy of the
// store drags both into the real object store.
func TestHarvestObjects_NeverCopiesInfo(t *testing.T) {
	m := objectsFixture(t)
	writeObj(t, m.HostObjects(), "ab", objName, "blob")

	info := filepath.Join(m.HostObjects(), "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, n := range []string{"alternates", "commit-graph", "packs"} {
		if err := os.WriteFile(filepath.Join(info, n), []byte("poison"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if _, err := HarvestObjects(m); err != nil {
		t.Fatalf("HarvestObjects: %v", err)
	}
	dstInfo := filepath.Join(m.HostGitCommon, "objects", "info")
	if entries, err := os.ReadDir(dstInfo); err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("harvest copied agent-controlled info/ into the user's object store: %v", names)
	}
}

// Git writes objects atomically (temp + rename), so a well-formed name is
// always a complete file. Anything else in the store is in-flight or junk.
func TestHarvestObjects_SkipsMalformedNames(t *testing.T) {
	m := objectsFixture(t)
	writeObj(t, m.HostObjects(), "ab", "tmp_obj_XyZ", "partial")
	writeObj(t, m.HostObjects(), "ab", "nothex-------------------------------", "junk")
	writeObj(t, m.HostObjects(), "zz", objName, "wrong shard")

	n, err := HarvestObjects(m)
	if err != nil {
		t.Fatalf("HarvestObjects: %v", err)
	}
	if n != 0 {
		t.Errorf("copied %d, want 0 — none of these are well-formed objects", n)
	}
}

// Object names are content addresses, so a name already present is the same
// bytes. Rewriting it would be pure risk.
func TestHarvestObjects_DoesNotClobber(t *testing.T) {
	m := objectsFixture(t)
	writeObj(t, m.HostObjects(), "ab", objName, "new")
	writeObj(t, filepath.Join(m.HostGitCommon, "objects"), "ab", objName, "existing")

	n, err := HarvestObjects(m)
	if err != nil {
		t.Fatalf("HarvestObjects: %v", err)
	}
	if n != 0 {
		t.Errorf("copied %d, want 0", n)
	}
	got, err := os.ReadFile(filepath.Join(m.HostGitCommon, "objects", "ab", objName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "existing" {
		t.Errorf("existing object was overwritten: %q", got)
	}
}

// Called on a 30-second timer, so it must be safe to run repeatedly.
func TestHarvestObjects_Idempotent(t *testing.T) {
	m := objectsFixture(t)
	writeObj(t, m.HostObjects(), "ab", objName, "blob")

	first, err := HarvestObjects(m)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := HarvestObjects(m)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != 1 || second != 0 {
		t.Errorf("copied %d then %d, want 1 then 0", first, second)
	}
}

func TestHarvestObjects_MissingStoreIsNotAnError(t *testing.T) {
	m := objectsFixture(t)
	if err := os.RemoveAll(m.HostObjects()); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if n, err := HarvestObjects(m); err != nil || n != 0 {
		t.Errorf("HarvestObjects = %d, %v; want 0, nil for a pane that never committed", n, err)
	}
}

// An MSYS-style "/c/..." path makes git answer "unable to normalize alternate
// object path" on every command in the repository — which looks exactly like
// the corruption the line exists to prevent.
func TestAlternatesLine_IsANativePath(t *testing.T) {
	m := objectsFixture(t)
	line := AlternatesLine(m)
	if strings.HasPrefix(line, "/c/") || strings.HasPrefix(line, "/e/") {
		t.Errorf("AlternatesLine = %q, want a native host path", line)
	}
	if !strings.HasSuffix(filepath.ToSlash(line), "/objects") {
		t.Errorf("AlternatesLine = %q, want the pane's object store", line)
	}
}

func TestAddAlternate_CreatesAndIsIdempotent(t *testing.T) {
	m := objectsFixture(t)
	for i := 0; i < 3; i++ {
		if err := AddAlternate(m); err != nil {
			t.Fatalf("AddAlternate: %v", err)
		}
	}
	lines, err := readAlternates(AlternatesPath(m))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(lines) != 1 {
		t.Errorf("alternates has %d lines, want 1 — a restart must not accumulate duplicates", len(lines))
	}
}

// Two sandbox panes on one repository write two lines, and the user may have
// written one of their own. Rewriting the file wholesale would break the other
// pane's repository.
func TestRemoveAlternate_RemovesOnlyItsOwnLine(t *testing.T) {
	m := objectsFixture(t)
	other := m
	other.PaneID = "p2"
	other.HostPaneRoot = filepath.Join(filepath.Dir(m.HostPaneRoot), "p2")
	userLine := filepath.Join(t.TempDir(), "user-store")

	if err := AddAlternate(m); err != nil {
		t.Fatalf("add p1: %v", err)
	}
	if err := AddAlternate(other); err != nil {
		t.Fatalf("add p2: %v", err)
	}
	// A line the user put there themselves.
	lines, _ := readAlternates(AlternatesPath(m))
	if err := writeAlternates(AlternatesPath(m), append(lines, userLine)); err != nil {
		t.Fatalf("seed user line: %v", err)
	}

	if err := RemoveAlternate(m); err != nil {
		t.Fatalf("RemoveAlternate: %v", err)
	}
	got, err := readAlternates(AlternatesPath(m))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("alternates has %d lines, want 2: %v", len(got), got)
	}
	for _, ln := range got {
		if sameHostPath(ln, AlternatesLine(m)) {
			t.Error("this pane's own line survived")
		}
	}
	var sawOther, sawUser bool
	for _, ln := range got {
		if sameHostPath(ln, AlternatesLine(other)) {
			sawOther = true
		}
		if sameHostPath(ln, userLine) {
			sawUser = true
		}
	}
	if !sawOther {
		t.Error("another pane's line was removed — its repository is now broken")
	}
	if !sawUser {
		t.Error("the user's own line was removed")
	}
}

// A last line removed should take the file with it, leaving no trace in a
// repository that had none.
func TestRemoveAlternate_DeletesAnEmptiedFile(t *testing.T) {
	m := objectsFixture(t)
	if err := AddAlternate(m); err != nil {
		t.Fatalf("AddAlternate: %v", err)
	}
	if err := RemoveAlternate(m); err != nil {
		t.Fatalf("RemoveAlternate: %v", err)
	}
	if _, err := os.Stat(AlternatesPath(m)); !os.IsNotExist(err) {
		t.Errorf("alternates file survived with no lines: %v", err)
	}
}

func TestRemoveAlternate_MissingFileIsNotAnError(t *testing.T) {
	m := objectsFixture(t)
	if err := RemoveAlternate(m); err != nil {
		t.Errorf("RemoveAlternate on a repository with no alternates: %v", err)
	}
}

// A line pointing at a deleted sandbox store makes EVERY git command in the
// repository print an error. The way that happens in practice is reset-daemon
// wiping QUIL_HOME while the lines still exist.
func TestPruneAlternates_RemovesOnlyDeadSandboxLines(t *testing.T) {
	m := objectsFixture(t)
	sandboxRoot := filepath.Join(filepath.Dir(filepath.Dir(m.HostPaneRoot)))

	live := m.HostObjects() // exists
	dead := filepath.Join(sandboxRoot, "panes", "gone", "objects")
	outside := filepath.Join(t.TempDir(), "someone-elses-store")

	if err := writeAlternates(AlternatesPath(m), []string{live, dead, outside}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := PruneAlternates(AlternatesPath(m), sandboxRoot)
	if err != nil {
		t.Fatalf("PruneAlternates: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	got, _ := readAlternates(AlternatesPath(m))
	if len(got) != 2 {
		t.Fatalf("kept %d lines, want 2: %v", len(got), got)
	}
	for _, ln := range got {
		if sameHostPath(ln, dead) {
			t.Error("the dead sandbox line survived")
		}
	}
	// A dangling line that is NOT ours is not ours to remove: it may be
	// pointing at a network store that is merely offline.
	var sawOutside bool
	for _, ln := range got {
		if sameHostPath(ln, outside) {
			sawOutside = true
		}
	}
	if !sawOutside {
		t.Error("a non-sandbox line was pruned")
	}
}
