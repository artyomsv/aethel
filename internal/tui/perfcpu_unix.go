//go:build !windows

package tui

import (
	"syscall"
	"time"
)

// processCPU returns the CPU time this process has consumed, user plus system.
//
// RUSAGE_SELF covers every thread of the process, which is what the perf line
// wants: the question is what the whole TUI spent, not what one goroutine's
// thread did.
//
// Timeval's Usec field is int64 on Linux and int32 on Darwin, so both fields go
// through an explicit conversion rather than being added as-is.
func processCPU() (time.Duration, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	tv := func(sec, usec int64) time.Duration {
		return time.Duration(sec)*time.Second + time.Duration(usec)*time.Microsecond
	}
	return tv(int64(ru.Utime.Sec), int64(ru.Utime.Usec)) +
		tv(int64(ru.Stime.Sec), int64(ru.Stime.Usec)), true
}
