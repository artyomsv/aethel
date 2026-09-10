package hookevents

import (
	"strconv"
	"time"
)

// WorkState is the daemon-side answer to "what is this agent pane doing".
//
// Derived, never stored: a WorkLedger replays the same hook edges the TUI's
// spinner replays (internal/tui/workstate.go) and reports the level. The TUI
// keeps its own copy of the derivation because its version is entangled with
// rendering state (spinner frame, the unseen mark, toasts); this one is the
// pure core, held per pane by the daemon so a client with no TUI attached — an
// MCP bridge — can ask.
type WorkState string

const (
	// WorkUnknown: no classified edge has been seen. A terminal pane stays
	// here forever, and so does an AI pane whose hooks never loaded — which is
	// why the zero value is not "idle": an unknown is not a promise that the
	// pane is free.
	WorkUnknown WorkState = ""
	WorkWorking WorkState = "working"
	WorkBlocked WorkState = "blocked"
	WorkIdle    WorkState = "idle"
)

// maxTrackedSubagents bounds the per-pane subagent ledger. agent_type is
// producer-controlled, so key cardinality is too, in a process that runs for
// weeks. Past the ceiling the ledger records that it lost track (overflow)
// rather than dropping the agent silently — see Apply.
const maxTrackedSubagents = 64

// subagentFailureEvent is the one turn-ending event that can belong to a
// SUBAGENT rather than the main turn; the producer names the agent when it
// does. See the StopFailure arm in Apply.
const subagentFailureEvent = "hook.claude.StopFailure"

// Transition reports what one Apply changed.
type Transition struct {
	Was, Now WorkState
	// FellIdle is the falling edge of "working": turn over AND every tracked
	// subagent drained. This is the edge "the task finished" hangs off — NOT
	// hook.claude.Stop, which fires while background subagents are still
	// running (measured: 3 Stops against 1 UserPromptSubmit on one pane).
	FellIdle bool
	// Aborted marks a falling edge caused by process exit. Not a completion.
	Aborted bool
}

// WorkLedger holds one pane's derivation state. The zero value is ready.
type WorkLedger struct {
	seen             bool
	turnActive       bool
	subagents        map[string]int
	subagentOverflow bool
	blockedSince     time.Time
	blockedReason    string
	lastIdleAt       time.Time
	aborted          bool
}

// State returns the current level. Precedence: blocked > working > idle,
// matching the sidebar's counts() order.
func (l *WorkLedger) State() WorkState {
	if !l.seen {
		return WorkUnknown
	}
	if !l.blockedSince.IsZero() {
		return WorkBlocked
	}
	if l.working() {
		return WorkWorking
	}
	return WorkIdle
}

// BlockedReason names the tool a park was raised for, when the producer said.
func (l *WorkLedger) BlockedReason() string { return l.blockedReason }

// LastIdleAt is when the pane last fell idle (zero if never).
func (l *WorkLedger) LastIdleAt() time.Time { return l.lastIdleAt }

// Aborted reports that the last thing this pane did was DIE — the process
// exited and no start edge has been seen since.
//
// State() cannot answer that question, and the shape of the ledger is why: an
// abort clears the turn, the subagents and the park, so the level it leaves
// behind is exactly WorkIdle, and LastIdleAt is stamped as for any other
// falling edge. A consumer asking "is this pane free" would therefore read a
// crashed pane as a settled one — which for anything that DELIVERS on idle
// (the task completion callback, the notify-back) is the difference between
// reporting a crash and reporting a completed turn. Transition.Aborted says
// the same thing about ONE event; this says it about the pane's current
// standing, which is what a subscriber arriving after that event needs.
//
// Sticky until the pane demonstrably works again: only a start edge clears it,
// because only that is evidence of a live child. A stop edge is not — a dead
// pane's last queued Stop must not erase the fact.
func (l *WorkLedger) Aborted() bool { return l.aborted }

