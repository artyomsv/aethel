package tui

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// State sweep for the production TUI stall.
//
// The live-heap reproduction (frame_gc_repro_test.go) falsified GC. What the
// perf log says next is that in a slow window View is ~8x slower AND Update is
// ~8x slower, on the same work — so whatever it is raises the cost of the whole
// frame rather than one renderer stage. The slow windows also come in BURSTS
// (runs of up to 33 consecutive 5 s windows, where random scatter at the
// observed 5.8% rate predicts runs of 4 at most), which is the shape of a STATE
// the model enters and later leaves, not of a random event.
//
// This sweep drives identical frames against one model state at a time and
// reports each state's frame cost against a baseline. It is looking for a state
// that costs an order of magnitude, because that is the size of the effect the
// production log shows. A state that costs 20% is not the answer.
//
// Same gate as the heap reproduction, and deliberately much cheaper: the heap
// is irrelevant here (that is what the other test established), so the
// scrollback is shallow and the run is short.
const stateSweepEnv = "QUIL_STATE_SWEEP"

// sweepFrames is per state. Short, because this is a search for an
// order-of-magnitude effect rather than a measurement of a small one.
const sweepFrames = 1500

// sweepState is one model configuration to measure. mutate runs after the
// workspace is built and before any frame is drawn.
type sweepState struct {
	name string
	// panes overrides the workspace size for this state. Zero means reproPanes.
	panes  int
	mutate func(m *Model, panes []*PaneModel)
}

