package main

import (
	"context"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestReportStep_RequiresNewDaemonBeforeSending(t *testing.T) {
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.72.0")
	s, r := toolHarness(t, local, nil)
	r.selfPane = "pane-local"
	_, err := callTool(t, s, "report_step", map[string]any{"status": "done", "result": map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), reportStepMinVersion) || !local.sawNo(ipc.MsgReportStepReq) {
		t.Fatal(err)
	}
}

func TestMCPToolset_Flow_ExposesOnlyLocalStepTools(t *testing.T) {
	flowOnly, err := parseMCPToolset([]string{"--toolset", "flow"})
	if err != nil || !flowOnly {
		t.Fatal(flowOnly, err)
	}
	if _, err := parseMCPToolset([]string{"--toolset", "typo"}); err == nil {
		t.Fatal("invalid toolset enabled unrestricted tools")
	}
	local := newFakeIPCDaemon(t, "pane-local")
	remote := newFakeIPCDaemon(t, "pane-remote")
	s, r := toolsetHarness(t, local, remote, flowOnly)
	r.selfPane = "pane-local"
	list, err := s.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 2 {
		t.Fatalf("flow toolset: %+v %v", list, err)
	}
	for _, tool := range list.Tools {
		if tool.Name != "report_step" && tool.Name != "get_task" {
			t.Fatal("unsafe flow tool", tool.Name)
		}
	}
	if _, err := callTool(t, s, "destroy_pane", map[string]any{"pane_id": "pane-local"}); err == nil {
		t.Fatal("flow bridge exposed destruction")
	}
	r.remember("gpu", "foreign")
	_, _ = callTool(t, s, "get_task", map[string]any{"task_id": "foreign"}) // Refusal is daemon-dependent; destination is not.
	if !remote.sawNo(ipc.MsgGetTaskReq) {
		t.Fatal("flow get_task reached remote daemon")
	}
}

func TestReportStep_OldDaemon_DoesNotDisableExistingTools(t *testing.T) {
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.72.0")
	s, _ := toolHarness(t, local, nil)
	if _, err := callTool(t, s, "list_projects", nil); err != nil {
		t.Fatal("1.72 tools were disabled", err)
	}
}

func TestReportStep_LocalCallerResolutionAndDaemonRefusals(t *testing.T) {
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.73.0")
	remote := newFakeIPCDaemon(t, "pane-remote")
	s, r := toolHarness(t, local, remote)
	r.selfPane = "pane-local"
	// Even an id remembered from a remote host must be sent to LOCAL.
	r.remember("gpu", "task-foreign")
	text, err := callTool(t, s, "report_step", map[string]any{"status": "done", "result": map[string]string{"plan": "tickets"}})
	if err != nil || !strings.Contains(text, "task-1") {
		t.Fatal(text, err)
	}
	_, err = callTool(t, s, "report_step", map[string]any{"status": "done", "task_id": "task-foreign", "result": map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "target") || !remote.sawNo(ipc.MsgReportStepReq) {
		t.Fatal(err)
	}
	r.selfPane = "missing"
	_, err = callTool(t, s, "report_step", map[string]any{"status": "done", "result": map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "no step is waiting") {
		t.Fatal(err)
	}
}
