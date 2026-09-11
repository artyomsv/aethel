package daemon

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
)

// A role's agent can be changed while its panes exist, and those panes keep
// their original Type. Keying the model on the role name alone therefore
// handed a live claude pane the codex id the role had just been given, and
// every restart of that pane failed on a model its agent has never heard of.
func TestSpawnPane_FlowRole_SkipsAModelConfiguredForAnotherAgent(t *testing.T) {
	d := newTestDaemon(t)
	registerShippedPlugins(t, d)
	stubFlowCodexProbe(t)
	old := flowMCPExeFn
	flowMCPExeFn = func() (string, error) { return "/test/quil", nil }
	t.Cleanup(func() { flowMCPExeFn = old })
	for _, agent := range []string{"claude-code", "codex"} {
		d.registry.Get(agent).Available = true
	}

	cfg := config.DefaultFlows()
	analyst := cfg.Roles[flow.Analyst]
	analyst.Agent, analyst.Model, analyst.Toggles = "codex", "gpt-5-codex", []string{"auto_workspace_write"}
	cfg.Roles[flow.Analyst] = analyst
	d.session.mu.Lock()
	d.session.flowConfig = cfg
	d.session.mu.Unlock()

	// The pane predates the agent change and is still claude-code.
	stale := &fakeSession{}
	if err := d.spawnPane(&Pane{ID: "pane-51a1e000", Type: "claude-code", FlowRole: string(flow.Analyst), CWD: t.TempDir()}, stale, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stale.startArgs, " "); strings.Contains(got, "gpt-5-codex") || strings.Contains(got, "--model") {
		t.Fatalf("claude pane received the codex role model: %s", got)
	}

	// A pane of the configured agent still gets it.
	matching := &fakeSession{}
	if err := d.spawnPane(&Pane{ID: "pane-a9a9a9a9", Type: "codex", FlowRole: string(flow.Analyst), CWD: t.TempDir()}, matching, false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(matching.startArgs, " "); !strings.Contains(got, "-m gpt-5-codex") {
		t.Fatalf("codex pane lost its configured model: %s", got)
	}
}
