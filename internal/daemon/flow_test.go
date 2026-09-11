package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

func flowTestDaemon(t *testing.T) (*Daemon, *ipc.Client) {
	t.Helper()
	stubFlowCodexProbe(t)
	d, c := mcpTestDaemon(t)
	for _, name := range []string{"claude-code", "codex", "opencode"} {
		d.registry.Get(name).Available = true
	}
	prevExe, prevList := flowMCPExeFn, worktreeListFn
	flowMCPExeFn = func() (string, error) { return "/test/quil", nil }
	root := t.TempDir()
	worktreeListFn = func(context.Context, string) ([]gitworktree.Worktree, error) {
		return []gitworktree.Worktree{{Path: root}}, nil
	}
	t.Cleanup(func() { flowMCPExeFn, worktreeListFn = prevExe, prevList })
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0700) })
	shortenIdleSettle(t, 25*time.Millisecond)
	return d, c
}

func awaitFlow(t *testing.T, d *Daemon, id string, stage flow.Stage, paused bool) flow.Flow {
	t.Helper()
	var got flow.Flow
	if !waitUntilTrue(t, func() bool {
		for _, f := range d.flowSnapshots() {
			if f.ID == id {
				got = f
				// dispatchFlow reserves an ID before registering the task. Wait
				// for registration before a test reports or forces completion.
				return f.Stage == stage && f.Paused == paused && (paused || stage.Role() == "" || d.tasksRegistry().get(f.TaskID) != nil)
			}
		}
		return false
	}, 3*time.Second) {
		t.Fatalf("flow want %s paused=%v, got %+v", stage, paused, got)
	}
	return got
}

func startTestFlow(t *testing.T, d *Daemon, c *ipc.Client) flow.Flow {
	t.Helper()
	resp := decodeInto[ipc.StartFlowRespPayload](t, roundTrip(t, c, ipc.MsgStartFlowReq, ipc.MsgStartFlowResp, ipc.StartFlowReqPayload{Feature: "implement feature", Branch: "feat/test-flow"}))
	if resp.Error != "" || resp.FlowID == "" || resp.TabID == "" {
		t.Fatal(resp)
	}
	return awaitFlow(t, d, resp.FlowID, flow.StagePlan, false)
}

func TestFlowIPC_WholeEpicReportsAndIdle(t *testing.T) {
	d, c := flowTestDaemon(t)
	finish := captureFlowReportTimer(t)
	old := d.session.CreateTab("keep focus")
	f := startTestFlow(t, d, c)
	if d.session.ActiveTabID() != old.ID {
		t.Fatal("flow stole focus")
	}
	for role, id := range f.Panes {
		p := d.session.Pane(id)
		p.PluginMu.Lock()
		name, storedRole, cwd := p.Name, p.FlowRole, p.CWD
		p.PluginMu.Unlock()
		if name != string(role) || storedRole != string(role) || cwd == "" {
			t.Fatalf("role %s: %s %s %s", role, name, storedRole, cwd)
		}
	}
	for _, tc := range []struct {
		result map[string]string
		next   flow.Stage
	}{
		{map[string]string{"plan": "epic tickets"}, flow.StageBuild},
		{map[string]string{"pr": "71"}, flow.StageReview},
		{map[string]string{"verdict": "changes", "notes": "test it"}, flow.StageFix},
		{nil, flow.StageReview},
		{map[string]string{"verdict": "approved"}, flow.StageReadyForYou},
	} {
		p := d.session.Pane(f.Panes[f.Stage.Role()])
		d.emitEvent(hookEvent(p, "hook.claude.UserPromptSubmit", nil))
		resp := decodeInto[ipc.ReportStepRespPayload](t, roundTrip(t, c, ipc.MsgReportStepReq, ipc.MsgReportStepResp, ipc.ReportStepReqPayload{PaneID: p.ID, Status: "done", Result: tc.result}))
		if resp.Error != "" || resp.TaskID != f.TaskID {
			t.Fatal(resp)
		}
		finish()
		if !d.tasksRegistry().live(d.tasksRegistry().get(f.TaskID)) {
			t.Fatal("report advanced a pane with live hooks before idle")
		}
		d.emitEvent(hookEvent(p, "hook.claude.Stop", nil))
		f = awaitFlow(t, d, f.ID, tc.next, false)
	}
	if f.Results.PR != "71" || f.Round != 1 || !waitUntilTrue(t, func() bool { return hasEventType(d, f.Panes[flow.Reviewer], "flow_ready") }, time.Second) {
		t.Fatal(f)
	}
	msg, _ := ipc.NewMessage(ipc.MsgDestroyTab, ipc.DestroyTabPayload{TabID: f.TabID})
	d.handleMessage(nil, msg)
	if len(d.flowSnapshots()) != 0 {
		t.Fatal("closed tab retained flow")
	}
}

