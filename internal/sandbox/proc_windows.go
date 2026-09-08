//go:build windows

package sandbox

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW. Not in syscall's constant set, so it is spelled out here
// the same way gitworktree/proc_windows.go and cmd/quil/proc_windows.go spell
// their flags.
const _CREATE_NO_WINDOW = 0x08000000

// hideWindow stops a git or docker invocation from flashing a console window.
//
// The daemon is spawned DETACHED_PROCESS, so it owns no console — and a
// Windows child started from a console-less parent gets a BRAND NEW console
// allocated, which is a real window that appears and vanishes.
//
// This matters more here than it does in gitworktree, where the cost is one
// flash per pane created. `docker info` runs on every capability-cache expiry
// while the create dialog is open, so without this the user watches a window
// flash over the dialog they are still reading.
//
// Duplicated rather than shared, following the precedent gitworktree set: the
// alternative is a fourth package holding four lines. Any future exec added to
// this package needs the same treatment.
func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= _CREATE_NO_WINDOW
}
