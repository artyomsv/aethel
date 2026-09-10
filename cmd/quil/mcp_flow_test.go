package main

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestReportStep_RequiresNewDaemonBeforeSending(t *testing.T) {
	local := newFakeIPCDaemonVersion(t, "pane-local", "1.72.0")
	s, r := toolHarness(t, local, nil)
	r.selfPane = "pane-local"
	_, err := callTool(t, s, "report_step", map[string]any{"status": "done", "result": map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), mcpDaemonMinVersion) || !local.sawNo(ipc.MsgReportStepReq) {
		t.Fatal(err)
	}
}

func TestReportStep_LocalCallerResolutionAndDaemonRefusals(t *testing.T) {
	local := newFakeIPCDaemon(t, "pane-local")
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