func TestFlowIPC_HooklessReportOwnershipCorrectionAndLateReport(t *testing.T) {
	d, c := flowTestDaemon(t)
	finish := captureFlowReportTimer(t)
	f := startTestFlow(t, d, c)
	report := func(req ipc.ReportStepReqPayload) ipc.ReportStepRespPayload {
		return decodeInto[ipc.ReportStepRespPayload](t, roundTrip(t, c, ipc.MsgReportStepReq, ipc.MsgReportStepResp, req))
	}
	if r := report(ipc.ReportStepReqPayload{PaneID: f.Panes[flow.Developer], TaskID: f.TaskID, Status: "done"}); !strings.Contains(r.Error, "target") {
		t.Fatal(r)
	}
	if r := report(ipc.ReportStepReqPayload{PaneID: "missing", Status: "done"}); r.Error != "no step is waiting on this pane" {
		t.Fatal(r)
	}
	id := f.TaskID
	for _, plan := range []string{"old", "corrected"} {
		if r := report(ipc.ReportStepReqPayload{PaneID: f.Panes[flow.Analyst], Status: "done", Result: map[string]string{"plan": plan}}); r.Error != "" {
			t.Fatal(r)
		}
	}
	finish()
	f = awaitFlow(t, d, f.ID, flow.StageBuild, false)
	if f.Results.Plan != "corrected" {
		t.Fatal(f)
	}
	if r := report(ipc.ReportStepReqPayload{PaneID: f.Panes[flow.Analyst], TaskID: id, Status: "done"}); r.Error != "step already ended" {
		t.Fatal(r)
	}
}

func TestFlowIPC_UnreportedIdlePausesAndExplicitResume(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	p := d.session.Pane(f.Panes[flow.Analyst])
	d.emitEvent(hookEvent(p, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(p, "hook.claude.Stop", nil))
	f = awaitFlow(t, d, f.ID, flow.StagePlan, true)
	if f.PauseWhy != "agent stopped without reporting" || !waitUntilTrue(t, func() bool { return hasEventType(d, p.ID, "flow_paused") }, time.Second) {
		t.Fatal(f)
	}
	r := decodeInto[ipc.StartFlowRespPayload](t, roundTrip(t, c, ipc.MsgResumeFlowReq, ipc.MsgResumeFlowResp, ipc.ResumeFlowReqPayload{FlowID: f.ID}))
	if r.Error != "" {
		t.Fatal(r)
	}
	awaitFlow(t, d, f.ID, flow.StagePlan, false)
}

func TestFlowIPC_RefusalCreatesNothingAndWorktreeFailureResumes(t *testing.T) {
	d, c := flowTestDaemon(t)
	d.registry.Get("codex").Available = false
	r := decodeInto[ipc.StartFlowRespPayload](t, roundTrip(t, c, ipc.MsgStartFlowReq, ipc.MsgStartFlowResp, ipc.StartFlowReqPayload{Feature: "x", Branch: "feat/x"}))
	if r.Error == "" || r.FlowID != "" || len(d.session.Tabs()) != 0 {
		t.Fatal(r)
	}
	d.registry.Get("codex").Available = true
	stubAdd(t, func(context.Context, string, string, string) error { return errors.New("git refused the branch") })
	r = decodeInto[ipc.StartFlowRespPayload](t, roundTrip(t, c, ipc.MsgStartFlowReq, ipc.MsgStartFlowResp, ipc.StartFlowReqPayload{Feature: "x", Branch: "feat/x"}))
	f := awaitFlow(t, d, r.FlowID, flow.StagePreparing, true)
	if f.PauseWhy != "git refused the branch" {
		t.Fatal(f)
	}
	stubAdd(t, func(_ context.Context, _, path, _ string) error { return os.MkdirAll(path, 0700) })
	if err := d.resumeFlow(f.ID); err != nil {
		t.Fatal(err)
	}
	awaitFlow(t, d, f.ID, flow.StagePlan, false)
}

func TestFlow_DelegateRefusalAndTimeoutPause(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	d.finishTask(d.tasksRegistry().get(f.TaskID), taskTimeout, "")
	f = awaitFlow(t, d, f.ID, flow.StagePlan, true)
	if f.PauseWhy != "timed out" {
		t.Fatal(f)
	}
	r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: f.Panes[flow.Analyst], Prompt: "other work"})
	if r.Error != "" {
		t.Fatal(r)
	}
	if err := d.resumeFlow(f.ID); err != nil {
		t.Fatal(err)
	}
	f = awaitFlow(t, d, f.ID, flow.StagePlan, true)
	if !strings.Contains(f.PauseWhy, "already has task") {
		t.Fatal(f)
	}
}

