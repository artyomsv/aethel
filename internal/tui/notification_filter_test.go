package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// showOnly builds a filter that shows exactly the named groups and hides the
// rest. Explicit rather than derived from a config literal so a test reads as
// what it exercises.
func showOnly(groups ...string) eventGroupFilter {
	f := eventGroupFilter{}
	for _, g := range []string{
		groupAgentTurn, groupAgentBlocked, groupAgentSubagent, groupAgentSession,
		groupProcess, groupPane, groupMCP, groupSystem, groupCommands, groupIdle,
	} {
		f[g] = false
	}
	for _, g := range groups {
		f[g] = true
	}
	return f
}

func TestNotificationCenter_HiddenGroupIsStoredButNotVisible(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))

	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle", Title: "Output idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-2", Type: "process_exit", Title: "Process exited"})

	if len(nc.events) != 2 {
		t.Fatalf("stored events: got %d, want 2 (AddEvent must store everything)", len(nc.events))
	}
	vis := nc.visibleEvents()
	if len(vis) != 1 || vis[0].ID != "TEST-2" {
		t.Fatalf("visibleEvents: got %+v, want only TEST-2", vis)
	}
	if nc.Count() != 1 {
		t.Errorf("Count: got %d, want 1 (the visible count)", nc.Count())
	}
}

// Turning a group back on must reveal the events that arrived while it was
// off. Storing the filtered set instead of filtering at read time would make
// this silently impossible.
func TestNotificationCenter_EnablingGroupRevealsPastEvents(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle"})

	if nc.Count() != 0 {
		t.Fatalf("Count before enabling: got %d, want 0", nc.Count())
	}
	nc.SetGroups(showOnly(groupProcess, groupIdle))
	if nc.Count() != 1 {
		t.Errorf("Count after enabling idle: got %d, want 1", nc.Count())
	}
}

func TestNotificationCenter_ShowAllOverridesFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle"})

	nc.HandleKey("a")
	if nc.Count() != 1 {
		t.Errorf("Count with showAll: got %d, want 1", nc.Count())
	}
	nc.HandleKey("a")
	if nc.Count() != 0 {
		t.Errorf("Count after toggling showAll off: got %d, want 0", nc.Count())
	}
}

// The regression this refactor is most likely to introduce. DismissSelected
// used to slice nc.events by cursor directly; with anything hidden, that
// dismisses a different event than the one under the cursor.
func TestNotificationCenter_DismissSelected_ResolvesThroughFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))

	// Stored newest-first after these calls: v2, h2, v1, h1.
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h1", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v1", Type: "process_exit"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h2", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v2", Type: "process_exit"})

	nc.cursor = 1 // the second VISIBLE event, which is TEST-v1

	if got := nc.DismissSelected(); got != "TEST-v1" {
		t.Fatalf("DismissSelected: got %q, want %q", got, "TEST-v1")
	}
	for _, e := range nc.events {
		if e.ID == "TEST-v1" {
			t.Fatal("TEST-v1 is still stored after being dismissed")
		}
	}
	if len(nc.events) != 3 {
		t.Errorf("stored events after dismiss: got %d, want 3", len(nc.events))
	}
}

func TestNotificationCenter_SelectedEvent_ResolvesThroughFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v", Type: "process_exit"})

	e := nc.SelectedEvent()
	if e == nil || e.ID != "TEST-v" {
		t.Fatalf("SelectedEvent: got %+v, want TEST-v", e)
	}
}

// Enter must navigate to the pane of the VISIBLE selection, not of whatever
// sits at that index in the stored slice.
func TestNotificationCenter_HandleKeyEnter_ResolvesThroughFilter(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h", PaneID: "pane-hidden", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v", PaneID: "pane-visible", Type: "process_exit"})

	action, eventID, paneID := nc.HandleKey("enter")
	if action != "navigate" {
		t.Fatalf("action: got %q, want %q", action, "navigate")
	}
	if eventID != "TEST-v" || paneID != "pane-visible" {
		t.Errorf("navigate target: got (%q, %q), want (TEST-v, pane-visible)", eventID, paneID)
	}
}

// AddEvent's aggregation branch chases the cursor's event through the
// move-to-front. It must chase it through the VISIBLE list, because that is
// what the cursor indexes — reading or writing the stored index there jumps
// the selection to an unrelated card as soon as anything is hidden.
func TestNotificationCenter_Aggregation_KeepsCursorOnSameVisibleEvent(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))

	// Stored newest-first: v2, h1, v1. Visible: v2, v1.
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v1", Type: "process_exit"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h1", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v2", Type: "process_exit"})

	nc.cursor = 1 // the user is reading TEST-v1
	if e := nc.SelectedEvent(); e == nil || e.ID != "TEST-v1" {
		t.Fatalf("fixture: SelectedEvent = %+v, want TEST-v1", e)
	}

	// A repeat of the HIDDEN event aggregates and moves to the front. The
	// user's selection must not move with it.
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h1", Type: "output_idle", Title: "again"})

	if e := nc.SelectedEvent(); e == nil || e.ID != "TEST-v1" {
		t.Errorf("SelectedEvent after an aggregation: got %+v, want TEST-v1", e)
	}
}

// Cursor navigation must be bounded by the VISIBLE list, not the stored one.
func TestNotificationCenter_CursorBoundedByVisible(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h1", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-h2", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-v", Type: "process_exit"})

	for i := 0; i < 5; i++ {
		nc.HandleKey("down")
	}
	if nc.cursor != 0 {
		t.Errorf("cursor after 5 downs over a 1-event visible list: got %d, want 0", nc.cursor)
	}
}

// Hiding the group the cursor was parked in must not leave the cursor past the
// end of the visible list.
func TestNotificationCenter_SetGroups_ClampsCursor(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.SetGroups(showOnly(groupProcess, groupIdle))
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-1", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-2", Type: "output_idle"})
	nc.AddEvent(ipc.PaneEventPayload{ID: "TEST-3", Type: "process_exit"})
	nc.cursor = 2

	nc.SetGroups(showOnly(groupProcess))

	if nc.cursor != 0 {
		t.Errorf("cursor after hiding idle: got %d, want 0", nc.cursor)
	}
	if e := nc.SelectedEvent(); e == nil || e.ID != "TEST-3" {
		t.Errorf("SelectedEvent after clamp: got %+v, want TEST-3", e)
	}
}
