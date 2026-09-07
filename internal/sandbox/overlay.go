package sandbox

// The two overlay files, and why they exist at all.
//
// A linked worktree's .git is a FILE holding an absolute HOST path:
//
//	gitdir: E:/Projects/quil/.git/worktrees/feat-x
//
// A Linux container cannot resolve that, so git inside the container answers
// "fatal: not a git repository" for the directory it is standing in. The fix
// is to mount a one-line file with the CONTAINER path over it.
//
// That alone is not enough, and the gap destroys data. The MAIN repository's
// admin file, .git/worktrees/<name>/gitdir, still names the host path — so git
// inside the container decides the worktree is gone and unregisters it through
// the /repo mount. Measured: `git worktree prune` printed "Removing
// worktrees/wt: gitdir file points to non-existent location", after which the
// host's own `git status` in that worktree answered "fatal: not a git
// repository" and the uncommitted work was unreachable. `git worktree repair`
// corrupts it the other way, writing a container path into the host's file.
// A second overlay over that admin file makes both a no-op.
//
// Both files live in a directory that is NEVER mounted as a directory, only as
// two individual read-only files. A read-only flag is per mount point: the
// same inode reachable read-write elsewhere is writable, and an earlier
// revision kept these inside the pane's own read-write root, where the agent
// simply rewrote them through the other path and re-opened the prune bug.

// DotGitOverlay is the content of the file mounted over the worktree's own
// .git — it points at where the admin directory is readable in the container.
//
// For an ordinary checkout there is no overlay: .git is a real directory
// inside the working tree, which is mounted as-is.
func DotGitOverlay(m Mapping) string {
	if m.Kind != KindWorktree {
		return ""
	}
	return "gitdir: " + ContainerGitDir + "/worktrees/" + m.AdminName + "\n"
}

// AdminGitdirOverlay is the content of the file mounted over the MAIN
// repository's worktrees/<name>/gitdir — it points back at the worktree as the
// container sees it, which is what makes `git worktree list` correct inside
// the container and `git worktree prune` a no-op.
func AdminGitdirOverlay(m Mapping) string {
	if m.Kind != KindWorktree {
		return ""
	}
	return m.ContainerWorkdir() + "/.git\n"
}
