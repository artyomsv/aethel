package sandbox

import "path/filepath"

// evalSymlinksFn is the seam the refusal tests drive. Production is
// filepath.EvalSymlinks; a test can make a path resolve somewhere else to
// exercise the junction case, which is otherwise unreachable on a Linux CI
// runner.
var evalSymlinksFn = filepath.EvalSymlinks

// realPath resolves p through every symlink and junction, then normalises it.
//
// Containment tests MUST run on real paths. A cleaned absolute path is still a
// path that traverses links, so a Windows junction — C:\dev pointing inside
// $QUIL_HOME — passes a string prefix test while the mount it authorises
// physically exposes $QUIL_HOME. The daemon already resolves symlinks for the
// same class of check when it validates a client-supplied CWD.
//
// It fails rather than falling back to the unresolved path. A caller that
// cannot see where a directory really is has no business deciding it is safe
// to mount, and every caller here treats the error as a refusal.
func realPath(p string) (string, error) {
	resolved, err := evalSymlinksFn(filepath.FromSlash(p))
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(abs), nil
}
