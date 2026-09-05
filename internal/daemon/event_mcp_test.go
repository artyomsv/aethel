package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// newNotifyTestDaemon builds a Daemon with only the fields these tests touch.
//
// It has no IPC server, so every broadcast is the documented nil-server no-op,
// and its SessionManager is empty, so emitEvent's mute lookup finds no pane and
// falls through to the queue — which is exactly the path under test.
func newNotifyTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		events:  newEventQueue(50),
		session: NewSessionManager(1024),
		hellos:  newHelloRegistry(),
	}
}

func TestHelloRegistry_RoleOf(t *testing.T) {
	r := newHelloRegistry()
	tuiConn := &ipc.Conn{}
	bridgeConn := &ipc.Conn{}
	silent := &ipc.Conn{}

	r.put(tuiConn, ipc.ClientHelloPayload{Role: "tui"})
	r.put(bridgeConn, ipc.ClientHelloPayload{Role: "bridge"})

	if got := r.roleOf(tuiConn); got != "tui" {
		t.Errorf("roleOf(tui): got %q, want %q", got, "tui")
	}
	if got := r.roleOf(bridgeConn); got != "bridge" {
		t.Errorf("roleOf(bridge): got %q, want %q", got, "bridge")
	}
	if got := r.roleOf(silent); got != "" {
		t.Errorf("roleOf(a conn that never said hello): got %q, want %q", got, "")
	}
	if got := r.roleOf(nil); got != "" {
		t.Errorf("roleOf(nil): got %q, want %q", got, "")
	}
}

func TestNotifyMCPControl_EmitsOncePerCooldown(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "shell"}

	d.notifyMCPControl(pane, "MCP agent typed here")
	if got := d.events.Count(); got != 1 {
		t.Fatalf("events after the first call: got %d, want 1", got)
	}

	// Inside the cooldown window: no second card. An agent driving a pane
	// sends many messages per turn, and the card says "an agent is on this
	// pane", which is a state rather than a per-keystroke fact.
	d.notifyMCPControl(pane, "MCP agent typed here")
	if got := d.events.Count(); got != 1 {
		t.Errorf("events after a second call inside the cooldown: got %d, want 1", got)
	}

	// Past the cooldown a new event is emitted. The queue aggregates by
	// (PaneID, Title) and reuses the prior ID, so the observable difference is
	// the count in Data, not the queue length.
	pane.PluginMu.Lock()
	pane.LastMCPEventAt = time.Now().Add(-mcpControlCooldown - time.Second)
	pane.PluginMu.Unlock()

	d.notifyMCPControl(pane, "MCP agent typed here")
	evs := d.events.Events()
	if len(evs) != 1 {
		t.Fatalf("events: got %d, want 1 (aggregated)", len(evs))
	}
	if evs[0].Data["count"] != "2" {
		t.Errorf("aggregation count: got %q, want %q", evs[0].Data["count"], "2")
	}
	if evs[0].Type != "mcp_control" {
		t.Errorf("event type: got %q, want %q", evs[0].Type, "mcp_control")
	}
	if evs[0].PaneName != "shell" {
		t.Errorf("pane name: got %q, want %q", evs[0].PaneName, "shell")
	}
}

// A restart is a different act from typing, so it carries a different title —
// and because the queue aggregates by (PaneID, Title), a different title is
// also what keeps the two from collapsing into one card.
func TestNotifyMCPControl_DistinctTitlesDoNotAggregate(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "shell"}

	d.notifyMCPControl(pane, "MCP agent typed here")
	pane.PluginMu.Lock()
	pane.LastMCPEventAt = time.Time{}
	pane.PluginMu.Unlock()
	d.notifyMCPControl(pane, "MCP agent restarted this pane")

	if got := d.events.Count(); got != 2 {
		t.Errorf("events for two different acts: got %d, want 2", got)
	}
}
