package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlowMCPSpawn_PreservesHooksAndQuotesPaths(t *testing.T) {
	old := flowMCPExeFn
	flowMCPExeFn = func() (string, error) { return `C:\Program Files\Quil\quil-dev.exe`, nil }
	t.Cleanup(func() { flowMCPExeFn = old })
	for _, agent := range []string{"claude-code", "codex", "opencode"} {
		args, env, err := flowMCPSpawn(agent, []string{"existing"}, []string{`OPENCODE_CONFIG_CONTENT={"plugin":["hook.js"],"mcp":{"other":{"type":"remote","url":"https://example.com/mcp"}}}`, "KEEP=yes"})
		if err != nil {
			t.Fatal(err)
		}
		if args[len(args)-1] != "existing" {
			t.Fatal("lost argv")
		}
		if agent == "codex" && !strings.Contains(strings.Join(args, " "), `mcp_servers.quil.env_vars=["QUIL_HOME","QUIL_PANE_ID"]`) {
			t.Fatal("Codex MCP must inherit the pane identity and daemon home")
		}
		if agent == "opencode" {
			found := false
			for _, v := range env {
				if data, ok := strings.CutPrefix(v, "OPENCODE_CONFIG_CONTENT="); ok {
					var cfg map[string]any
					if err := json.Unmarshal([]byte(data), &cfg); err != nil {
						t.Fatal(err)
					}
					if cfg["plugin"] == nil || cfg["mcp"] == nil {
						t.Fatal(cfg)
					}
					servers := cfg["mcp"].(map[string]any)
					if servers["other"] == nil || servers["quil"] == nil {
						t.Fatal("lost existing MCP server", cfg)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("no OpenCode config")
			}
		} else if !strings.Contains(strings.Join(args, " "), "quil") {
			t.Fatal(args)
		}
	}
}
