package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func flowMCPSupported(agent string) bool {
	return agent == "claude-code" || agent == "codex" || agent == "opencode"
}

// Use the matched sibling bridge, including the dev/debug suffix. A PATH
// lookup could select a production bridge talking to another workspace.
var flowMCPExeFn = func() (string, error) {
	daemon, err := quildExeFn()
	if err != nil {
		return "", err
	}
	name := filepath.Base(daemon)
	if !strings.HasPrefix(name, "quild") {
		return "", fmt.Errorf("cannot locate Quil MCP bridge beside %s", daemon)
	}
	path := filepath.Join(filepath.Dir(daemon), "quil"+strings.TrimPrefix(name, "quild"))
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", fmt.Errorf("Quil MCP bridge is missing: %s", path)
	}
	return path, nil
}

// These are argv elements, not shell snippets. JSON/TOML encoders preserve
// Windows paths and spaces without shell interpolation. The pane inherits
// QUIL_HOME and QUIL_PANE_ID through the existing spawn environment.
func flowMCPSpawn(agent string, args, env []string) ([]string, []string, error) {
	exe, err := flowMCPExeFn()
	if err != nil {
		return nil, nil, err
	}
	switch agent {
	case "claude-code":
		b, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"quil": map[string]any{
			"type": "stdio", "command": exe, "args": []string{"mcp", "--toolset", "flow"},
		}}})
		if err != nil {
			return nil, nil, err
		}
		args = append([]string{"--mcp-config", string(b)}, args...)
	case "codex":
		// Codex filters the environment inherited by stdio servers. Explicitly
		// forward the pane identity and daemon directory or report_step cannot
		// resolve its caller (and a custom QUIL_HOME would reach another daemon).
		args = append([]string{"-c", "mcp_servers.quil.command=" + strconv.Quote(exe), "-c", `mcp_servers.quil.args=["mcp","--toolset","flow"]`,
			"-c", `mcp_servers.quil.env_vars=["QUIL_HOME","QUIL_PANE_ID"]`}, args...)
	case "opencode":
		cfg := make(map[string]any)
		out := make([]string, 0, len(env))
		for _, item := range env {
			if data, ok := strings.CutPrefix(item, "OPENCODE_CONFIG_CONTENT="); ok {
				if err := json.Unmarshal([]byte(data), &cfg); err != nil {
					return nil, nil, fmt.Errorf("OpenCode config: %w", err)
				}
			} else {
				out = append(out, item)
			}
		}
		servers, _ := cfg["mcp"].(map[string]any)
		if servers == nil {
			servers = make(map[string]any)
		}
		servers["quil"] = map[string]any{"type": "local", "command": []string{exe, "mcp", "--toolset", "flow"}, "enabled": true}
		cfg["mcp"] = servers
		b, err := json.Marshal(cfg)
		if err != nil {
			return nil, nil, err
		}
		env = append(out, "OPENCODE_CONFIG_CONTENT="+string(b))
	default:
		return nil, nil, fmt.Errorf("agent %q has no per-spawn MCP support", agent)
	}
	return args, env, nil
}
