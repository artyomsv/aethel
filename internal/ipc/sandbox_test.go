package ipc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A create with no sandbox must be byte-identical to what every existing
// producer sends today — the MCP bridge, restore, the plugin dialog. That is
// what keeps them on the unchanged path with no branch anywhere in the daemon.
func TestCreatePanePayload_NoSandboxIsWireIdentical(t *testing.T) {
	raw, err := json.Marshal(ipc.CreatePanePayload{TabID: "t1", Type: "claude-code"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sandbox") {
		t.Errorf("a sandbox-less create carries the field: %s", raw)
	}
}

func TestCreatePanePayload_SandboxRoundTrips(t *testing.T) {
	raw, err := json.Marshal(ipc.CreatePanePayload{
		TabID:   "t1",
		Sandbox: &ipc.SandboxSpec{Image: "ghcr.io/o/img:1"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ipc.CreatePanePayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Sandbox == nil || back.Sandbox.Image != "ghcr.io/o/img:1" {
		t.Errorf("sandbox spec did not survive: %+v", back.Sandbox)
	}
}

// The silent isolation failure this field exists to prevent: the setup
// dialog's own flow — new tab, worktree chosen, sandbox on — reaches the
// daemon through FirstPaneSpec, so a spec that stopped at CreatePanePayload
// would open a tab whose agent runs on the HOST with no error.
func TestFirstPaneSpec_CarriesSandboxAlongsideWorktree(t *testing.T) {
	raw, err := json.Marshal(ipc.CreateTabPayload{
		FirstPane: &ipc.FirstPaneSpec{
			Type:     "claude-code",
			Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
			Sandbox:  &ipc.SandboxSpec{Image: "img"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ipc.CreateTabPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.FirstPane == nil || back.FirstPane.Sandbox == nil {
		t.Fatal("sandbox spec lost on the tab-first-pane path")
	}
	if back.FirstPane.Worktree == nil {
		t.Fatal("worktree spec lost — the two must travel together")
	}
}

func TestFirstPaneSpec_NoSandboxIsWireIdentical(t *testing.T) {
	raw, err := json.Marshal(ipc.CreateTabPayload{FirstPane: &ipc.FirstPaneSpec{Type: "terminal"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "sandbox") {
		t.Errorf("a sandbox-less first pane carries the field: %s", raw)
	}
}

func TestSandboxCapRespPayload_RoundTrips(t *testing.T) {
	raw, err := json.Marshal(ipc.SandboxCapRespPayload{
		Available: true, ServerVersion: "29.6.2", OSType: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ipc.SandboxCapRespPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Available || back.Arch != "amd64" || back.OSType != "linux" {
		t.Errorf("capability answer did not survive: %+v", back)
	}
}

// An unavailable engine must be distinguishable from one that never answered:
// the client treats silence as unavailable, and an explicit false with a
// reason is what lets the dialog explain itself.
func TestSandboxCapRespPayload_UnavailableCarriesAReason(t *testing.T) {
	raw, _ := json.Marshal(ipc.SandboxCapRespPayload{Error: "docker engine not reachable"})
	var back ipc.SandboxCapRespPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Available {
		t.Error("zero value must not read as available")
	}
	if back.Error == "" {
		t.Error("the reason was dropped")
	}
}