func TestFlow_RestartSnapshot(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	active, tabs, panes, projects, project, flows := d.session.snapshotStateWithFlows()
	state := d.workspaceStateFromSnapshot(active, tabs, panes, projects, project, false, flows)
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), f.TaskID) {
		t.Fatal("runtime task persisted")
	}
	if err := os.WriteFile(config.WorkspacePath(), b, 0600); err != nil {
		t.Fatal(err)
	}
	restored := New(config.Default())
	if err := restored.restoreWorkspace(); err != nil {
		t.Fatal(err)
	}
	got := awaitFlow(t, restored, f.ID, flow.StagePlan, true)
	if got.PauseWhy != "daemon restarted" || got.TaskID != "" {
		t.Fatal(got)
	}
	if restored.session.Pane(f.Panes[flow.Analyst]).FlowRole != "analyst" {
		t.Fatal("FlowRole lost on restore")
	}
	preparing := f.Clone()
	preparing.ID = "preparing"
	preparing.Stage = flow.StagePreparing
	missing := f.Clone()
	missing.ID = "missing"
	missing.TabID = "missing"
	restored.restoreFlows([]flow.Flow{f, preparing, missing})
	if len(restored.flowSnapshots()) != 1 {
		t.Fatal("preparing or orphan flow survived restore")
	}
}

func TestFlowIPC_SaveConfiguration(t *testing.T) {
	d, c := flowTestDaemon(t)
	cfg := config.DefaultFlows()
	cfg.MaxReviewRounds = 8
	r := decodeInto[ipc.FlowConfigRespPayload](t, roundTrip(t, c, ipc.MsgSaveFlowConfigReq, ipc.MsgSaveFlowConfigResp, ipc.SaveFlowConfigReqPayload{Config: cfg.Wire()}))
	if r.Error != "" {
		t.Fatal(r)
	}
	disk, err := config.LoadFlows()
	live, liveErr := d.flowsConfig()
	if err != nil || liveErr != nil || disk.MaxReviewRounds != 8 || live.MaxReviewRounds != 8 {
		t.Fatal(disk, live, err, liveErr)
	}
	role := cfg.Roles[flow.Analyst]
	role.Prompt = " "
	cfg.Roles[flow.Analyst] = role
	cfg.MaxReviewRounds = 9
	r = decodeInto[ipc.FlowConfigRespPayload](t, roundTrip(t, c, ipc.MsgSaveFlowConfigReq, ipc.MsgSaveFlowConfigResp, ipc.SaveFlowConfigReqPayload{Config: cfg.Wire()}))
	if !strings.Contains(r.Error, "prompt") {
		t.Fatal("blank prompt accepted", r)
	}
	disk, err = config.LoadFlows()
	live, liveErr = d.flowsConfig()
	if err != nil || liveErr != nil || disk.MaxReviewRounds != 8 || live.MaxReviewRounds != 8 {
		t.Fatal("invalid configuration changed disk or live settings", disk, live, err, liveErr)
	}
}

