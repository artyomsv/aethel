package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// stubGit points runGit at a canned rev-parse answer, so every mapping case is
// reachable with no repository and no git binary. Restores on cleanup.
func stubGit(t *testing.T, top, common, gitDir string) {
	t.Helper()
	prev := runGit
	runGit = func(context.Context, string, ...string) (string, error) {
		return top + "\n" + common + "\n" + gitDir + "\n", nil
	}
	t.Cleanup(func() { runGit = prev })
}

// stubRealPath makes path resolution the identity, so the refusal tests are
// about the containment logic rather than about what exists on the CI runner's
// disk. The junction case gets its own stub below.
func stubRealPath(t *testing.T) {
	t.Helper()
	prev := evalSymlinksFn
	evalSymlinksFn = func(p string) (string, error) { return p, nil }
	t.Cleanup(func() { evalSymlinksFn = prev })
}

func TestNewMapping_LinkedWorktree(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/quil-worktrees/feat-x", "/projects/quil/.git",
		"/projects/quil/.git/worktrees/feat-x")

	m, err := NewMapping(context.Background(), "/home/u/.quil",
		"/projects/quil-worktrees/feat-x", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	if m.Kind != KindWorktree {
		t.Errorf("Kind = %v, want KindWorktree", m.Kind)
	}
	if m.AdminName != "feat-x" {
		t.Errorf("AdminName = %q, want feat-x", m.AdminName)
	}
	if m.HostSubdir != "" {
		t.Errorf("HostSubdir = %q, want empty at the toplevel", m.HostSubdir)
	}
	if got := m.ContainerAlternate(); got != "/repo/.git/objects" {
		t.Errorf("ContainerAlternate = %q, want the shared store under /repo", got)
	}
}

// An ordinary checkout has no separate admin directory, so its git dir and its
// common dir are the same path. Getting this wrong produces an admin mount
// pointing at the whole .git.
func TestNewMapping_OrdinaryCheckout(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/plain", "/projects/plain/.git", "/projects/plain/.git")

	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/plain", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	if m.Kind != KindCheckout {
		t.Fatalf("Kind = %v, want KindCheckout", m.Kind)
	}
	if m.HostAdminDir != "" || m.AdminName != "" {
		t.Errorf("admin dir set for an ordinary checkout: %q %q", m.HostAdminDir, m.AdminName)
	}
	// Its .git rides inside the mounted working tree, so the alternate is
	// under /work, not /repo.
	if got := m.ContainerAlternate(); !strings.HasPrefix(got, ContainerWorkRoot+"/") {
		t.Errorf("ContainerAlternate = %q, want it under %s", got, ContainerWorkRoot)
	}
	if DotGitOverlay(m) != "" || AdminGitdirOverlay(m) != "" {
		t.Error("an ordinary checkout must have no overlays: its .git is a real directory")
	}
}

// A pane created after browsing into a subdirectory must still mount the
// REPOSITORY and merely start in the subdirectory. Mounting the subdirectory
// alone answers "fatal: not a git repository" inside the container.
func TestNewMapping_SubdirectoryMountsTheToplevel(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")

	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt/src/api", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	if m.HostWorktree != "/projects/wt" {
		t.Errorf("HostWorktree = %q, want the toplevel", m.HostWorktree)
	}
	if m.HostSubdir != "src/api" {
		t.Errorf("HostSubdir = %q, want src/api", m.HostSubdir)
	}
	if got, want := m.ContainerCWD(), m.ContainerWorkdir()+"/src/api"; got != want {
		t.Errorf("ContainerCWD = %q, want %q", got, want)
	}
}

// The refusal that matters most: the dev build puts QUIL_HOME inside the
// checkout, so sandboxing a quil worktree would mount every pane's settings
// file and the daemon socket into the container.
func TestNewMapping_RefusesQuilHomeInsideTheWorktree(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/quil", "/projects/quil/.git", "/projects/quil/.git")

	_, err := NewMapping(context.Background(), "/projects/quil/.quil", "/projects/quil", "pane1")
	if !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping", err)
	}
	if !strings.Contains(err.Error(), "QUIL_HOME") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

func TestNewMapping_RefusesQuilHomeInsideTheGitDir(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")

	_, err := NewMapping(context.Background(), "/projects/main/.git/quil", "/projects/wt", "pane1")
	if !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping", err)
	}
}

