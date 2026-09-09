package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

func stubWorktreeBranches(t *testing.T, out []gitworktree.Branch, truncated bool, err error) *int {
	t.Helper()
	var calls int
	prev := worktreeBranchesFn
	worktreeBranchesFn = func(ctx context.Context, dir string) ([]gitworktree.Branch, bool, error) {
		calls++
		return out, truncated, err
	}
	t.Cleanup(func() { worktreeBranchesFn = prev })
	return &calls
}

// named builds a dateless branch listing, for tests that care about names only.
func named(names ...string) []gitworktree.Branch {
	out := make([]gitworktree.Branch, 0, len(names))
	for _, n := range names {
		out = append(out, gitworktree.Branch{Name: n})
	}
	return out
}

// Each worktree carries its branch's last commit time, joined from the branch
// listing, so the dialog can put the recently worked-on ones first. Joined
// HERE rather than sent as a second list for the client to match, because the
// join key — the branch name — is git's spelling on both sides only on this
// machine. A detached checkout has no branch to look up and stays at zero.
func TestWorktreeListResponse_CarriesEachWorktreesCommitTime(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
		{Path: "/projects/quil-worktrees/feat-a", Branch: "feat/a"},
		{Path: "/projects/quil-worktrees/detached", Detached: true},
	}, nil)
	stubWorktreeBranches(t, []gitworktree.Branch{
		{Name: "master", CommitTime: 100},
		{Name: "feat/a", CommitTime: 200},
	}, false, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil"}, "")
	if len(got.Worktrees) != 3 {
		t.Fatalf("Worktrees = %+v, want 3 entries", got.Worktrees)
	}
	if got.Worktrees[0].CommitTime != 100 || got.Worktrees[1].CommitTime != 200 {
		t.Errorf("CommitTime = %d, %d, want 100, 200 (joined from the branch listing)",
			got.Worktrees[0].CommitTime, got.Worktrees[1].CommitTime)
	}
	if got.Worktrees[2].CommitTime != 0 {
		t.Errorf("detached CommitTime = %d, want 0", got.Worktrees[2].CommitTime)
	}
	// The wire's branch list stays a list of NAMES: the client's collision
	// check reads it, and an older client decodes it.
	if len(got.Branches) != 2 || got.Branches[1] != "feat/a" {
		t.Errorf("Branches = %v, want the names alone", got.Branches)
	}
}

// The dialog refuses a branch name git would refuse, and this listing is where
// it learns the names. Without it the only branches the client knows about are
// the ones with a checkout — so a branch whose worktree was removed is invisible
// and the create fails in git with no dialog warning at all.
func TestWorktreeListResponse_CarriesTheRepositoryBranches(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
	}, nil)
	stubWorktreeBranches(t, named("master", "fix/nationality-filter"), false, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil"}, "")
	if len(got.Branches) != 2 || got.Branches[1] != "fix/nationality-filter" {
		t.Errorf("Branches = %v, want the repository's local branches", got.Branches)
	}
	if got.BranchesTruncated {
		t.Error("BranchesTruncated = true for a complete listing")
	}
}

// The branch listing is scoped to the SAME directory the worktree listing ran
// in — the browsed path, which git resolves to its repository. Running it
// somewhere else would answer about a different repository than the one whose
// Root the response reports.
func TestWorktreeListResponse_ListsBranchesInTheScannedDirectory(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
	}, nil)
	var sawDir string
	prev := worktreeBranchesFn
	worktreeBranchesFn = func(ctx context.Context, dir string) ([]gitworktree.Branch, bool, error) {
		sawDir = dir
		return nil, false, nil
	}
	t.Cleanup(func() { worktreeBranchesFn = prev })

	worktreeListResponse(ipc.WorktreeListReqPayload{Path: ""}, "/daemon/cwd")
	if sawDir != "/daemon/cwd" {
		t.Errorf("listed branches in %q, want the scanned directory", sawDir)
	}
}

// A directory outside any repository must cost NO second git invocation. The
// setup dialog's browser asks about every directory the user walks through, and
// most are not repositories — a subprocess per keystroke of navigation is what
// the single-flight and the permit budget exist to avoid.
func TestWorktreeListResponse_SkipsTheBranchListingOutsideARepository(t *testing.T) {
	stubWorktreeList(t, nil, nil)
	calls := stubWorktreeBranches(t, named("master"), false, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/tmp"}, "")
	if *calls != 0 {
		t.Errorf("ran the branch listing %d times outside a repository, want 0", *calls)
	}
	if len(got.Branches) != 0 {
		t.Errorf("Branches = %v outside a repository, want none", got.Branches)
	}
}

// A failed branch listing must NOT fail the whole response. The worktree list
// is what the dialog needs to function; the branch names are an extra check, and
// losing them degrades to the behaviour that shipped for a year — the daemon's
// own error at create time.
func TestWorktreeListResponse_BranchFailureKeepsTheWorktreeListing(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
	}, nil)
	stubWorktreeBranches(t, nil, false, errors.New("git not found"))

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil"}, "")
	if got.Error != "" {
		t.Errorf("Error = %q — a branch-listing failure must not fail the worktree listing", got.Error)
	}
	if !got.Repo || got.Root != "/projects/quil" {
		t.Error("the worktree listing was lost with the branch listing")
	}
	if len(got.Branches) != 0 {
		t.Errorf("Branches = %v after a failure, want none", got.Branches)
	}
}

// The truncation flag has to survive onto the wire. It is what stops the client
// concluding a name is FREE from a list it can see is incomplete — the
// false-negative direction, where a branch git will refuse looks available.
func TestWorktreeListResponse_CarriesTheTruncationFlag(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
	}, nil)
	stubWorktreeBranches(t, named("master"), true, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil"}, "")
	if !got.BranchesTruncated {
		t.Error("BranchesTruncated was dropped — the client would trust a clipped list")
	}
}