func TestFlow_HooksAppearingDuringFallbackKeepTaskLive(t *testing.T) {
	d, c := flowTestDaemon(t)
	finish := captureFlowReportTimer(t)
	f := startTestFlow(t, d, c)
	r := d.reportStep(ipc.ReportStepReqPayload{PaneID: f.Panes[flow.Analyst], Status: "done", Result: map[string]string{"plan": "p"}})
	if r.Error != "" {
		t.Fatal(r)
	}
	p := d.session.Pane(f.Panes[flow.Analyst])
	d.emitEvent(hookEvent(p, "hook.claude.UserPromptSubmit", nil))
	finish()
	if !d.tasksRegistry().live(d.tasksRegistry().get(f.TaskID)) {
		t.Fatal("hookless fallback ignored arriving hooks")
	}
	d.emitEvent(hookEvent(p, "hook.claude.Stop", nil))
	awaitFlow(t, d, f.ID, flow.StageBuild, false)
}

func TestFlow_ClosedStepPanePausesAndResumeRefuses(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	msg, _ := ipc.NewMessage(ipc.MsgDestroyPane, ipc.DestroyPanePayload{PaneID: f.Panes[flow.Analyst]})
	d.handleMessage(nil, msg)
	f = awaitFlow(t, d, f.ID, flow.StagePlan, true)
	if f.PauseWhy != "pane analyst was closed" {
		t.Fatal(f)
	}
	if !waitUntilTrue(t, func() bool { return hasEventType(d, f.Panes[flow.Developer], "flow_paused") }, time.Second) {
		t.Fatal("closed pane lost its pause notification")
	}
	if err := d.resumeFlow(f.ID); err == nil || !strings.Contains(err.Error(), "analyst") {
		t.Fatal(err)
	}
}

func TestFlow_TaskEndDoesNotWaitForWedgedWriter(t *testing.T) {
	d := newTestDaemon(t)
	a, _ := agentPane(t, d, "analyst")
	tab := d.session.Tab(a.TabID)
	p, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := newWedgedSession()
	defer close(w.release)
	p.PTY = w
	p.Type = "claude-code"
	f := flow.Flow{ID: "f", TabID: tab.ID, Stage: flow.StagePlan, TaskID: "done-task", Panes: map[flow.Role]string{flow.Analyst: a.ID, flow.Developer: p.ID}}
	d.session.mu.Lock()
	d.session.flows = map[string]*flow.Flow{"f": &f}
	d.session.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		d.flowOnTaskEnd(ipc.TaskInfo{ID: "done-task", ToPane: a.ID, State: "done"}, &flow.Report{Status: "done", Result: map[string]string{"plan": "implement"}})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("task completion waited for PTY writer")
	}
	if got := awaitFlow(t, d, "f", flow.StageBuild, false); got.TaskID == "" {
		t.Fatal(got)
	}
}

// Tests inspect the exact same snapshot as persistence and workspace broadcasts.
func (d *Daemon) flowSnapshots() []flow.Flow {
	_, _, _, _, _, flows := d.session.snapshotStateWithFlows()
	return flows
}

func captureFlowReportTimer(t *testing.T) func() {
	t.Helper()
	old := flowReportAfter
	var callbacks chan func() = make(chan func(), 2)
	flowReportAfter = func(_ time.Duration, f func()) *time.Timer {
		callbacks <- f
		return time.NewTimer(time.Hour)
	}
	t.Cleanup(func() { flowReportAfter = old })
	return func() {
		t.Helper()
		select {
		case f := <-callbacks:
			f()
		default:
			t.Fatal("report did not schedule fallback")
		}
		if len(callbacks) != 0 {
			t.Fatal("corrected report scheduled a second timer")
		}
	}
}

func TestFlowIPC_OrdinaryTask_RefusesReportWithoutEndingTask(t *testing.T) {
	d, c := flowTestDaemon(t)
	p, _ := agentPane(t, d, "ordinary")
	r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: p.ID, Prompt: "ordinary work"})
	if r.Error != "" {
		t.Fatal(r)
	}
	for _, taskID := range []string{"", r.Task.ID} {
		resp := decodeInto[ipc.ReportStepRespPayload](t, roundTrip(t, c, ipc.MsgReportStepReq, ipc.MsgReportStepResp,
			ipc.ReportStepReqPayload{PaneID: p.ID, TaskID: taskID, Status: "done", Result: map[string]string{"plan": "ignored"}}))
		if resp.Error != "task is not a flow step" {
			t.Fatal(resp)
		}
	}
	reg := d.tasksRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	task := reg.byID[r.Task.ID]
	if task.state.terminal() || task.report != nil || task.reportTimer != nil {
		t.Fatal("report mutated ordinary task")
	}
}

