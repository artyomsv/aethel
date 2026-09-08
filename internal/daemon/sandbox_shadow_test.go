package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The regression that made every sandbox pane after the first fail to spawn.
//
// The file is created 0400. On Windows that sets the ReadOnly attribute, so a
// later O_CREATE|O_WRONLY open answers "Access is denied" — and the old code
// forgave only os.IsExist, which O_CREATE without O_EXCL can never return. The
// first pane on a fresh QUIL_HOME worked; every one after it died at spawn.
func TestEnsureEmptyShadowFile_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-file")

	for i := 1; i <= 3; i++ {
		if err := ensureEmptyShadowFile(path); err != nil {
			t.Fatalf("call %d: %v — a second sandbox pane cannot spawn", i, err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() != 0 {
		t.Errorf("shadow file is %d bytes; it is mounted over config.worktree and git would read it", st.Size())
	}
}

// The mode is what triggers the Windows attribute. Asserting the created file
// really is read-only keeps this test describing the shipped condition rather
// than a friendlier one.
func TestEnsureEmptyShadowFile_CreatesItReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows maps mode onto the ReadOnly attribute, not a permission bit")
	}
	path := filepath.Join(t.TempDir(), "empty-file")
	if err := ensureEmptyShadowFile(path); err != nil {
		t.Fatalf("create: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0o200 != 0 {
		t.Errorf("mode = %v, want no write bit", st.Mode().Perm())
	}
}

// Docker invents a root-owned DIRECTORY for a missing bind source. Silently
// accepting one would mount a directory over config.worktree, which git
// refuses to read — with the reason nowhere near the symptom.
func TestEnsureEmptyShadowFile_RefusesADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-file")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := ensureEmptyShadowFile(path); err == nil {
		t.Error("a directory was accepted as the shadow file")
	}
}

// Content here is not cosmetic: the file is mounted over config.worktree, and
// host git executes values from that file.
func TestEnsureEmptyShadowFile_RefusesANonEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-file")
	if err := os.WriteFile(path, []byte("[core]\n\tfsmonitor = evil\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ensureEmptyShadowFile(path); err == nil {
		t.Error("a non-empty shadow file was accepted; git would read it as config")
	}
}
