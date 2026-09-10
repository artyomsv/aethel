package hookevents

import (
	"fmt"
	"testing"
	"time"
)

func applyAll(t *testing.T, l *WorkLedger, steps ...string) Transition {
	t.Helper()
	var tr Transition
	for _, s := range steps {
		tr = l.Apply(s, nil, time.Now())
	}
	return tr
}

func TestWorkLedger_StartsUnknownUntilFirstClassifiedEvent(t *testing.T) {
	var l WorkLedger
	if got := l.State(); got != WorkUnknown {
		t.Fatalf("fresh ledger State() = %q, want %q", got, WorkUnknown)
	}
	l.Apply("command_complete", nil, time.Now())
	if got := l.State(); got != WorkUnknown {
		t.Fatalf("after an unclassified event State() = %q, want %q", got, WorkUnknown)
	}
}

func TestWorkLedger_TurnStartAndStop(t *testing.T) {
	var l WorkLedger
	tr := l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	if l.State() != WorkWorking || tr.Now != WorkWorking || tr.Was != WorkUnknown {
		t.Fatalf("after start: state=%q tr=%+v", l.State(), tr)
	}
	stopAt := time.Unix(1000, 0)
	tr = l.Apply("hook.claude.Stop", nil, stopAt)
	if l.State() != WorkIdle || !tr.FellIdle || tr.Aborted {
		t.Fatalf("after stop: state=%q tr=%+v", l.State(), tr)
	}
	if !l.LastIdleAt().Equal(stopAt) {
		t.Fatalf("LastIdleAt = %v, want %v", l.LastIdleAt(), stopAt)
	}
}

func TestWorkLedger_SubagentsKeepTheTurnWorkingPastStop(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.SubagentStart", map[string]string{"agent_type": "qa"}, time.Now())
	tr := l.Apply("hook.claude.Stop", nil, time.Now())
	if l.State() != WorkWorking || tr.FellIdle {
		t.Fatalf("stop with a live subagent: state=%q tr=%+v, want still working", l.State(), tr)
	}
	// The unpaired end-of-turn stop names no agent and must not drain qa.
	l.Apply("hook.claude.SubagentStop", map[string]string{"agent_type": ""}, time.Now())
	if l.State() != WorkWorking {
		t.Fatalf("empty-key SubagentStop drained a live agent")
	}
	tr = l.Apply("hook.claude.SubagentStop", map[string]string{"agent_type": "qa"}, time.Now())
	if l.State() != WorkIdle || !tr.FellIdle {
		t.Fatalf("after draining qa: state=%q tr=%+v", l.State(), tr)
	}
}

func TestWorkLedger_CoalescedCountsAreHonoured(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.SubagentStart", map[string]string{"agent_type": "qa", "coalesced": "3"}, time.Now())
	l.Apply("hook.claude.Stop", nil, time.Now())
	l.Apply("hook.claude.SubagentStop", map[string]string{"agent_type": "qa", "coalesced": "2"}, time.Now())
	if l.State() != WorkWorking {
		t.Fatalf("3 starts minus 2 stops left nothing live")
	}
	l.Apply("hook.claude.SubagentStop", map[string]string{"agent_type": "qa"}, time.Now())
	if l.State() != WorkIdle {
		t.Fatalf("3 starts minus 3 stops still working")
	}
}

func TestWorkLedger_StopFailureNamingAnAgentDrainsOnlyThatAgent(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.SubagentStart", map[string]string{"agent_type": "qa"}, time.Now())
	l.Apply("hook.claude.StopFailure", map[string]string{"agent_type": "qa"}, time.Now())
	if l.State() != WorkWorking {
		t.Fatalf("a subagent failure ended the main turn")
	}
	tr := l.Apply("hook.claude.StopFailure", nil, time.Now())
	if l.State() != WorkIdle || !tr.FellIdle {
		t.Fatalf("main-turn failure: state=%q tr=%+v", l.State(), tr)
	}
}

func TestWorkLedger_PermissionRequestBlocksWithoutEndingTheTurn(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.PermissionRequest", map[string]string{"tool": "Bash"}, time.Now())
	if l.State() != WorkBlocked || l.BlockedReason() != "Bash" {
		t.Fatalf("state=%q reason=%q", l.State(), l.BlockedReason())
	}
	// A second park for the same prompt with no tool keeps the name.
	l.Apply("hook.claude.PermissionRequest", nil, time.Now())
	if l.BlockedReason() != "Bash" {
		t.Fatalf("reason erased by a park with no tool: %q", l.BlockedReason())
	}
	// Approving fires no hook; the turn's Stop is what unblocks.
	tr := l.Apply("hook.claude.Stop", nil, time.Now())
	if l.State() != WorkIdle || !tr.FellIdle || l.BlockedReason() != "" {
		t.Fatalf("after stop: state=%q reason=%q tr=%+v", l.State(), l.BlockedReason(), tr)
	}
}