func TestFlowStart_InvalidInputs_AllocatesNothing(t *testing.T) {
	d, _ := flowTestDaemon(t)
	for _, feature := range []string{"bad\x1b[201~\r", "bad\u009b", "bad\r"} {
		r, _ := d.startFlow(ipc.StartFlowReqPayload{Feature: feature, Branch: "feat/invalid"})
		if r.Error == "" || len(d.session.Tabs()) != 0 {
			t.Fatal(r)
		}
	}
	cfg := config.DefaultFlows()
	r := cfg.Roles[flow.Analyst]
	r.Toggles = nil
	cfg.Roles[flow.Analyst] = r
	if err := d.validateFlowRoles(cfg); err == nil || !strings.Contains(err.Error(), "permission_mode") {
		t.Fatal(err)
	}
}

func TestFlowResume_ForeignTab_RefusesDispatch(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	d.finishTask(d.tasksRegistry().get(f.TaskID), taskTimeout, "timed out")
	foreign, _ := agentPane(t, d, "foreign")
	d.session.mu.Lock()
	d.session.flows[f.ID].Panes[flow.Analyst] = foreign.ID
	d.session.mu.Unlock()
	if err := d.resumeFlow(f.ID); err == nil || !strings.Contains(err.Error(), "another tab") {
		t.Fatal(err)
	}
}

func TestFlowBroadcast_ResultBodies_OnlyPersisted(t *testing.T) {
	d, c := flowTestDaemon(t)
	f := startTestFlow(t, d, c)
	d.session.mu.Lock()
	d.session.flows[f.ID].Results = flow.Results{Plan: "private plan", Notes: "private notes", PR: "42"}
	d.session.mu.Unlock()
	broadcast := d.buildWorkspaceState()["flows"].([]flow.Flow)[0]
	persisted := d.flowSnapshots()[0]
	if broadcast.Feature != "" || broadcast.Results.Plan != "" || broadcast.Results.Notes != "" || broadcast.Results.PR != "42" {
		t.Fatal(broadcast)
	}
	if persisted.Feature == "" || persisted.Results.Plan != "private plan" || persisted.Results.Notes != "private notes" {
		t.Fatal("broadcast mutated persisted flow", persisted)
	}
}

type flowSpawnSession struct {
	apty.Session
	beforeStart func() error
}

func (s *flowSpawnSession) Start(cmd string, args ...string) error {
	if err := s.beforeStart(); err != nil {
		return err
	}
	return s.Session.Start(cmd, args...)
}

func TestPrepareFlow_CancelledDuringSpawn_ReleasesCreatedPanes(t *testing.T) {
	d, _ := flowTestDaemon(t)
	resp, prepare := d.startFlow(ipc.StartFlowReqPayload{Feature: "cancel me", Branch: "feat/cancel"})
	if resp.Error != "" {
		t.Fatal(resp)
	}
	old := newSessionFn
	count := 0
	newSessionFn = func(cols, rows int) apty.Session {
		count++
		n := count
		return &flowSpawnSession{Session: old(cols, rows), beforeStart: func() error {
			if n == 2 {
				d.session.mu.Lock()
				delete(d.session.flows, resp.FlowID)
				d.session.mu.Unlock()
			}
			return nil
		}}
	}
	t.Cleanup(func() { newSessionFn = old })
	prepare()
	if len(d.session.Panes(resp.TabID)) != 0 {
		t.Fatal("cancelled preparation leaked panes")
	}
}

