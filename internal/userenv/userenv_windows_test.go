//go:build windows

package userenv

import "testing"

// CI is Linux, so this file is never compiled by `dev.sh test`. Build a
// Windows test binary in Docker and run the .exe on the host:
//
//	GOOS=windows go test -c ./internal/userenv/ -o userenv.test.exe
//	./userenv.test.exe -test.run TestSetGet -test.v
//
// The variable name is deliberately marked as a test value and removed again,
// so a failed run leaves nothing behind in the user's real environment.
const testVar = "QUIL_TEST_USERENV_DELETE_ME"

func TestSetGet_RoundTripsThroughTheRegistry(t *testing.T) {
	t.Cleanup(func() { _ = remove(testVar) })

	const want = "TEST-value-not-a-real-token"
	if err := Set(testVar, want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := Get(testVar)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Errorf("Get = %q, want %q — the value did not survive the registry round trip", got, want)
	}
}

// A name that was never written must read as empty, NOT as an error: status
// has to distinguish "not set up yet" from "could not be read", and treating
// the first as a failure would make a fresh install look broken.
func TestGet_MissingNameIsEmptyNotAnError(t *testing.T) {
	got, err := Get("QUIL_TEST_USERENV_NEVER_WRITTEN")
	if err != nil {
		t.Errorf("Get on a missing name = %v, want no error", err)
	}
	if got != "" {
		t.Errorf("Get on a missing name = %q, want empty", got)
	}
}
