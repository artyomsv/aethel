//go:build windows

package tui

import (
	"syscall"
	"time"
)

// processCPU returns the CPU time this process has consumed, user plus kernel.
//
// GetProcessTimes is the Windows counterpart to getrusage(2). It is already in
// the syscall package, so this needs no CGo and no new dependency — matching
// how internal/clipboard and internal/notify reach Win32.
//
// The handle is the pseudo-handle GetCurrentProcess returns: a constant, not a
// real object, so it must NOT be closed. Closing it is harmless on paper but
// the call is documented as unnecessary, and a Close here would read as though
// a resource were being managed.
func processCPU() (time.Duration, bool) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, false
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	// Filetime.Nanoseconds converts the 100-ns FILETIME ticks. Kernel and user
	// are both counted: a frame blocked in a console write spends its time in
	// the kernel, and excluding that would hide exactly the case this figure
	// was added to identify.
	return time.Duration(kernel.Nanoseconds() + user.Nanoseconds()), true
}
