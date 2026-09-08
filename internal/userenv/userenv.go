// Package userenv writes a PERSISTENT, user-scoped environment variable.
//
// It exists for one caller: `quil sandbox login`, which has to put
// CLAUDE_CODE_OAUTH_TOKEN somewhere the daemon will find it on every future
// start. The alternative — Quil keeping its own copy of the token — was
// deliberately rejected: `claude setup-token` mints a Claude session token, and
// storing one reads as "collect, store, or intermediate … session tokens" under
// Anthropic's authentication policy, the same wording that parked copying
// ~/.claude/.credentials.json. Writing it to the OS store makes Quil a UI over
// somewhere the user could have typed it themselves, and Quil retains nothing.
//
// The split mirrors internal/notify: every file with logic is
// platform-neutral, and the //go:build windows file holds syscalls only — CI
// is Linux, so anything behind that tag is never compiled by `dev.sh test`.
package userenv

import "errors"

// ErrUnsupported reports a platform with no per-user environment store this
// package can write.
//
// Unix has none: a login shell's environment comes from a shell profile, and
// WHICH profile depends on the shell and on whether the session is a login
// one — while a daemon started by launchd or systemd reads none of them. A
// wrong guess there is worse than no answer, because it looks like it worked
// and the daemon still cannot see the variable. Callers print the export line
// and let the user place it.
var ErrUnsupported = errors.New("no per-user environment store on this platform")

// ErrEmptyValue refuses a blank write. Persisting an empty token is
// indistinguishable at the daemon from having none, except that it silently
// overwrites a good one the user set earlier.
var ErrEmptyValue = errors.New("refusing to persist an empty value")

// Set writes name=value into the user's persistent environment.
//
// It does NOT change the current process. The caller sets that separately with
// os.Setenv, because the two have different scopes and conflating them hides a
// real gap: Windows builds a new process's environment from its PARENT's, not
// from the registry, so a daemon spawned by this same run would not see a
// registry-only write until something re-read it. Doing both is what makes
// `quil sandbox login` take effect immediately AND survive a reboot.
func Set(name, value string) error {
	if name == "" {
		return errors.New("empty variable name")
	}
	if value == "" {
		return ErrEmptyValue
	}
	return set(name, value)
}

// Get reads name back from the user's persistent environment.
//
// Separate from os.Getenv on purpose: `quil sandbox status` has to be able to
// say "persisted but this shell has not picked it up yet", which the process
// environment alone cannot distinguish from "never set".
func Get(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty variable name")
	}
	return get(name)
}
