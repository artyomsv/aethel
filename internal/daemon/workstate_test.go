package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
)

func shortenIdleSettle(t *testing.T, d time.Duration) {
	t.Helper()
	prev := agentIdleSettle
	agentIdleSettle = d
	t.Cleanup(func() { agentIdleSettle = prev })
}

func hookEvent(pane *Pane, typ string, data map[string]string) PaneEvent {
	return PaneEvent{
		ID: typ + "-" + pane.ID, PaneID: pane.ID, TabID: pane.TabID,
		Type: typ, Title: typ, Severity: "info", Timestamp: time.Now(), Data: data,
	}
}

func spawnedPane(t *testing.T, d *Daemon) *Pane {
	t.Helper()
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	// A LIVE fake: the plain fakeSession exits at once, and the process_exit
	// that follows is an abort edge that resets the very ledger under test.
	if err := d.spawnPane(pane, newLiveFakeSession(), false); err != nil {
		t.Fatalf("spawnPane: %v", err)
	}
	return pane
}

func hasEventType(d *Daemon, paneID, typ string) bool {
	for _, e := range d.events.Events() {
		if e.PaneID == paneID && e.Type == typ {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// The daemon's answer to "is this pane free" is derived from the same edges the
// TUI derives its spinner from, so an MCP client with no TUI can ask.
func TestEmitEvent_DrivesAgentStateOnTheWire(t *testing.T) {
	d := newTestDaemon(t)
	pane := spawnedPane(t, d)

	if got := d.buildPaneStatus(pane).AgentState; got != "" {
		t.Fatalf("fresh pane AgentState = %q, want empty (unknown)", got)
	}
	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	if got := d.buildPaneStatus(pane).AgentState; got != "working" {
		t.Fatalf("after start AgentState = %q", got)
	}
	d.emitEvent(hookEvent(pane, "hook.claude.PermissionRequest", map[string]string{"tool": "Bash"}))
	st := d.buildPaneStatus(pane)
	if st.AgentState != "blocked" || st.BlockedReason != "Bash" {
		t.Fatalf("after park: %+v", st)
	}
	d.emitEvent(hookEvent(pane, "hook.claude.Stop", nil))
	st = d.buildPaneStatus(pane)
	if st.AgentState != "idle" || st.LastIdleAt == 0 {
		t.Fatalf("after stop: %+v", st)
	}
	infos := d.buildPaneInfos()
	if len(infos) != 1 || infos[0].AgentState != "idle" || infos[0].ProjectID == "" {
		t.Fatalf("list_panes view: %+v", infos)
	}
}

// agent_idle is the queued completion signal: it fires after the settle window
// and NOT on the raw Stop, which lands while background subagents still run.
func TestEmitEvent_AgentIdleFiresAfterSettle(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 30*time.Millisecond)
	pane := spawnedPane(t, d)

	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(pane, "hook.claude.Stop", nil))
	if hasEventType(d, pane.ID, "agent_idle") {
		t.Fatal("agent_idle queued on the raw Stop, before the settle window")
	}
	if !waitFor(t, func() bool { return hasEventType(d, pane.ID, "agent_idle") }, time.Second) {
		t.Fatal("agent_idle never queued after the settle window")
	}
}

func TestEmitEvent_StartInsideSettleCancelsAgentIdle(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 40*time.Millisecond)
	pane := spawnedPane(t, d)

	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(pane, "hook.claude.Stop", nil))
	// Claude resumed on its own (a teammate reported back): a heartbeat.
	d.emitEvent(hookEvent(pane, "hook.claude.PreToolUse", nil))
	time.Sleep(120 * time.Millisecond)
	if hasEventType(d, pane.ID, "agent_idle") {
		t.Fatal("agent_idle queued although the pane went back to work inside the window")
	}
	if got := d.buildPaneStatus(pane).AgentState; got != "working" {
		t.Fatalf("AgentState = %q, want working", got)
	}
}

// A muted pane emits no notifications, but its ledger must keep moving or the
// daemon would report a finished pane as working for the whole mute.
func TestEmitEvent_MutedPaneStillUpdatesLedger(t *testing.T) {
	d := newTestDaemon(t)
	pane := spawnedPane(t, d)
	pane.PluginMu.Lock()
	pane.Muted = true
	pane.PluginMu.Unlock()

	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	d.emitEvent(hookEvent(pane, "hook.claude.Stop", nil))
	if got := d.buildPaneStatus(pane).AgentState; got != "idle" {
		t.Fatalf("muted pane AgentState = %q, want idle", got)
	}
	if hasEventType(d, pane.ID, "hook.claude.Stop") {
		t.Fatal("a muted pane's Stop reached the queue")
	}
}

// onPaneIdle runs immediately for a pane that is not working, and after the
// settle for one that is.
func TestOnPaneIdle_RunsNowOrAfterSettle(t *testing.T) {
	d := newTestDaemon(t)
	shortenIdleSettle(t, 20*time.Millisecond)
	pane := spawnedPane(t, d)

	ran := make(chan struct{}, 2)
	if !d.onPaneIdle(pane.ID, func() { ran <- struct{}{} }) {
		t.Fatal("onPaneIdle refused a live pane")
	}
	select {
	case <-ran:
	default:
		t.Fatal("subscriber on an unknown-state pane did not run immediately")
	}

	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	d.onPaneIdle(pane.ID, func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("subscriber ran while the pane was working")
	default:
	}
	d.emitEvent(hookEvent(pane, "hook.claude.Stop", nil))
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("subscriber never ran after the pane settled idle")
	}
	if d.onPaneIdle("pane-nope", func() {}) {
		t.Fatal("onPaneIdle accepted an unknown pane")
	}
}

// A process exit is not a completion but it is final: subscribers must not be
// stranded waiting for an idle edge that can never come.
func TestOnPaneIdle_ProcessExitReleasesSubscribers(t *testing.T) {
	d := newTestDaemon(t)
	pane := spawnedPane(t, d)
	d.emitEvent(hookEvent(pane, "hook.claude.UserPromptSubmit", nil))
	ran := make(chan struct{}, 1)
	d.onPaneIdle(pane.ID, func() { ran <- struct{}{} })
	d.emitEvent(hookEvent(pane, "process_exit", map[string]string{"exit_code": "1"}))
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("subscriber stranded after process exit")
	}
	if _, ok := any(hookevents.WorkIdle).(hookevents.WorkState); !ok {
		t.Fatal("unreachable")
	}
}
