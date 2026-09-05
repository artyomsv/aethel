package daemon

import "testing"

func TestNotifyPaneDestroyed_CarriesActorAndName(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "build"}

	d.notifyPaneDestroyed(pane, "mcp")

	evs := d.events.Events()
	if len(evs) != 1 {
		t.Fatalf("events: got %d, want 1", len(evs))
	}
	if evs[0].Type != "pane_destroyed" {
		t.Errorf("type: got %q, want %q", evs[0].Type, "pane_destroyed")
	}
	if evs[0].Data["by"] != "mcp" {
		t.Errorf("Data[by]: got %q, want %q", evs[0].Data["by"], "mcp")
	}
	// Read before the pane leaves the session maps, or the card names nothing.
	if evs[0].PaneName != "build" {
		t.Errorf("PaneName: got %q, want %q", evs[0].PaneName, "build")
	}
	if evs[0].Title != "Pane closed by MCP agent" {
		t.Errorf("Title: got %q, want %q", evs[0].Title, "Pane closed by MCP agent")
	}
}

func TestNotifyPaneDestroyed_UserActor(t *testing.T) {
	d := newNotifyTestDaemon(t)
	d.notifyPaneDestroyed(&Pane{ID: "pane-test-1", Name: "shell"}, "user")

	evs := d.events.Events()
	if len(evs) != 1 || evs[0].Title != "Pane closed" {
		t.Fatalf("events: %+v, want one titled %q", evs, "Pane closed")
	}
	if evs[0].Data["by"] != "user" {
		t.Errorf("Data[by]: got %q, want %q", evs[0].Data["by"], "user")
	}
}

// An overlay pane is auto-destroyed on exit as ordinary lifecycle. A card per
// Alt+G toggle is exactly the telemetry this feature removes.
func TestNotifyPaneDestroyed_SkipsOverlay(t *testing.T) {
	d := newNotifyTestDaemon(t)
	d.notifyPaneDestroyed(&Pane{ID: "pane-test-1", Name: "lazygit", Overlay: true}, "user")

	if got := d.events.Count(); got != 0 {
		t.Errorf("events for an overlay pane: got %d, want 0", got)
	}
}

func TestNotifyPaneDestroyed_NilPane(t *testing.T) {
	d := newNotifyTestDaemon(t)
	d.notifyPaneDestroyed(nil, "user")
	if got := d.events.Count(); got != 0 {
		t.Errorf("events for a nil pane: got %d, want 0", got)
	}
}

func TestNotifyPaneMark_Types(t *testing.T) {
	cases := []struct{ eventType, title string }{
		{"pane_pinned", "Pane pinned for attention"},
		{"pane_unpinned", "Pane attention cleared"},
		{"pane_marked_deletion", "Pane marked for deletion"},
		{"pane_unmarked_deletion", "Pane deletion mark cleared"},
	}
	for _, c := range cases {
		d := newNotifyTestDaemon(t)
		d.notifyPaneMark(&Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "api"}, c.eventType, c.title)

		evs := d.events.Events()
		if len(evs) != 1 {
			t.Fatalf("%s: events got %d, want 1", c.eventType, len(evs))
		}
		if evs[0].Type != c.eventType || evs[0].Title != c.title {
			t.Errorf("got (%q, %q), want (%q, %q)", evs[0].Type, evs[0].Title, c.eventType, c.title)
		}
		if evs[0].PaneName != "api" {
			t.Errorf("%s: PaneName got %q, want %q", c.eventType, evs[0].PaneName, "api")
		}
	}
}

// Four explicit types rather than two with a boolean in Data: the queue
// aggregates by (PaneID, Title), so a pin and an unpin sharing a title would
// collapse into one card wearing the wrong label.
func TestNotifyPaneMark_PinAndUnpinDoNotAggregate(t *testing.T) {
	d := newNotifyTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", Name: "api"}

	d.notifyPaneMark(pane, "pane_pinned", "Pane pinned for attention")
	d.notifyPaneMark(pane, "pane_unpinned", "Pane attention cleared")

	if got := d.events.Count(); got != 2 {
		t.Errorf("events for a pin then an unpin: got %d, want 2", got)
	}
}

func TestNotifyWorktreeReady_CarriesBranch(t *testing.T) {
	d := newNotifyTestDaemon(t)
	d.notifyWorktreeReady(&Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "claude"}, "feat/example")

	evs := d.events.Events()
	if len(evs) != 1 {
		t.Fatalf("events: got %d, want 1", len(evs))
	}
	if evs[0].Type != "worktree_ready" {
		t.Errorf("type: got %q, want %q", evs[0].Type, "worktree_ready")
	}
	if evs[0].Data["branch"] != "feat/example" {
		t.Errorf("Data[branch]: got %q, want %q", evs[0].Data["branch"], "feat/example")
	}
	if evs[0].Title != "Worktree ready: feat/example" {
		t.Errorf("Title: got %q, want %q", evs[0].Title, "Worktree ready: feat/example")
	}
}

func TestNotifyWorktreeReady_NilPane(t *testing.T) {
	d := newNotifyTestDaemon(t)
	d.notifyWorktreeReady(nil, "feat/example")
	if got := d.events.Count(); got != 0 {
		t.Errorf("events for a nil pane: got %d, want 0", got)
	}
}
