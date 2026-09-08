//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW. Spelled out the same way gitinfo, gitworktree and
// cmd/quil spell their flags — it is not in syscall's constant set.
const _CREATE_NO_WINDOW = 0x08000000

// hideGitWindow stops the identity read from flashing a console window.
//
// The daemon is spawned DETACHED_PROCESS and owns no console, so a Windows
// child of a console-less parent gets a brand-new console allocated: a real
// window that appears and vanishes. This runs twice per sandbox pane created,
// which is enough for the user to see it.
func hideGitWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= _CREATE_NO_WINDOW
}
