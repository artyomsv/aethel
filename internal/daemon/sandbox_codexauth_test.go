package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/artyomsv/quil/internal/sandbox"
)

func codexFixture(t *testing.T) (sandbox.Mapping, string) {
	t.Helper()
	m := sandbox.Mapping{PaneID: "p1", HostPaneRoot: t.TempDir()}
	if err := os.MkdirAll(m.HostCodexHome(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hostHome := t.TempDir()
	prev := hostCodexHomeFn
	hostCodexHomeFn = func() string { return hostHome }
	t.Cleanup(func() { hostCodexHomeFn = prev })
	return m, hostHome
}

// MEASURED before this was written: a container given the host's auth.json
// under CODEX_HOME runs codex against the user's own plan. Without the copy
// every codex pane opens on Codex's sign-in menu, whose browser option cannot
// work from a container at all.
func TestSeedCodexAuth_CopiesTheHostCredential(t *testing.T) {
	m, hostHome := codexFixture(t)
	const body = "TEST-not-a-real-credential"
	if err := os.WriteFile(filepath.Join(hostHome, codexAuthFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write host auth: %v", err)
	}

	if err := seedCodexAuth(m); err != nil {
		t.Fatalf("seedCodexAuth: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(m.HostCodexHome(), codexAuthFile))
	if err != nil {
		t.Fatalf("no credential in the pane's codex home: %v", err)
	}
	if string(got) != body {
		t.Errorf("copied %q, want %q", got, body)
	}
}

// Once the pane has one it is the PANE's: codex rewrites auth.json when it
// refreshes, so overwriting would replace a live credential with a staler copy
// of the host's.
func TestSeedCodexAuth_NeverOverwrites(t *testing.T) {
	m, hostHome := codexFixture(t)
	if err := os.WriteFile(filepath.Join(hostHome, codexAuthFile), []byte("HOST"), 0o600); err != nil {
		t.Fatalf("write host: %v", err)
	}
	dst := filepath.Join(m.HostCodexHome(), codexAuthFile)
	if err := os.WriteFile(dst, []byte("PANE-REFRESHED"), 0o600); err != nil {
		t.Fatalf("write pane: %v", err)
	}

	if err := seedCodexAuth(m); err != nil {
		t.Fatalf("seedCodexAuth: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "PANE-REFRESHED" {
		t.Errorf("the pane's own credential was overwritten with %q", got)
	}
}

// A user who has never run codex on this machine has no credential to copy.
// The pane then shows Codex's own sign-in, which is correct — not an error.
func TestSeedCodexAuth_NoHostCredentialIsNotAnError(t *testing.T) {
	m, _ := codexFixture(t)

	if err := seedCodexAuth(m); err != nil {
		t.Errorf("seedCodexAuth = %v; a user with no codex login must still get a pane", err)
	}
	if _, err := os.Stat(filepath.Join(m.HostCodexHome(), codexAuthFile)); !os.IsNotExist(err) {
		t.Error("a credential appeared from nowhere")
	}
}

// The copy is a credential in a directory the container can write, so it gets
// the narrowest mode the container user can still read.
func TestSeedCodexAuth_WritesItPrivate(t *testing.T) {
	// runtime.GOOS, not os.Getenv("GOOS") — GOOS is a BUILD variable and does
	// not exist in the environment at test time, so the Getenv form never
	// fired and this ran on Windows against Go's synthesised 0666.
	if runtime.GOOS == "windows" {
		t.Skip("Windows maps mode onto attributes, not permission bits")
	}
	m, hostHome := codexFixture(t)
	if err := os.WriteFile(filepath.Join(hostHome, codexAuthFile), []byte("x"), 0o600); err != nil {
		t.Fatalf("write host: %v", err)
	}
	if err := seedCodexAuth(m); err != nil {
		t.Fatalf("seedCodexAuth: %v", err)
	}
	st, err := os.Stat(filepath.Join(m.HostCodexHome(), codexAuthFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode = %v, want no group or other access", st.Mode().Perm())
	}
}