func (l *WorkLedger) working() bool {
	return l.turnActive || len(l.subagents) > 0 || l.subagentOverflow
}

// Apply feeds one PaneEvent type (with its Data) and returns the transition.
// The switch mirrors internal/tui/workstate.go's applyWorkTransition; keep the
// two in step, or the daemon's answer and the sidebar will disagree about the
// same pane.
func (l *WorkLedger) Apply(eventType string, data map[string]string, now time.Time) Transition {
	kind := ClassifyWorkEvent(eventType)
	was := l.State()
	if kind == WorkEventNone {
		return Transition{Was: was, Now: was}
	}
	l.seen = true
	wasWorking := l.working()
	abort := false

	switch kind {
	case WorkEventStart:
		l.turnActive = true
		l.blockedSince = time.Time{}
		l.blockedReason = ""
		// The pane is working, so whatever exited before it is history — this
		// is the one edge that proves a live child. See Aborted().
		l.aborted = false
	case WorkEventSubagentStart:
		agentType := data["agent_type"]
		if agentType == "" {
			// A start must NAME the agent: the empty key is exactly the one
			// the unpaired end-of-turn stop carries, so admitting it here
			// would let that phantom drain real work.
			break
		}
		_, live := l.subagents[agentType]
		if !live && len(l.subagents) >= maxTrackedSubagents {
			// Sticky until a terminal edge: we never learn that an untracked
			// agent finished, so wrong-on is the safe direction.
			l.subagentOverflow = true
			break
		}
		if l.subagents == nil {
			l.subagents = make(map[string]int, 1)
		}
		l.subagents[agentType] += coalescedCount(data)
	case WorkEventSubagentStop:
		l.drainSubagent(data["agent_type"], coalescedCount(data))
	case WorkEventStop, WorkEventStopFinal:
		if eventType == subagentFailureEvent && data["agent_type"] != "" {
			// A SUBAGENT's failure: the only ending that agent gets (it emits
			// no SubagentStop), so this is the stop that would have been.
			l.drainSubagent(data["agent_type"], coalescedCount(data))
			break
		}
		l.turnActive = false
		// A completed turn is by definition not blocked; approving a prompt
		// fires no hook, so the turn's Stop is the only edge that un-blocks.
		l.blockedSince = time.Time{}
		l.blockedReason = ""
		if kind == WorkEventStopFinal {
			clear(l.subagents)
			l.subagentOverflow = false
		}
	case WorkEventPark:
		l.blockedSince = now
		if tool := data["tool"]; tool != "" {
			l.blockedReason = tool
		}
	case WorkEventNotify:
		// Park unless the producer marked this as the idle nudge AND the turn
		// is already over. See ClassifyWorkEvent's WorkEventNotify comment.
		if data[DataNotifyKind] != NotifyKindIdle || l.turnActive {
			l.blockedSince = now
			if tool := data["tool"]; tool != "" {
				l.blockedReason = tool
			}
		}
	case WorkEventAbort:
		l.turnActive = false
		clear(l.subagents)
		l.subagentOverflow = false
		l.blockedSince = time.Time{}
		l.blockedReason = ""
		abort = true
		l.aborted = true
	}

	tr := Transition{Was: was, Now: l.State(), Aborted: abort}
	if wasWorking && !l.working() {
		l.lastIdleAt = now
		tr.FellIdle = !abort
	}
	return tr
}

func (l *WorkLedger) drainSubagent(agentType string, n int) {
	outstanding, live := l.subagents[agentType]
	if !live {
		return
	}
	outstanding -= n
	if outstanding <= 0 {
		delete(l.subagents, agentType)
	} else {
		l.subagents[agentType] = outstanding
	}
}

// coalescedCount extracts the ingester's burst count ("coalesced" = events
// merged into this one), defaulting to 1.
func coalescedCount(data map[string]string) int {
	n, err := strconv.Atoi(data["coalesced"])
	if err != nil || n < 1 {
		return 1
	}
	return n
}
