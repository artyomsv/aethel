package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/sandbox"
)

func seedFixture(t *testing.T) sandbox.Mapping {
	t.Helper()
	root := t.TempDir()
	m := sandbox.Mapping{
		PaneID:       "p1",
		Kind:         sandbox.KindWorktree,
		Slug:         "wt-abcd1234",
		HostPaneRoot: root,
	}
	if err := os.MkdirAll(m.HostClaudeConfig(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return m
}

func readSeed(t *testing.T, m sandbox.Mapping) map[string]any {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(m.HostClaudeConfig(), ".claude.json"))
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	return got
}

// Each key answers a first-run screen the user would otherwise meet on EVERY
// sandbox pane. Verified against the real image: with none of them, interactive
// Claude Code shows a theme picker and then a sign-in — which is what made a
// working token look broken.
func TestSeedClaudeConfig_AnswersEveryFirstRunScreen(t *testing.T) {
	m := seedFixture(t)
	if err := seedClaudeConfig(m, true); err != nil {
		t.Fatalf("seedClaudeConfig: %v", err)
	}
	got := readSeed(t, m)

	if got["hasCompletedOnboarding"] != true {
		t.Error("hasCompletedOnboarding missing — the pane shows the theme picker and then a sign-in")
	}
	if got["theme"] == nil || got["theme"] == "" {
		t.Error("theme missing — skipping the picker means answering it")
	}
	if got["bypassPermissionsModeAccepted"] != true {
		t.Error("bypassPermissionsModeAccepted missing — the pane shows the bypass banner")
	}

	// The project entry is keyed by the path the CONTAINER sees, not the
	// host's: Claude records trust against its own working directory, which is
	// where `docker run -w` puts it.
	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects = %T, want an object", got["projects"])
	}
	entry, ok := projects[m.ContainerCWD()].(map[string]any)
	if !ok {
		t.Fatalf("no project entry for the container CWD %q; keys are %v",
			m.ContainerCWD(), keysOf(projects))
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Error("hasTrustDialogAccepted missing — the pane shows the trust prompt")
	}
}

// The file is the pane's own state once Claude has written to it, and under
// shared_claude_config it belongs to every sandbox pane at once. Overwriting
// would discard a real sign-in, the user's theme and their trust answers.
func TestSeedClaudeConfig_NeverOverwrites(t *testing.T) {
	m := seedFixture(t)
	path := filepath.Join(m.HostClaudeConfig(), ".claude.json")
	const existing = `{"theme":"light","mine":true}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := seedClaudeConfig(m, true); err != nil {
		t.Fatalf("seedClaudeConfig: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != existing {
		t.Errorf("an existing config was rewritten:\n got %s\nwant %s", body, existing)
	}
}

// A second pane sharing one config directory must not have the first pane's
// sign-in replaced by a fresh seed.
func TestSeedClaudeConfig_IsIdempotent(t *testing.T) {
	m := seedFixture(t)
	for i := 0; i < 3; i++ {
		if err := seedClaudeConfig(m, true); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if got := readSeed(t, m); got["hasCompletedOnboarding"] != true {
		t.Error("the seed did not survive repeated calls")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The regression the `authed` gate exists for.
//
// Skipping onboarding also skips the SIGN-IN SCREEN inside it. Seeding a pane
// that receives no credential therefore hands the user a working-looking
// prompt that fails on its first request with no way to log in — measured
// against the real image before the gate existed. Under `auth = "browser"`
// that onboarding login IS the sign-in.
func TestSeedClaudeConfig_SkipsWhenThePaneGetsNoToken(t *testing.T) {
	m := seedFixture(t)

	if err := seedClaudeConfig(m, false); err != nil {
		t.Fatalf("seedClaudeConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.HostClaudeConfig(), ".claude.json")); !os.IsNotExist(err) {
		t.Error("an unauthenticated pane was seeded; its sign-in screen is now hidden " +
			"and it has no way to authenticate")
	}
}
