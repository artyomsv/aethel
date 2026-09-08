//go:build !windows

package userenv

// Unix has no per-user environment store to write.
//
// Guessing a shell profile is worse than refusing: the right file depends on
// the shell and on whether the session is a login one, and a daemon started by
// launchd or systemd reads none of them at all — so a write that "succeeds"
// would leave the daemon exactly as unable to see the variable as before,
// while telling the user it had been handled. The caller prints the export
// line instead and lets the user place it where their own setup needs it.
func set(string, string) error { return ErrUnsupported }

func get(string) (string, error) { return "", ErrUnsupported }
