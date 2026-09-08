//go:build !windows

package daemon

import "os/exec"

// hideGitWindow is a no-op everywhere but Windows: no other platform
// allocates a console for a child of a console-less parent.
func hideGitWindow(*exec.Cmd) {}
