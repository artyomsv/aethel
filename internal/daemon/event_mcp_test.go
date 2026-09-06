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
	pane.LastMCPEventAt["MCP agent typed here"] = time.Now().Add(-mcpControlCooldown - time.Second)
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

// The cooldown is keyed BY TITLE, and this is the case that forced it.
//
// An agent typically types into a pane and then restarts it, both well inside
// 30 s. With one timestamp per pane the restart — the rarer and more
// consequential act — was dropped every time, because the chatty one got there
// first. Nothing here resets the cooldown: the two calls are back to back,
// exactly as the production call sites make them.
func TestNotifyMCPControl_RestartIsNotSwallowedByARecentType(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "shell"}

	d.notifyMCPControl(pane, "MCP agent typed here")
	d.notifyMCPControl(pane, "MCP agent restarted this pane")

	evs := d.events.Events()
	if len(evs) != 2 {
		t.Fatalf("events for a type followed by a restart: got %d, want 2", len(evs))
	}
	titles := map[string]bool{evs[0].Title: true, evs[1].Title: true}
	if !titles["MCP agent typed here"] || !titles["MCP agent restarted this pane"] {
		t.Errorf("titles: got %v, want both acts represented", titles)
	}
}

// The same title inside the window is still suppressed — the cooldown is
// per-title, not abolished.
func TestNotifyMCPControl_SameTitleStillCoolsDown(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "shell"}

	d.notifyMCPControl(pane, "MCP agent typed here")
	d.notifyMCPControl(pane, "MCP agent typed here")
	d.notifyMCPControl(pane, "MCP agent typed here")

	if got := d.events.Count(); got != 1 {
		t.Errorf("events for three identical acts inside the window: got %d, want 1", got)
	}
}
