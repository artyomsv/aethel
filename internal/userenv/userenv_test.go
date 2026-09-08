package userenv

import (
	"errors"
	"runtime"
	"testing"
)

// An empty write is refused rather than persisted: at the daemon it is
// indistinguishable from having no token, except that it has silently
// overwritten one the user may have set earlier.
func TestSet_RefusesAnEmptyValue(t *testing.T) {
	if err := Set("QUIL_TEST_VAR", ""); !errors.Is(err, ErrEmptyValue) {
		t.Errorf("Set with an empty value = %v, want ErrEmptyValue", err)
	}
}

func TestSet_RefusesAnEmptyName(t *testing.T) {
	if err := Set("", "value"); err == nil {
		t.Error("Set accepted an empty variable name")
	}
}

// Unix must report ErrUnsupported rather than guessing a shell profile: a
// daemon started by launchd or systemd reads none of them, so a write that
// "succeeded" would leave the caller believing a gap had been closed.
func TestSet_UnixIsUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has a per-user environment store")
	}
	if err := Set("QUIL_TEST_VAR", "value"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Set on %s = %v, want ErrUnsupported", runtime.GOOS, err)
	}
	if _, err := Get("QUIL_TEST_VAR"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Get on %s = %v, want ErrUnsupported", runtime.GOOS, err)
	}
}
