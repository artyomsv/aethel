//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// CI is Linux and runs as ROOT, where mode 0400 is not enforced — so the
// idempotency test in sandbox_shadow_test.go passes against the BROKEN
// implementation there. Verified by mutation. This file is where that bug is
// actually reachable, and it is never compiled by `dev.sh test`:
//
//	GOOS=windows go test -c ./internal/daemon/ -o daemon.test.exe
//	./daemon.test.exe -test.run ShadowFile -test.v
//
// The mechanism: 0400 sets the Windows ReadOnly ATTRIBUTE, and opening a
// ReadOnly file for writing answers "Access is denied" — which os.IsExist does
// not match, so the old forgiving branch never fired and the spawn failed.
func TestEnsureEmptyShadowFile_SurvivesTheReadOnlyAttribute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-file")

	if err := ensureEmptyShadowFile(path); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Precondition, asserted rather than assumed: without the ReadOnly
	// attribute this test cannot fail and proves nothing.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0o200 != 0 {
		t.Fatalf("precondition: the file is writable (mode %v); the ReadOnly attribute "+
			"is what this test exists to survive", st.Mode().Perm())
	}

	// The call that used to fail with "Access is denied".
	if err := ensureEmptyShadowFile(path); err != nil {
		t.Errorf("second call: %v — this is the failure that stopped every sandbox pane "+
			"after the first from spawning", err)
	}
}