// A cleaned absolute path still traverses links, so the containment test must
// run on RESOLVED paths. This is the Windows junction case, which no amount of
// string cleaning catches.
func TestNewMapping_RefusalResolvesSymlinks(t *testing.T) {
	stubGit(t, "/projects/wt", "/projects/wt/.git", "/projects/wt/.git")

	// QUIL_HOME is spelled through a junction that lands INSIDE the worktree.
	// A prefix test on the unresolved paths answers "not inside" and allows
	// the mount; only the resolved paths show the exposure.
	prev := evalSymlinksFn
	evalSymlinksFn = func(p string) (string, error) {
		s := filepath.ToSlash(p)
		if strings.HasPrefix(s, "/link/.quil") {
			return "/projects/wt/.quil" + strings.TrimPrefix(s, "/link/.quil"), nil
		}
		return s, nil
	}
	t.Cleanup(func() { evalSymlinksFn = prev })

	if _, err := NewMapping(context.Background(), "/link/.quil", "/projects/wt", "pane1"); !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping — a junction defeated the containment test", err)
	}
}

// Failing to resolve a path is a refusal, not a reason to proceed: the whole
// point of the check is knowing where a directory really is.
func TestNewMapping_RefusesWhenPathsCannotBeResolved(t *testing.T) {
	stubGit(t, "/projects/wt", "/projects/wt/.git", "/projects/wt/.git")

	prev := evalSymlinksFn
	evalSymlinksFn = func(string) (string, error) { return "", errors.New("no such file") }
	t.Cleanup(func() { evalSymlinksFn = prev })

	if _, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt", "p"); !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping", err)
	}
}

func TestNewMapping_NotARepository(t *testing.T) {
	stubRealPath(t)
	prev := runGit
	runGit = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("exit status 128")
	}
	t.Cleanup(func() { runGit = prev })

	if _, err := NewMapping(context.Background(), "/home/u/.quil", "/tmp/nope", "p"); !errors.Is(err, ErrNotARepository) {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

// A bare repository has no working tree to mount.
func TestNewMapping_RefusesBareRepository(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "", "/projects/bare.git", "/projects/bare.git")

	if _, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/bare.git", "p"); !errors.Is(err, ErrNotARepository) {
		t.Fatalf("err = %v, want ErrNotARepository", err)
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string // prefix before the digest, "" when only a digest is expected
	}{
		{"plain", "/projects/feat-x", "feat-x-"},
		// The slug reaches the container side of a colon-delimited -v, so a
		// colon in the name would make docker read part of it as a mode.
		{"colon collapses", "/projects/feat:ro", "feat-ro-"},
		{"dot collapses so .. is unreachable", "/projects/..", ""},
		{"spaces collapse", "/projects/my project", "my-project-"},
		{"drive root has no basename", "/", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slug(tc.path)
			if strings.ContainsAny(got, `:/\ .`) {
				t.Fatalf("slug %q contains a character that breaks -v or -w", got)
			}
			if tc.want == "" {
				if len(got) != slugDigestLen {
					t.Errorf("slug = %q, want a bare %d-char digest", got, slugDigestLen)
				}
				return
			}
			if !strings.HasPrefix(got, tc.want) {
				t.Errorf("slug = %q, want prefix %q", got, tc.want)
			}
		})
	}
}

// Two panes on ONE worktree must share a slug. The digest covers the host path
// and nothing else: adding the pane id would give every new pane a fresh
// Claude project directory and leave its resume picker permanently empty.
func TestSlug_StableAcrossPanes(t *testing.T) {
	if a, b := slug("/projects/wt"), slug("/projects/wt"); a != b {
		t.Errorf("slug is not stable for one path: %q vs %q", a, b)
	}
	if a, b := slug("/projects/a/wt"), slug("/projects/b/wt"); a == b {
		t.Errorf("two different repositories share slug %q", a)
	}
}