// TestFrameCostByModelState measures the frame cost of each model state a
// production TUI can be sitting in for minutes at a time.
//
// It asserts nothing: absolute frame cost in a container is not the user's, and
// the question is comparative. What it reports is each state's p50 and max
// against the baseline, so a state that multiplies frame cost stands out
// without a threshold anyone has to justify.
func TestFrameCostByModelState(t *testing.T) {
	if os.Getenv(stateSweepEnv) == "" {
		t.Skipf("set %s=1 to run the model-state frame sweep", stateSweepEnv)
	}

	states := []sweepState{
		{name: "baseline", mutate: func(*Model, []*PaneModel) {}},

		// Every pane parked waiting on the user. This is what the daemon
		// announces in the mass "Waiting for input" bursts that precede the
		// longest stalls in the log — 30 panes flipping within three seconds.
		{name: "all-panes-blocked", mutate: func(m *Model, panes []*PaneModel) {
			for _, p := range panes {
				p.blockedSince = time.Now()
				p.blockedReason = "Bash"
				p.working = false
				p.turnActive = false
			}
		}},

		// Work finished unnoticed on every pane: green borders plus the
		// sidebar's per-pane attention marks.
		{name: "all-panes-unseen", mutate: func(m *Model, panes []*PaneModel) {
			for _, p := range panes {
				p.unseen = true
			}
		}},

		// The notification sidebar open and full. It is composited over the
		// frame on EVERY View, so a costly one is paid per frame for as long as
		// the user leaves it open — which is the burst shape.
		{name: "notif-sidebar-open-full", mutate: func(m *Model, panes []*PaneModel) {
			m.notifications.visible = true
			for i := 0; i < 50; i++ {
				m.notifications.AddEvent(ipc.PaneEventPayload{
					ID:        fmt.Sprintf("ev-%d", i),
					PaneID:    panes[i%len(panes)].ID,
					PaneName:  fmt.Sprintf("pane %d", i),
					Type:      "output_idle",
					Title:     "Waiting for input",
					Message:   "claude-code is waiting for input on a long-running turn",
					Severity:  "info",
					Timestamp: time.Now().Unix(),
				})
			}
		}},

		// Scrolled back into history. maxScroll and the scrollback slice are
		// walked per frame, and the pane render cache keys on scrollBack, so a
		// user parked in history rebuilds from a different code path than the
		// live-tail one every other measurement here uses.
		{name: "active-pane-scrolled-back", mutate: func(m *Model, panes []*PaneModel) {
			// maxScroll(), not a literal: a literal larger than the buffer
			// still works (ScrollUp clamps) but reads like a bug to the next
			// person, and it silently stops meaning "as far back as it goes"
			// the moment sweepScrollback changes.
			p := m.activeTabModel().Leaves()[0]
			p.ScrollUp(p.maxScroll())
		}},

		// A selection held open across frames. renderContent honours it per
		// row, and it invalidates the pane cache while it is being dragged.
		{name: "selection-held", mutate: func(m *Model, panes []*PaneModel) {
			active := m.activeTabModel().Leaves()[0]
			m.selection = &Selection{
				PaneID: active.ID,
				Anchor: SelectionAnchor{Line: 0, Col: 0},
				Cursor: SelectionAnchor{Line: 40, Col: 150},
			}
		}},

		// Wide canvas OFF, which puts the pane into the wrapped-preview render
		// instead of the native one.
		//
		// The emulator is deliberately left at the wide size buildReproModel
		// gave it. In production, clearing WideCanvas also resizes the VT, so
		// this arm measures a preview render over a WIDER grid than production
		// would have — the pessimistic side, which is the right one for a
		// search whose question is "can any state cost an order of magnitude".
		{name: "wide-canvas-off", mutate: func(m *Model, panes []*PaneModel) {
			for _, p := range panes {
				p.WideCanvas = false
			}
		}},

		// Twice the workspace. The sidebar and tab bar walk every tab in the
		// workspace on every frame, so this is the one state whose cost is
		// known in advance to grow — it is the control that proves the sweep
		// can detect a real effect at all.
		{name: "96-tabs", panes: reproPanes * 2, mutate: func(*Model, []*PaneModel) {}},
	}

	type row struct {
		name           string
		panes          int
		p50, p99, peak time.Duration
	}
	var rows []row
	var basis time.Duration

	for _, st := range states {
		panesWanted := reproPanes
		if st.panes > 0 {
			panesWanted = st.panes
		}
		m, panes := buildReproModel(t, sweepScrollback, panesWanted)
		st.mutate(&m, panes)

		// Settle before measuring, as the heap reproduction does. Building and
		// filling a fresh 48-pane workspace is a large allocation burst, and
		// the previous state's disposed one is still garbage: without this, a
		// collection caused by setup — or by the state before — lands inside
		// this state's frames and shows up in ITS p99 and max, which are then
		// compared against a baseline that did not pay for it. Two passes
		// because the first can resurrect finalizable objects into the second's
		// work.
		runtime.GC()
		runtime.GC()

		run := driveFrames(t, &m, sweepFrames)
		s := run.sorted
		r := row{name: st.name, panes: panesWanted, p50: pct(s, 50), p99: pct(s, 99), peak: s[len(s)-1]}
		if st.name == "baseline" {
			basis = r.p50
		}
		rows = append(rows, r)

		// Dispose AND collect. Each pane's emulator is held live by its own
		// drainVTResponses goroutine, so dropping the Model frees nothing;
		// leaving the corpse for the next state to collect is what puts one
		// state's garbage inside another state's measurement.
		for _, p := range panes {
			p.Dispose()
		}
		runtime.GC()
		debug.FreeOSMemory()
	}

	// The multiplier column is meaningless without a baseline, and a baseline
	// only exists if the "baseline" state ran. Reordering the slice would
	// otherwise silently print "—" for every row above it and no one would be
	// told the comparison had stopped happening.
	if basis <= 0 {
		t.Fatalf("no baseline p50 recorded — the sweep needs a state named %q", "baseline")
	}

	t.Logf("%d frames per state, %dx%d terminal (pane count per row)",
		sweepFrames, reproTermWidth, reproTermHeight)
	t.Logf("%-28s %6s %10s %10s %10s %8s", "state", "panes", "p50", "p99", "max", "vs base")
	for _, r := range rows {
		t.Logf("%-28s %6d %10v %10v %10v %7.2fx", r.name, r.panes,
			r.p50.Round(time.Microsecond), r.p99.Round(time.Microsecond),
			r.peak.Round(time.Microsecond), float64(r.p50)/float64(basis))
	}
}

// sweepScrollback is the per-pane depth every state is built at.
//
// Shallow deliberately: the heap reproduction established that scrollback depth
// does not move frame cost, so paying 16 s of fill per state here would buy
// nothing and would make the sweep too slow to iterate on.
const sweepScrollback = 200
