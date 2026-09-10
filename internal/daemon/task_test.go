package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// recordingLiveSession is a live fake PTY that records every stdin write.
type recordingLiveSession struct {
	liveFakeSession
	mu     sync.Mutex
	writes []string
}

func newRecordingLiveSession() *recordingLiveSession {
	return &recordingLiveSession{liveFakeSession: liveFakeSession{done: make(chan struct{})}}
}

func (r *recordingLiveSession) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes = append(r.writes, string(p))
	r.mu.Unlock()
	return len(p), nil
}

func (r *recordingLiveSession) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.writes, "")
}

// agentPane spawns a claude-code pane on a recording live session.
func agentPane(t *testing.T, d *Daemon, name string) (*Pane, *recordingLiveSession) {
	t.Helper()
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pane.PluginMu.Lock()
	pane.Type = "claude-code"
	pane.Name = name
	pane.PluginMu.Unlock()
	sess := newRecordingLiveSession()
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	return pane, sess
}

func waitWrites(t *testing.T, s *recordingLiveSession, want string) {
	t.Helper()
	if !waitUntilTrue(t, func() bool { return strings.Contains(s.joined(), want) }, 2*time.Second) {
		t.Fatalf("stdin never received %q; got %q", want, s.joined())
	}
}

func TestDelegateTask_PastesPromptThenEnter(t *testing.T) {
	d := newTestDaemon(t)
	target, sess := agentPane(t, d, "worker")

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "run the tests\nthen report"})
	if resp.Error != "" || resp.Task.State != "sent" || !strings.HasPrefix(resp.Task.ID, "task-") {
		t.Fatalf("resp = %+v", resp)
	}
	waitWrites(t, sess, "\x1b[200~run the tests\nthen report\x1b[201~")
	waitWrites(t, sess, "\r")
	if strings.Index(sess.joined(), "\r") < strings.Index(sess.joined(), "\x1b[201~") {
		t.Fatalf("Enter arrived before the paste closed: %q", sess.joined())
	}
}

func TestDelegateTask_RefusesWhatPaneInputRefuses(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	placeholder, _ := d.session.CreatePane(tab.ID, t.TempDir())
	placeholder.PreparingWorktree = "feat/x"

	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: placeholder.ID, Prompt: "x"}); r.Error == "" || !strings.Contains(r.Error, "worktree") {
		t.Fatalf("placeholder accepted: %+v", r)
	}
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: "pane-nope", Prompt: "x"}); r.Error == "" {
		t.Fatalf("unknown pane accepted: %+v", r)
	}
	target, _ := agentPane(t, d, "w")
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "  "}); r.Error == "" {
		t.Fatalf("empty prompt accepted: %+v", r)
	}
	if r := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, FromPane: target.ID, Prompt: "x"}); r.Error == "" {
		t.Fatalf("self-delegation accepted: %+v", r)
	}
}

func TestDelegateTask_CompletesOnSettledIdleNotRawStop(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 30*time.Millisecond)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("after start: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.SubagentStart", map[string]string{"agent_type": "qa"}))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	time.Sleep(80 * time.Millisecond)
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("Stop with a live subagent ended the task: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.SubagentStop", map[string]string{"agent_type": "qa"}))
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("task ended on the raw falling edge, before the settle window: %s", st)
	}
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never settled done: %+v", reg.info(task))
	}
	info := reg.info(task)
	if info.EndedAt == 0 || info.StartedAt == 0 {
		t.Fatalf("timestamps missing: %+v", info)
	}
	if !hasEventType(d, target.ID, "task_done") {
		t.Fatal("task_done never queued")
	}
	select {
	case <-task.done:
	default:
		t.Fatal("done channel not closed")
	}
}

func TestDelegateTask_ResumeInsideSettleKeepsWorking(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 40*time.Millisecond)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "do it"})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(target, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	d.emitEvent(hookEvent(target, "hook.claude.PreToolUse", nil))
	time.Sleep(120 * time.Millisecond)
	if st := reg.info(task).State; st != "working" {
		t.Fatalf("task ended although the agent resumed inside the window: %s", st)
	}
	d.emitEvent(hookEvent(target, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never finished after the second Stop: %+v", reg.info(task))
	}
}

func TestDelegateTask_ProcessExitFailsAndTimeoutTimesOut(t *testing.T) {
	d := newTestDaemon(t)
	target, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	r1 := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "a", TimeoutMs: 30})
	if !waitUntilTrue(t, func() bool { return reg.info(reg.get(r1.Task.ID)).State == "timeout" }, 2*time.Second) {
		t.Fatalf("timeout never fired: %+v", reg.info(reg.get(r1.Task.ID)))
	}
	r2 := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: target.ID, Prompt: "b"})
	d.emitEvent(hookEvent(target, "process_exit", map[string]string{"exit_code": "1"}))
	info := reg.info(reg.get(r2.Task.ID))
	if info.State != "failed" || info.Error == "" {
		t.Fatalf("after exit: %+v", info)
	}
}

func TestDelegateTask_TerminalTargetCompletesOnCommandComplete(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, _ := d.session.CreatePane(tab.ID, t.TempDir())
	sess := newRecordingLiveSession()
	if err := d.spawnPane(pane, sess, false); err != nil {
		t.Fatal(err)
	}
	reg := d.tasksRegistry()
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: pane.ID, Prompt: "make test"})
	// CR, not LF: LF is echoed but not executed by PowerShell under ConPTY.
	waitWrites(t, sess, "make test\r")
	if strings.Contains(sess.joined(), "\x1b[200~") {
		t.Fatalf("a terminal got a bracketed paste: %q", sess.joined())
	}
	d.emitEvent(hookEvent(pane, "command_complete", map[string]string{"exit_code": "0"}))
	if st := reg.info(reg.get(resp.Task.ID)).State; st != "done" {
		t.Fatalf("after command_complete: %s", st)
	}
}