func TestWorkLedger_NotificationParksUnlessMarkedIdleAfterStop(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.Notification", nil, time.Now())
	if l.State() != WorkBlocked {
		t.Fatalf("unmarked Notification mid-turn did not park: %q", l.State())
	}
	l.Apply("hook.claude.Stop", nil, time.Now())
	l.Apply("hook.claude.Notification", map[string]string{DataNotifyKind: NotifyKindIdle}, time.Now())
	if l.State() != WorkIdle {
		t.Fatalf("idle nudge after Stop changed state to %q", l.State())
	}
	l.Apply("hook.claude.Notification", nil, time.Now())
	if l.State() != WorkBlocked {
		t.Fatalf("unmarked Notification after Stop must park (may be a permission prompt): %q", l.State())
	}
}

func TestWorkLedger_ProcessExitAbortsWithoutCounting(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.SubagentStart", map[string]string{"agent_type": "qa"}, time.Now())
	tr := l.Apply("process_exit", nil, time.Now())
	if l.State() != WorkIdle || !tr.Aborted || tr.FellIdle {
		t.Fatalf("after exit: state=%q tr=%+v", l.State(), tr)
	}
}

// An abort leaves the level at WorkIdle, so the level cannot be the whole
// answer: Aborted() is what separates a pane that died from one that finished.
// It is sticky until the pane demonstrably works again.
func TestWorkLedger_AbortedOutlivesTheEventThatCausedIt(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	if l.Aborted() {
		t.Fatal("a working pane reports aborted")
	}
	l.Apply("process_exit", nil, time.Now())
	if l.State() != WorkIdle {
		t.Fatalf("state after exit = %q", l.State())
	}
	if !l.Aborted() {
		t.Fatal("a crashed pane is indistinguishable from a settled one")
	}
	// A stop edge is not evidence of a live child: a dead pane's last queued
	// Stop must not erase the fact.
	l.Apply("hook.claude.Stop", nil, time.Now())
	if !l.Aborted() {
		t.Fatal("a Stop cleared the abort")
	}
	// Working again does clear it.
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	if l.Aborted() {
		t.Fatal("a pane that started a new turn still reports aborted")
	}
}

func TestWorkLedger_SessionEndClearsLedgerAndOverflow(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	for i := 0; i < maxTrackedSubagents+1; i++ {
		l.Apply("hook.claude.SubagentStart", map[string]string{"agent_type": fmt.Sprintf("a%d", i)}, time.Now())
	}
	// Drain every tracked one; the overflowed agent is still (invisibly) live.
	for i := 0; i < maxTrackedSubagents; i++ {
		l.Apply("hook.claude.SubagentStop", map[string]string{"agent_type": fmt.Sprintf("a%d", i)}, time.Now())
	}
	l.Apply("hook.claude.Stop", nil, time.Now())
	if l.State() != WorkWorking {
		t.Fatalf("overflow flag did not keep the pane working: %q", l.State())
	}
	tr := l.Apply("hook.claude.SessionEnd", nil, time.Now())
	if l.State() != WorkIdle || !tr.FellIdle {
		t.Fatalf("after SessionEnd: state=%q tr=%+v", l.State(), tr)
	}
}

func TestWorkLedger_HeartbeatReArmsWorking(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	l.Apply("hook.claude.Stop", nil, time.Now())
	tr := l.Apply("hook.claude.PreToolUse", nil, time.Now())
	if l.State() != WorkWorking || tr.Now != WorkWorking {
		t.Fatalf("heartbeat after stop: state=%q tr=%+v", l.State(), tr)
	}
}

func TestWorkLedger_OpencodeAndCodexEdges(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.opencode.chat.message", nil, time.Now())
	l.Apply("hook.opencode.permission.ask", nil, time.Now())
	if l.State() != WorkBlocked {
		t.Fatalf("opencode permission.ask: %q", l.State())
	}
	l.Apply("hook.opencode.session.idle", nil, time.Now())
	if l.State() != WorkIdle {
		t.Fatalf("opencode session.idle: %q", l.State())
	}
	var c WorkLedger
	c.Apply("hook.codex.UserPromptSubmit", nil, time.Now())
	c.Apply("hook.codex.Stop", nil, time.Now())
	if c.State() != WorkIdle {
		t.Fatalf("codex stop: %q", c.State())
	}
}

func TestWorkLedger_TransitionReportsNoEdgeOnRepeatedState(t *testing.T) {
	var l WorkLedger
	l.Apply("hook.claude.UserPromptSubmit", nil, time.Now())
	tr := l.Apply("hook.claude.PreToolUse", nil, time.Now())
	if tr.Was != WorkWorking || tr.Now != WorkWorking || tr.FellIdle {
		t.Fatalf("repeated working: %+v", tr)
	}
}