func TestPrepareFlow_SpawnFailure_NamesRoleAndRefusesResume(t *testing.T) {
	d, _ := flowTestDaemon(t)
	resp, prepare := d.startFlow(ipc.StartFlowReqPayload{Feature: "failed spawn", Branch: "feat/failure"})
	if resp.Error != "" {
		t.Fatal(resp)
	}
	old := newSessionFn
	count := 0
	newSessionFn = func(cols, rows int) apty.Session {
		count++
		n := count
		return &flowSpawnSession{Session: old(cols, rows), beforeStart: func() error {
			if n == 2 {
				return errors.New("cannot launch")
			}
			return nil
		}}
	}
	t.Cleanup(func() { newSessionFn = old })
	prepare()
	f := awaitFlow(t, d, resp.FlowID, flow.StagePlan, true)
	if !strings.Contains(f.PauseWhy, "developer") || !strings.Contains(f.PauseWhy, "cannot launch") {
		t.Fatal(f)
	}
	if err := d.resumeFlow(f.ID); err == nil || !strings.Contains(err.Error(), "developer") {
		t.Fatal(err)
	}
}

func TestFlow_Shutdown_RefusesNewWork(t *testing.T) {
	d := New(config.Default())
	close(d.shutdown)
	resp, prepare := d.startFlow(ipc.StartFlowReqPayload{Feature: "feature", Branch: "feat/x"})
	if resp.Error == "" || prepare != nil || len(d.session.Tabs()) != 0 {
		t.Fatal(resp)
	}
	if err := d.resumeFlow("f"); err == nil {
		t.Fatal("resumed during shutdown")
	}
}

func TestSpawnPane_FlowRoles_PassConfiguredModel(t *testing.T) {
	d := newTestDaemon(t)
	registerShippedPlugins(t, d)
	stubFlowCodexProbe(t)
	old := flowMCPExeFn
	flowMCPExeFn = func() (string, error) { return "/test/quil", nil }
	t.Cleanup(func() { flowMCPExeFn = old })
	cfg := config.DefaultFlows()
	models := map[flow.Role]struct{ agent, model, flag string }{
		flow.Analyst:   {"claude-code", "claude-opus-5", "--model"},
		flow.Developer: {"codex", "gpt-5-codex", "-m"},
		flow.Reviewer:  {"opencode", "anthropic/claude-sonnet-5", "--model"},
	}
	for role, m := range models {
		r := cfg.Roles[role]
		r.Agent, r.Model = m.agent, m.model
		cfg.Roles[role] = r
		d.registry.Get(m.agent).Available = true
	}
	d.session.mu.Lock()
	d.session.flowConfig = cfg
	d.session.mu.Unlock()
	for role, m := range models {
		for _, flowRole := range []string{string(role), ""} {
			fake := &fakeSession{}
			pane := &Pane{ID: "pane-m0de1000", Type: m.agent, FlowRole: flowRole, CWD: t.TempDir()}
			if err := d.spawnPane(pane, fake, false); err != nil {
				t.Fatal(role, err)
			}
			idx := -1
			for i, arg := range fake.startArgs {
				if arg == m.flag && i+1 < len(fake.startArgs) && fake.startArgs[i+1] == m.model {
					idx = i
				}
			}
			if (idx >= 0) != (flowRole != "") {
				t.Fatalf("%s role %q: model flag present=%v in %v", role, flowRole, idx >= 0, fake.startArgs)
			}
		}
	}
}

func TestFlowStart_Repository_UsesGivenDirectoryAndRefusesMissing(t *testing.T) {
	d, _ := flowTestDaemon(t)
	other := t.TempDir()
	resp, _ := d.startFlow(ipc.StartFlowReqPayload{Feature: "in another repo", Branch: "feat/other", CWD: other})
	if resp.Error != "" {
		t.Fatal(resp)
	}
	panes := d.session.Panes(resp.TabID)
	if len(panes) != 1 {
		t.Fatal("expected the analyst placeholder", len(panes))
	}
	want, _ := filepath.EvalSymlinks(other)
	got, _ := filepath.EvalSymlinks(panes[0].CWD)
	if !strings.EqualFold(got, want) {
		t.Fatalf("placeholder cwd %q, want %q", panes[0].CWD, other)
	}
	tabs := len(d.session.Tabs())
	missing := filepath.Join(other, "no-such-dir")
	resp, _ = d.startFlow(ipc.StartFlowReqPayload{Feature: "x", Branch: "feat/missing", CWD: missing})
	if resp.Error == "" || !strings.Contains(resp.Error, "repository directory") {
		t.Fatal("missing repository accepted", resp)
	}
	if len(d.session.Tabs()) != tabs {
		t.Fatal("refused flow created a tab")
	}
}