// The notify-back is typed into the requester only while it is not mid-turn;
// otherwise it waits for the requester's own settled idle.
func TestDelegateTask_NotifyBackWaitsForRequesterIdle(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	from, fromSess := agentPane(t, d, "orchestrator")
	to, _ := agentPane(t, d, "worker")
	reg := d.tasksRegistry()

	// Requester is mid-turn (it is the one calling the tool, after all).
	d.emitEvent(hookEvent(from, "hook.claude.UserPromptSubmit", nil))
	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: to.ID, FromPane: from.ID, Prompt: "do it", Notify: true})
	task := reg.get(resp.Task.ID)

	d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	if !waitUntilTrue(t, func() bool { return reg.info(task).State == "done" }, 2*time.Second) {
		t.Fatalf("task never done: %+v", reg.info(task))
	}
	time.Sleep(60 * time.Millisecond)
	if strings.Contains(fromSess.joined(), "[quil task") {
		t.Fatalf("notice typed into a WORKING requester: %q", fromSess.joined())
	}
	if reg.info(task).Notified {
		t.Fatal("Notified reported before delivery")
	}
	// Requester finishes its turn → the deferred notice lands.
	d.emitEvent(hookEvent(from, "hook.claude.Stop", nil))
	waitWrites(t, fromSess, "[quil task "+task.id+"] pane "+to.ID+" (worker) done.")
	waitWrites(t, fromSess, "get_task with task_id="+task.id)
	if !waitUntilTrue(t, func() bool { return reg.info(task).Notified }, time.Second) {
		t.Fatal("Notified never reported")
	}
}

func TestDelegateTask_NotifyBackIsImmediateForAnIdleRequester(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	from, fromSess := agentPane(t, d, "orchestrator")
	to, _ := agentPane(t, d, "worker")

	resp := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: to.ID, FromPane: from.ID, Prompt: "do it", Notify: true})
	d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	waitWrites(t, fromSess, "[quil task "+resp.Task.ID+"]")
}

func TestTaskRegistry_EvictsOldestTerminalOnly(t *testing.T) {
	r := newTaskRegistry(2)
	live := &task{id: "a", state: taskWorking, done: make(chan struct{})}
	old := &task{id: "b", state: taskDone, done: make(chan struct{})}
	r.add(live)
	r.add(old)
	r.add(&task{id: "c", state: taskSent, done: make(chan struct{})})
	if r.get("b") != nil {
		t.Fatal("terminal task b not evicted")
	}
	if r.get("a") == nil || r.get("c") == nil {
		t.Fatal("a live task was evicted")
	}
}

func TestTaskRequests_RoundTrip(t *testing.T) {
	d, client := mcpTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	to, _ := agentPane(t, d, "worker")

	del := decodeInto[ipc.DelegateTaskRespPayload](t, roundTrip(t, client, ipc.MsgDelegateTaskReq, ipc.MsgDelegateTaskResp,
		ipc.DelegateTaskReqPayload{ToPane: to.ID, Prompt: "hello"}))
	if del.Error != "" || del.Task.ID == "" {
		t.Fatalf("delegate: %+v", del)
	}
	got := decodeInto[ipc.GetTaskRespPayload](t, roundTrip(t, client, ipc.MsgGetTaskReq, ipc.MsgGetTaskResp,
		ipc.GetTaskReqPayload{TaskID: del.Task.ID}))
	if got.Task.State != "sent" || got.Task.ToPaneName != "worker" {
		t.Fatalf("get: %+v", got)
	}
	list := decodeInto[ipc.ListTasksRespPayload](t, roundTrip(t, client, ipc.MsgListTasksReq, ipc.MsgListTasksResp,
		ipc.ListTasksReqPayload{PaneID: to.ID}))
	if len(list.Tasks) != 1 {
		t.Fatalf("list: %+v", list)
	}
	none := decodeInto[ipc.ListTasksRespPayload](t, roundTrip(t, client, ipc.MsgListTasksReq, ipc.MsgListTasksResp,
		ipc.ListTasksReqPayload{PaneID: "pane-other"}))
	if len(none.Tasks) != 0 {
		t.Fatalf("list for another pane: %+v", none)
	}
	timed := decodeInto[ipc.WaitTaskRespPayload](t, roundTrip(t, client, ipc.MsgWaitTaskReq, ipc.MsgWaitTaskResp,
		ipc.WaitTaskReqPayload{TaskID: del.Task.ID, TimeoutMs: 50}))
	if !timed.Timeout {
		t.Fatalf("wait on a live task did not time out: %+v", timed)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		d.emitEvent(hookEvent(to, "hook.claude.UserPromptSubmit", nil))
		d.emitEvent(hookEvent(to, "hook.claude.Stop", nil))
	}()
	done := decodeInto[ipc.WaitTaskRespPayload](t, roundTrip(t, client, ipc.MsgWaitTaskReq, ipc.MsgWaitTaskResp,
		ipc.WaitTaskReqPayload{TaskID: del.Task.ID, TimeoutMs: 3000}))
	if done.Timeout || done.Task.State != "done" {
		t.Fatalf("wait: %+v", done)
	}
	missing := decodeInto[ipc.GetTaskRespPayload](t, roundTrip(t, client, ipc.MsgGetTaskReq, ipc.MsgGetTaskResp,
		ipc.GetTaskReqPayload{TaskID: "task-nope"}))
	if missing.Error == "" {
		t.Fatal("unknown task answered without an error")
	}
}