func TestOverlays(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")
	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}

	// The worktree's .git must point where the admin directory is READABLE in
	// the container, not where it lives on the host.
	if got, want := DotGitOverlay(m), "gitdir: /repo/.git/worktrees/wt\n"; got != want {
		t.Errorf("DotGitOverlay = %q, want %q", got, want)
	}
	// And the main repository's admin file must point back at the container's
	// view of the worktree, or `git worktree prune` inside the container
	// deletes the host's registration.
	if got, want := AdminGitdirOverlay(m), m.ContainerWorkdir()+"/.git\n"; got != want {
		t.Errorf("AdminGitdirOverlay = %q, want %q", got, want)
	}
	for _, s := range []string{DotGitOverlay(m), AdminGitdirOverlay(m)} {
		if strings.Contains(s, `\`) {
			t.Errorf("overlay %q has a backslash: the container cannot resolve it", s)
		}
	}
}

// The overlays must NOT live anywhere that is mounted as a directory. A
// read-only flag is per mount point, so an overlay reachable read-write
// elsewhere is rewritable by the agent — and rewriting admin-gitdir re-opens
// the prune bug that destroys the host worktree.
func TestOverlayDirIsNotInsideAnyMountedDirectory(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")
	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	for _, mt := range Mounts(m) {
		if mt.host == m.HostDotGitOverlay() || mt.host == m.HostAdminGitdirOverlay() {
			continue // the two file mounts themselves
		}
		if pathWithin(mt.host, m.HostOverlayDir) {
			t.Errorf("overlay dir %s is inside mounted directory %s — the agent can rewrite it",
				m.HostOverlayDir, mt.host)
		}
	}
}

// A comma anywhere in the mount set must be refused, and the check is derived
// from Mounts(m) precisely so a path nobody remembered is still covered. The
// first version hand-listed four paths and missed HostAdminDir — reachable
// through a linked worktree whose NAME contains a comma.
func TestNewMapping_RefusesACommaInAnyMountSource(t *testing.T) {
	stubRealPath(t)
	for _, tc := range []struct {
		name             string
		top, common, git string
	}{
		{"worktree path", "/projects/a,b/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt"},
		{"admin dir name", "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/w,t"},
		{"repository .git", "/projects/wt", "/projects/m,ain/.git", "/projects/m,ain/.git/worktrees/wt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubGit(t, tc.top, tc.common, tc.git)
			_, err := NewMapping(context.Background(), "/home/u/.quil", tc.top, "p1")
			if !errors.Is(err, ErrUnsafeMapping) {
				t.Fatalf("err = %v, want ErrUnsafeMapping — docker would mis-split this --mount source", err)
			}
		})
	}
}

// And the quil-side paths, which every pane shares.
func TestNewMapping_RefusesACommaInQuilHome(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")
	if _, err := NewMapping(context.Background(), "/home/u/qu,il", "/projects/wt", "p1"); !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping", err)
	}
}

// The fields the daemon fills in AFTER NewMapping returns — the hook binary
// and the shared config directory — are mount sources too, so Validate has to
// be callable again once they are set. A check that only ever ran inside
// NewMapping could not see either.
func TestValidate_CatchesLateFields(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")
	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt", "p1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a clean mapping was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		set  func(*Mapping)
	}{
		{"hook binary", func(mm *Mapping) { mm.HostQuild = "/opt/qu,il/quild" }},
		{"shared claude root", func(mm *Mapping) { mm.SharedClaudeRoot = "/home/u/.qu,il/sandbox/claude" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			late := m
			tc.set(&late)
			if err := late.Validate(); !errors.Is(err, ErrUnsafeMapping) {
				t.Errorf("err = %v, want ErrUnsafeMapping — this path reaches docker as a --mount source", err)
			}
		})
	}
}

// stubModulesExist makes NewMapping believe the repository has a .git/modules,
// which is the one filesystem question it asks that git does not answer.
func stubModulesExist(t *testing.T, exists bool) {
	t.Helper()
	prev := dirExistsFn
	dirExistsFn = func(string) bool { return exists }
	t.Cleanup(func() { dirExistsFn = prev })
}

// .git/modules is found with a stat, and a stat FOLLOWS links — so a symlink
// or junction there makes an unrelated host directory the bind SOURCE for
// /repo/.git/modules, mounting it into the container. Read-only, but read-only
// host data the boundary exists to exclude.
func TestNewMapping_RefusesGitModulesLinkedOutsideTheRepository(t *testing.T) {
	stubGit(t, "/projects/repo", "/projects/repo/.git", "/projects/repo/.git")
	stubModulesExist(t, true)

	prev := evalSymlinksFn
	evalSymlinksFn = func(p string) (string, error) {
		s := filepath.ToSlash(p)
		if s == "/projects/repo/.git/modules" {
			return "/home/u/secrets", nil
		}
		return s, nil
	}
	t.Cleanup(func() { evalSymlinksFn = prev })

	_, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/repo", "pane1")
	if !errors.Is(err, ErrUnsafeMapping) {
		t.Fatalf("err = %v, want ErrUnsafeMapping — the link's target would be mounted", err)
	}
	if !strings.Contains(err.Error(), "modules") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

// The ordinary case must still work, or the refusal above is indistinguishable
// from "submodule repositories never map".
func TestNewMapping_AcceptsGitModulesInsideTheRepository(t *testing.T) {
	stubRealPath(t)
	stubModulesExist(t, true)
	stubGit(t, "/projects/repo", "/projects/repo/.git", "/projects/repo/.git")

	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/repo", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	if m.HostGitModules != filepath.ToSlash(filepath.Join("/projects/repo/.git", "modules")) &&
		filepath.ToSlash(m.HostGitModules) != "/projects/repo/.git/modules" {
		t.Errorf("HostGitModules = %q, want the repository's own modules directory", m.HostGitModules)
	}
}
