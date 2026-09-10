package daemon

import (
	"time"

	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/google/uuid"
)

// agentIdleSettle is how long a pane must stay idle after its work ledger
// falls before the daemon calls the turn finished.
//
// The falling edge alone is not enough: when a teammate reports back, Claude
// resumes on its own — a user-ROLE transcript entry with no UserPromptSubmit —
// and the next edge is a PreToolUse heartbeat up to workHeartbeatInterval
// later. Two seconds catches the immediate resume (the common case, measured
// as Stop → PreToolUse within ~1 s) without holding a genuinely finished task
// for long. A package var so tests can shorten it.
var agentIdleSettle = 2 * time.Second

// applyWorkEvent feeds one event into the pane's work ledger and arms or
// disarms the settle window. Called from emitEvent BEFORE the mute and
// work-state-only bypasses, so the daemon's ledger sees exactly the edges the
// TUI's does — a muted pane's Stop and a PreToolUse heartbeat included.
func (d *Daemon) applyWorkEvent(pane *Pane, e PaneEvent) hookevents.Transition {
	pane.workMu.Lock()
	defer pane.workMu.Unlock()
	tr := pane.Work.Apply(e.Type, e.Data, e.Timestamp)
	if tr.Now == hookevents.WorkWorking && pane.idleTimer != nil {
		// Working again inside the settle window: the earlier fall was a
		// pause between turns, not a completion.
		pane.idleTimer.Stop()
		pane.idleTimer = nil
	}
	if tr.FellIdle {
		if pane.idleTimer != nil {
			pane.idleTimer.Stop()
		}
		id := pane.ID
		pane.idleTimer = time.AfterFunc(agentIdleSettle, func() { d.settleIdle(id) })
	}
	if tr.Aborted {
		// A crash is not a completion, but nothing will ever fire for this
		// pane again: run the subscribers so a deferred notify-back or a task
		// waiting on this pane is not stranded.
		if pane.idleTimer != nil {
			pane.idleTimer.Stop()
			pane.idleTimer = nil
		}
		subs := pane.idleSubs
		pane.idleSubs = nil
		go func() {
			for _, fn := range subs {
				fn()
			}
		}()
	}
	return tr
}

// settleIdle runs when the settle window elapses. It re-checks the ledger —
// the timer may have raced a start edge — then emits agent_idle and runs the
// idle subscribers once.
func (d *Daemon) settleIdle(paneID string) {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return
	}
	pane.workMu.Lock()
	pane.idleTimer = nil
	if pane.Work.State() != hookevents.WorkIdle {
		pane.workMu.Unlock()
		return
	}
	subs := pane.idleSubs
	pane.idleSubs = nil
	pane.workMu.Unlock()

	pane.PluginMu.Lock()
	name := pane.Name
	pane.PluginMu.Unlock()
	d.emitEvent(withExcerpt(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     pane.TabID,
		PaneName:  name,
		Type:      "agent_idle",
		Title:     "Turn finished",
		Severity:  "info",
		Timestamp: time.Now(),
		Data:      map[string]string{},
	}, paneOutputExcerpt(pane, 5)))
	for _, fn := range subs {
		fn()
	}
}

// onPaneIdle registers fn to run once the pane next settles idle. If the pane
// is ALREADY idle (settled or never worked), fn runs immediately on the
// caller's goroutine — a subscriber must not wait for an edge that already
// happened. Returns false when the pane does not exist.
func (d *Daemon) onPaneIdle(paneID string, fn func()) bool {
	pane := d.session.Pane(paneID)
	if pane == nil {
		return false
	}
	pane.workMu.Lock()
	state := pane.Work.State()
	settling := pane.idleTimer != nil
	if state != hookevents.WorkWorking && state != hookevents.WorkBlocked && !settling {
		pane.workMu.Unlock()
		fn()
		return true
	}
	pane.idleSubs = append(pane.idleSubs, fn)
	pane.workMu.Unlock()
	return true
}

// paneWorkState reads the ledger for the wire.
func paneWorkState(pane *Pane) (state, reason string, lastIdle int64) {
	pane.workMu.Lock()
	defer pane.workMu.Unlock()
	state = string(pane.Work.State())
	reason = pane.Work.BlockedReason()
	if t := pane.Work.LastIdleAt(); !t.IsZero() {
		lastIdle = t.UnixMilli()
	}
	return state, reason, lastIdle
}
