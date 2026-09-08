package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// repoProbeTimeout bounds the one rev-parse NewMapping runs. It is a metadata
// read, so it is budgeted like gitinfo's probes rather than like a checkout —
// but it can still park on a dead network mount, and NewMapping runs on the
// create path with a user waiting.
var repoProbeTimeout = 10 * time.Second

// gitBinary is the executable resolveRepo invokes. A package var so a test can
// point it at a stub without a PATH game.
var gitBinary = "git"

// runGit is the seam every git invocation goes through, so the table tests
// need no repository and no git binary.
//
// It mirrors gitworktree.runGit rather than calling it: that package exists to
// keep repository WRITES away from the read-only one, and importing it here to
// borrow four lines would put a writer in a package whose only git call is a
// read. The two non-obvious pieces are the same, and both are load-bearing —
// hideWindow (a console-less daemon otherwise allocates a visible console per
// child on Windows) and WaitDelay (CommandContext kills git on expiry, but a
// credential helper or fsmonitor inheriting the pipe keeps Wait blocked).
var runGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	return string(out), err
}

// resolveRepo asks git for the three facts NewMapping needs: the checkout's
// toplevel, the repository's shared .git, and this worktree's own git dir.
//
// --path-format=absolute is what makes the answers usable directly. Without it
// git answers --git-dir and --git-common-dir relative to the CWD for an
// ordinary checkout and absolute for a linked worktree, so the caller would
// have to know which shape it was looking at before it could tell which shape
// it was looking at.
func resolveRepo(ctx context.Context, cwd string) (top, common, gitDir string, err error) {
	ctx, cancel := context.WithTimeout(ctx, repoProbeTimeout)
	defer cancel()

	out, err := runGit(ctx, cwd, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-common-dir", "--git-dir")
	if err != nil {
		return "", "", "", fmt.Errorf("%w: %s: %v", ErrNotARepository, cwd, err)
	}
	lines := splitLines(out)
	if len(lines) != 3 {
		return "", "", "", fmt.Errorf("%w: %s: git answered %d lines, want 3",
			ErrNotARepository, cwd, len(lines))
	}
	top, common, gitDir = lines[0], lines[1], lines[2]
	if top == "" || common == "" || gitDir == "" {
		// A bare repository answers with an empty toplevel. It has no
		// working tree, so there is nothing to mount and nothing to edit.
		return "", "", "", fmt.Errorf("%w: %s: no working tree", ErrNotARepository, cwd)
	}
	return filepath.ToSlash(top), filepath.ToSlash(common), filepath.ToSlash(gitDir), nil
}

// splitLines returns the non-empty trimmed lines of git's output. \r is
// trimmed too: git on Windows is happy to emit CRLF here.
func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}
