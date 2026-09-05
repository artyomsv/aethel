# Notification Timeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn Quil's notification sidebar from a stream of daemon telemetry into a scrollable, clickable, user-filtered timeline of work events.

**Architecture:** Every event type maps to one of ten named groups. A client-side filter, configured from a new `F1 → Settings → Notifications` screen, decides which groups the sidebar shows. The daemon event queue and the three MCP notification tools stay unfiltered, so agents keep receiving everything a human has hidden. Rendering moves to a single pure geometry function shared by the painter and the mouse hit test, which makes variable-height cards and line-based scrolling safe.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2 (`charm.land/lipgloss/v2`), TOML via `BurntSushi/toml`. Build and test through `./scripts/dev.sh` (Docker; the host has no Go toolchain).

**Spec:** `docs/superpowers/specs/2026-09-06-notification-timeline-design.md`

## Global Constraints

- **Go 1.25**, module path `github.com/artyomsv/quil`.
- **Build and test only through `./scripts/dev.sh`.** Go and make are not installed on the host.
- **`./scripts/dev.sh test` takes exactly ONE package argument.** Extra arguments are silently dropped, so a three-package run tests one package and greps clean. Run it once per package.
- **Never touch the production daemon or `~/.quil/`.** All runtime verification uses dev mode (`./scripts/quil-dev.ps1`, state in `./.quil/`). See `.claude/rules/dev-environment.md`.
- **Indentation: tabs** (Go standard, `gofmt`). Only run `gofmt` on files you touched — a directory-wide run rewrites line endings on ~200 files.
- **Commit messages:** imperative mood, max 72 characters on the first line. **Never** mention AI, model names, vendors, or add `Co-Authored-By` trailers.
- **No synthetic data.** Test fixtures use obviously-synthetic values (`TEST-`, `pane-test-1`); nothing plausible enough to be mistaken for a real observation.
- **The daemon `eventQueue` does not change.** No new IPC message types, no change to its bound, its `(PaneID, Title)` aggregation, or the attach replay.
- **The three MCP notification tools do not change.** `get_notifications`, `watch_notifications` and `dismiss_notifications` stay unfiltered.
- **`hookevents.IsWorkStateOnly` does not change.** `PreToolUse` / `PostToolUse` stay out of the queue entirely.
- **Daemon locking rule:** `emitEvent` re-locks `Pane.PluginMu` for its mute check and Go mutexes are not reentrant. Every new emitter must release `PluginMu` **before** calling `emitEvent`. Violating this reproduces the 2026-06-12 daemon-wide freeze.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/config/config.go` | `EventGroupsConfig` struct, field on `NotificationConfig`, defaults in `Default()` |
| `internal/tui/notification_class.go` | **new** — event `Type` → group name, group name constants, config → filter map |
| `internal/tui/notification.go` | `NotificationCenter`: storage, filter, cursor, scroll, geometry, rendering |
| `internal/tui/model.go` | dialog constant, mouse click/wheel routing, badge count, pane locator |
| `internal/tui/dialog.go` | `submenu` flag on `settingsField`, the new Notifications screen |
| `internal/daemon/procreport.go` | `helloRegistry.roleOf` |
| `internal/daemon/session.go` | `Pane.LastMCPEventAt` |
| `internal/daemon/daemon.go` | `mcp_control`, pin/mark events, `notifyPaneDestroyed` |
| `internal/daemon/worktree_add.go` | `worktree_ready` |

Test files sit beside their subject with a `_test.go` suffix, per the package's existing convention (`notification_test.go`, `event_test.go`).

---

## Task 1: Config — event group settings

**Files:**
- Modify: `internal/config/config.go` (add struct near `NotificationConfig:113`, field on `NotificationConfig`, defaults in `Default():512`)
- Test: `internal/config/eventgroups_test.go` (create)

**Interfaces:**
- Consumes: nothing.
- Produces: `config.EventGroupsConfig` with exported bool fields `AgentTurn`, `AgentBlocked`, `AgentSubagent`, `AgentSession`, `Process`, `Pane`, `MCP`, `System`, `Commands`, `Idle`; reachable as `cfg.Notification.Events`.

- [ ] **Step 1: Write the failing test**

Create `internal/config/eventgroups_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestDefault_EventGroups(t *testing.T) {
	got := config.Default().Notification.Events
	want := config.EventGroupsConfig{
		AgentTurn:     true,
		AgentBlocked:  true,
		AgentSubagent: true,
		AgentSession:  true,
		Process:       true,
		Pane:          true,
		MCP:           true,
		System:        true,
		Commands:      false,
		Idle:          false,
	}
	if got != want {
		t.Errorf("Default event groups:\n got %+v\nwant %+v", got, want)
	}
}

// A config.toml written before this feature existed has no [notification.events]
// section. Load starts from Default() and decodes over it, so every group must
// survive untouched — otherwise an upgrade blanks the sidebar.
func TestLoad_NoEventsSection_KeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[notification]\nsidebar_width = 40\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Notification.SidebarWidth != 40 {
		t.Errorf("sidebar_width: got %d, want 40", cfg.Notification.SidebarWidth)
	}
	if cfg.Notification.Events != config.Default().Notification.Events {
		t.Errorf("event groups: got %+v, want the defaults", cfg.Notification.Events)
	}
}

// A partial section must override only the keys it names.
func TestLoad_PartialEventsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[notification.events]\nidle = true\nagent_turn = false\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ev := cfg.Notification.Events
	if !ev.Idle {
		t.Error("idle: got false, want true (explicitly set)")
	}
	if ev.AgentTurn {
		t.Error("agent_turn: got true, want false (explicitly set)")
	}
	if !ev.AgentBlocked || !ev.Process || !ev.System {
		t.Errorf("unnamed groups lost their defaults: %+v", ev)
	}
	if ev.Commands {
		t.Error("commands: got true, want false (default)")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/config`

Expected: FAIL — `cfg.Notification.Events undefined` / `undefined: config.EventGroupsConfig`.

- [ ] **Step 3: Add the struct and the field**

In `internal/config/config.go`, add the `Events` field to `NotificationConfig`:

```go
type NotificationConfig struct {
	SidebarWidth int                     `toml:"sidebar_width"` // default 30
	MaxEvents    int                     `toml:"max_events"`    // default 200
	Hooks        HookNotificationsConfig `toml:"hooks"`
	Desktop      DesktopConfig           `toml:"desktop"`
	Events       EventGroupsConfig       `toml:"events"`
}
```

Add the struct immediately after `NotificationConfig`:

```go
// EventGroupsConfig selects which notification groups the TUI sidebar shows.
//
// Consumed ONLY by the client. The daemon event queue and the three MCP
// notification tools stay unfiltered by design: an agent polling for "has this
// pane gone quiet" wants output_idle, and a human does not, so one queue with a
// client-side filter serves both without either reader losing.
//
// Plain bools rather than pointers: Load starts from Default() and decodes over
// it, so an absent key already means "the default" and there is no third state
// to express. That is what keeps a config.toml written before this feature from
// blanking the sidebar on upgrade.
//
// Groups, not event types. Event types are internal strings that change
// whenever an upstream tool adds a hook (hook.claude.*, hook.codex.*), so a
// config keyed on them rots; a new type joins an existing group instead. The
// type -> group table lives at internal/tui/notification_class.go.
type EventGroupsConfig struct {
	AgentTurn     bool `toml:"agent_turn"`     // Working on… / Reply ready
	AgentBlocked  bool `toml:"agent_blocked"`  // permission prompts, waiting for you, bell
	AgentSubagent bool `toml:"agent_subagent"` // subagent + task start/stop
	AgentSession  bool `toml:"agent_session"`  // session end, compaction
	Process       bool `toml:"process"`        // process exited / failed
	Pane          bool `toml:"pane"`           // closed, pinned, marked for deletion
	MCP           bool `toml:"mcp"`            // an agent drove a pane
	System        bool `toml:"system"`         // input blocked, worktree ready, unknown types
	Commands      bool `toml:"commands"`       // every shell command (OSC 133)
	Idle          bool `toml:"idle"`           // output idle
}
```

- [ ] **Step 4: Add the defaults**

In `Default()`, inside the `Notification: NotificationConfig{…}` literal, after the `Hooks:` block:

```go
			// Commands and Idle default OFF. Both describe machine state
			// rather than news: output_idle fires for every quiet pane every
			// 30 s forever, and command_complete fires on every shell command.
			// Because eventQueue.Push aggregates by (PaneID, Title) and
			// re-prepends, a constant-title repeat jumps back to position 1 on
			// every fire — so left on, these two permanently occupy the rows
			// the sidebar can render. MCP consumers still receive them.
			Events: EventGroupsConfig{
				AgentTurn:     true,
				AgentBlocked:  true,
				AgentSubagent: true,
				AgentSession:  true,
				Process:       true,
				Pane:          true,
				MCP:           true,
				System:        true,
				Commands:      false,
				Idle:          false,
			},
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/config`

Expected: PASS, including the pre-existing config tests.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/eventgroups_test.go
git commit -m "feat(config): add notification event group settings"
```

---

## Task 2: Event classification table

**Files:**
- Create: `internal/tui/notification_class.go`
- Test: `internal/tui/notification_class_test.go` (create)

**Interfaces:**
- Consumes: `config.EventGroupsConfig` (Task 1).
- Produces:
  - group name constants `groupAgentTurn`, `groupAgentBlocked`, `groupAgentSubagent`, `groupAgentSession`, `groupProcess`, `groupPane`, `groupMCP`, `groupSystem`, `groupCommands`, `groupIdle` (all `string`)
  - `func eventGroup(eventType string) string`
  - `type eventGroupFilter map[string]bool`
  - `func groupFilterFrom(c config.EventGroupsConfig) eventGroupFilter`
  - `func (f eventGroupFilter) shows(eventType string) bool`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/notification_class_test.go`:

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestEventGroup_Table(t *testing.T) {
	cases := []struct {
		eventType string
		want      string
	}{
		// Hook events are matched on the last segment, so a fourth agent
		// source needs no table change.
		{"hook.claude.UserPromptSubmit", groupAgentTurn},
		{"hook.codex.Stop", groupAgentTurn},
		{"hook.claude.StopFailure", groupAgentTurn},
		{"hook.opencode.chat.message", groupAgentTurn},
		{"hook.opencode.session.idle", groupAgentTurn},
		{"hook.opencode.session.error", groupAgentTurn},
		{"hook.claude.PermissionRequest", groupAgentBlocked},
		{"hook.claude.Notification", groupAgentBlocked},
		{"hook.opencode.permission.ask", groupAgentBlocked},
		{"bell", groupAgentBlocked},
		{"hook.claude.SubagentStart", groupAgentSubagent},
		{"hook.codex.SubagentStop", groupAgentSubagent},
		{"hook.claude.TaskCreated", groupAgentSubagent},
		{"hook.claude.TaskCompleted", groupAgentSubagent},
		{"hook.claude.SessionEnd", groupAgentSession},
		{"hook.claude.PreCompact", groupAgentSession},
		{"hook.codex.PostCompact", groupAgentSession},
		{"process_exit", groupProcess},
		{"pane_destroyed", groupPane},
		{"pane_pinned", groupPane},
		{"pane_unpinned", groupPane},
		{"pane_marked_deletion", groupPane},
		{"pane_unmarked_deletion", groupPane},
		{"mcp_control", groupMCP},
		{"input_blocked", groupSystem},
		{"worktree_ready", groupSystem},
		{"command_complete", groupCommands},
		{"output_idle", groupIdle},
	}
	for _, c := range cases {
		if got := eventGroup(c.eventType); got != c.want {
			t.Errorf("eventGroup(%q) = %q, want %q", c.eventType, got, c.want)
		}
	}
}

// A newer daemon can send a type this build has never heard of. It must land
// in a group that DEFAULTS ON: a wrong extra card is visible and
// self-correcting, a wrong hidden card is silent. Same rule the plugin
// availability model states in .claude/rules/remote-dialogs.md.
func TestEventGroup_UnknownFallsBackToSystem(t *testing.T) {
	for _, typ := range []string{"", "totally_new_thing", "hook.newagent.Whatever"} {
		if got := eventGroup(typ); got != groupSystem {
			t.Errorf("eventGroup(%q) = %q, want %q", typ, got, groupSystem)
		}
	}
}

func TestGroupFilterFrom_DefaultConfig(t *testing.T) {
	f := groupFilterFrom(config.Default().Notification.Events)

	if !f.shows("hook.claude.Stop") {
		t.Error("agent_turn event hidden under default config")
	}
	if f.shows("output_idle") {
		t.Error("output_idle shown under default config, want hidden")
	}
	if f.shows("command_complete") {
		t.Error("command_complete shown under default config, want hidden")
	}
	if !f.shows("brand_new_type") {
		t.Error("unknown type hidden under default config, want shown")
	}
}

// A nil filter is the zero value on a Model built by a test that never set
// config. It must not hide everything.
func TestEventGroupFilter_NilShowsEverything(t *testing.T) {
	var f eventGroupFilter
	if !f.shows("output_idle") {
		t.Error("nil filter hid an event; want show-everything")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: FAIL — `undefined: eventGroup`, `undefined: groupAgentTurn`, …

- [ ] **Step 3: Write the implementation**

Create `internal/tui/notification_class.go`:

```go
package tui

import (
	"strings"

	"github.com/artyomsv/quil/internal/config"
)

// Notification groups. A group is the unit the user toggles in
// F1 -> Settings -> Notifications; an event TYPE is an internal string that
// changes whenever an upstream tool adds a hook, so a setting keyed on types
// would rot while a setting keyed on groups survives.
const (
	groupAgentTurn     = "agent_turn"
	groupAgentBlocked  = "agent_blocked"
	groupAgentSubagent = "agent_subagent"
	groupAgentSession  = "agent_session"
	groupProcess       = "process"
	groupPane          = "pane"
	groupMCP           = "mcp"
	groupSystem        = "system"
	groupCommands      = "commands"
	groupIdle          = "idle"
)

// hookEventGroups maps a hook event's TRAILING segment — everything after
// "hook.<source>." — to its group. Keyed on the trailing part rather than the
// full type so a fourth agent source (a new hook producer beside claude,
// opencode and codex) is classified correctly with no table change, which is
// the same forward-compatibility the daemon's own ClassifyWorkEvent lacks and
// pays for with a case per source.
var hookEventGroups = map[string]string{
	"UserPromptSubmit":  groupAgentTurn,
	"Stop":              groupAgentTurn,
	"StopFailure":       groupAgentTurn,
	"chat.message":      groupAgentTurn,
	"session.idle":      groupAgentTurn,
	"session.error":     groupAgentTurn,
	"PermissionRequest": groupAgentBlocked,
	"Notification":      groupAgentBlocked,
	"permission.ask":    groupAgentBlocked,
	"SubagentStart":     groupAgentSubagent,
	"SubagentStop":      groupAgentSubagent,
	"TaskCreated":       groupAgentSubagent,
	"TaskCompleted":     groupAgentSubagent,
	"SessionEnd":        groupAgentSession,
	"PreCompact":        groupAgentSession,
	"PostCompact":       groupAgentSession,
}

// plainEventGroups maps a non-hook event type, matched whole.
var plainEventGroups = map[string]string{
	"bell":                   groupAgentBlocked,
	"process_exit":           groupProcess,
	"pane_destroyed":         groupPane,
	"pane_pinned":            groupPane,
	"pane_unpinned":          groupPane,
	"pane_marked_deletion":   groupPane,
	"pane_unmarked_deletion": groupPane,
	"mcp_control":            groupMCP,
	"input_blocked":          groupSystem,
	"worktree_ready":         groupSystem,
	"command_complete":       groupCommands,
	"output_idle":            groupIdle,
}

// eventGroup classifies one PaneEvent Type.
//
// An UNRECOGNISED type returns groupSystem, which defaults ON. That direction
// is deliberate and is the same rule the plugin-availability model states: a
// wrong extra card is visible and the user can silence it, while a wrong
// hidden card is silent and the user never learns the event existed. It also
// makes a NEWER daemon paired with an OLDER client degrade safely — new event
// types render as ordinary cards instead of vanishing.
func eventGroup(eventType string) string {
	if rest, ok := strings.CutPrefix(eventType, "hook."); ok {
		// rest is "<source>.<event>"; the event may itself contain dots
		// (opencode's "chat.message"), so cut only the source segment.
		if _, event, ok := strings.Cut(rest, "."); ok {
			if g, known := hookEventGroups[event]; known {
				return g
			}
		}
		return groupSystem
	}
	if g, ok := plainEventGroups[eventType]; ok {
		return g
	}
	return groupSystem
}

// eventGroupFilter answers "does the sidebar show this group". A NIL filter
// shows everything: roughly 46 tests build a Model directly and never set
// config, and a zero value that hid every event would make the sidebar look
// broken in all of them.
type eventGroupFilter map[string]bool

// groupFilterFrom projects the config struct onto the map the renderer uses.
// The struct is the wire and file format; the map is what the hot path reads.
func groupFilterFrom(c config.EventGroupsConfig) eventGroupFilter {
	return eventGroupFilter{
		groupAgentTurn:     c.AgentTurn,
		groupAgentBlocked:  c.AgentBlocked,
		groupAgentSubagent: c.AgentSubagent,
		groupAgentSession:  c.AgentSession,
		groupProcess:       c.Process,
		groupPane:          c.Pane,
		groupMCP:           c.MCP,
		groupSystem:        c.System,
		groupCommands:      c.Commands,
		groupIdle:          c.Idle,
	}
}

// shows reports whether an event of this type belongs on the sidebar.
func (f eventGroupFilter) shows(eventType string) bool {
	if f == nil {
		return true
	}
	return f[eventGroup(eventType)]
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: PASS.

- [ ] **Step 5: Vet**

Run: `./scripts/dev.sh vet`

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/notification_class.go internal/tui/notification_class_test.go
git commit -m "feat(tui): classify notification events into groups"
```

---

## Task 3: Filter the notification center

**Files:**
- Modify: `internal/tui/notification.go` (struct at `:13`, `DismissSelected:87`, `SelectedEvent:105`, `Count:113`, `HandleKey:121`)
- Modify: `internal/tui/model.go:1037` (pass the filter at construction)
- Test: `internal/tui/notification_filter_test.go` (create)

**Interfaces:**
- Consumes: `eventGroupFilter`, `groupFilterFrom` (Task 2).
- Produces:
  - `func (nc *NotificationCenter) SetGroups(f eventGroupFilter)`
  - `func (nc *NotificationCenter) visible() []ipc.PaneEventPayload`
  - `NotificationCenter.showAll bool`
  - `Count()` now returns the visible count
  - `HandleKey` gains the `"a"` case returning action `"none"`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/notification_filter_test.go`:

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// showOnly builds a filter that shows exactly the named groups.
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
	vis := nc.visible()
	if len(vis) != 1 || vis[0].ID != "TEST-2" {
		t.Fatalf("visible: got %+v, want only TEST-2", vis)
	}
	if nc.Count() != 1 {
		t.Errorf("Count: got %d, want 1 (visible count)", nc.Count())
	}
}

// Turning a group back on must reveal the events that arrived while it was off.
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

	if _, _, _ = nc.HandleKey("a"); nc.Count() != 1 {
		t.Errorf("Count with showAll: got %d, want 1", nc.Count())
	}
	if _, _, _ = nc.HandleKey("a"); nc.Count() != 0 {
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

	nc.cursor = 1 // second VISIBLE event = TEST-v1

	if got := nc.DismissSelected(); got != "TEST-v1" {
		t.Fatalf("DismissSelected: got %q, want %q", got, "TEST-v1")
	}
	for _, e := range nc.events {
		if e.ID == "TEST-v1" {
			t.Fatal("TEST-v1 still stored after dismiss")
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: FAIL — `nc.SetGroups undefined`, `nc.visible undefined`.

- [ ] **Step 3: Add the fields and the filter accessors**

In `internal/tui/notification.go`, replace the struct with:

```go
// NotificationCenter manages the notification sidebar state.
//
// It stores EVERY event it is given and filters at READ time. Storing the
// filtered set instead would make turning a group back on silently useless —
// the events that arrived while it was off would already be gone — and the
// filter is a display preference, not a subscription.
type NotificationCenter struct {
	events    []ipc.PaneEventPayload
	cursor    int // index into visibleEvents(), NOT into events
	scroll    int // first visible LINE of the card viewport (added in Task 4)
	visible   bool
	focused   bool
	width     int
	maxEvents int
	// groups is the display filter. NIL shows everything, which is what a
	// Model built directly by a test gets.
	groups eventGroupFilter
	// showAll is the sidebar's 'a' override: display every stored event
	// regardless of the configured groups. Session-only and never persisted —
	// it is a debugging affordance, not a setting.
	showAll bool
}
```

> **Naming note:** the existing `visible bool` field stays as it is, so the
> filter accessor is named `visibleEvents()` rather than `visible()`. That
> spelling is used in every later task; do not rename either one.

Add below `NewNotificationCenter`:

```go
// SetGroups installs the display filter. Called at construction and again
// whenever the user toggles a group in F1 -> Settings -> Notifications, so the
// change applies live — a visible control that did nothing until relaunch reads
// as a broken dialog, the same rule the Sidebar width row states.
//
// The cursor is clamped rather than reset: enabling a group must not throw away
// the selection the user is reading.
func (nc *NotificationCenter) SetGroups(f eventGroupFilter) {
	nc.groups = f
	nc.clampCursor()
}

// visibleEvents returns the events the configured groups allow, newest first.
// EVERY read path resolves through this — cursor, selection, dismissal, the
// status-bar badge and the renderer — so none of them can disagree about which
// event position N is.
func (nc *NotificationCenter) visibleEvents() []ipc.PaneEventPayload {
	if nc.showAll || nc.groups == nil {
		return nc.events
	}
	out := make([]ipc.PaneEventPayload, 0, len(nc.events))
	for _, e := range nc.events {
		if nc.groups.shows(e.Type) {
			out = append(out, e)
		}
	}
	return out
}

// clampCursor keeps the cursor inside the visible list after anything that can
// shrink it: a filter change, a dismissal, an eviction at maxEvents.
func (nc *NotificationCenter) clampCursor() {
	n := len(nc.visibleEvents())
	if nc.cursor >= n {
		nc.cursor = n - 1
	}
	if nc.cursor < 0 {
		nc.cursor = 0
	}
}
```

- [ ] **Step 4: Move the read paths onto the filtered list**

Replace `DismissSelected`, `SelectedEvent` and `Count` in `internal/tui/notification.go`:

```go
// DismissSelected removes the selected event and returns its ID.
//
// It resolves the cursor through visibleEvents() and then deletes BY ID from
// the stored slice. Slicing nc.events by the cursor directly — which is what
// this did before the filter existed — dismisses a different event than the one
// under the cursor as soon as anything is hidden.
func (nc *NotificationCenter) DismissSelected() string {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return ""
	}
	id := vis[nc.cursor].ID
	for i, e := range nc.events {
		if e.ID == id {
			nc.events = append(nc.events[:i], nc.events[i+1:]...)
			break
		}
	}
	nc.clampCursor()
	return id
}

// SelectedEvent returns the currently selected event, or nil.
func (nc *NotificationCenter) SelectedEvent() *ipc.PaneEventPayload {
	vis := nc.visibleEvents()
	if nc.cursor < 0 || nc.cursor >= len(vis) {
		return nil
	}
	return &vis[nc.cursor]
}

// Count returns the number of events the user can currently see. The
// status-bar badge reads this, so a workspace holding nothing but hidden
// telemetry shows no badge — which is the point of the filter.
func (nc *NotificationCenter) Count() int {
	return len(nc.visibleEvents())
}
```

- [ ] **Step 5: Bound the cursor and add the `a` key**

In `HandleKey`, replace the `up` / `down` cases and add `a`:

```go
	case "up", "k":
		if nc.cursor > 0 {
			nc.cursor--
		}
		return "none", "", ""
	case "down", "j":
		if nc.cursor < len(nc.visibleEvents())-1 {
			nc.cursor++
		}
		return "none", "", ""
	case "a":
		// Reveal every stored event regardless of the configured groups, for
		// as long as the user wants it. Not persisted: the config screen is
		// where a lasting choice is made.
		nc.showAll = !nc.showAll
		nc.clampCursor()
		return "none", "", ""
```

- [ ] **Step 6: Clamp after a prepend eviction**

At the end of `AddEvent`'s fresh-prepend branch, after the `maxEvents` trim, add:

```go
	nc.clampCursor()
```

- [ ] **Step 7: Wire the filter at construction**

In `internal/tui/model.go`, at the `notifications:` line of the `Model` literal (`:1037`), leave the constructor call as it is and add immediately after the literal is built — inside `NewModel`, next to the other post-construction setup — this line:

```go
	m.notifications.SetGroups(groupFilterFrom(cfg.Notification.Events))
```

Place it directly above the existing `m.initKeymap()` call (`model.go:1062`).

- [ ] **Step 8: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: PASS, including the pre-existing `notification_test.go`, `notification_aggregate_test.go` and `notification_active_pane_test.go`.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/notification.go internal/tui/notification_filter_test.go internal/tui/model.go
git commit -m "feat(tui): filter sidebar events by configured group"
```

---

## Task 4: Line-based geometry, variable card height, scrolling

**Files:**
- Modify: `internal/tui/notification.go` (`View:154`)
- Modify: `internal/tui/model.go:4308` (pass the pane locator into `View`)
- Test: `internal/tui/notification_geometry_test.go` (create)

**Interfaces:**
- Consumes: `visibleEvents()`, `clampCursor()` (Task 3).
- Produces:
  - `type paneLocator func(paneID string) (label string, alive bool)`
  - `type renderedLine struct { text string; eventIdx int }`
  - `func notificationLines(events []ipc.PaneEventPayload, innerW, cursor int, focused bool, loc paneLocator) []renderedLine`
  - `func (nc *NotificationCenter) View(height int, loc paneLocator) string` — **signature change**
  - `func (nc *NotificationCenter) ScrollBy(delta, height int)`
  - `func (nc *NotificationCenter) eventIndexAtRow(y, height int, loc paneLocator) int`
  - `const notifyViewportOffset = 3`

**Layout contract** (fixed here, relied on by Task 5):

```
screen y     interior row   content
   0         —              tab bar (never the sidebar)
   1         —              box top border
   2         0              " Notifications " title
   3         1              card viewport line 0   <- nc.scroll indexes here
   …         …              …
 h-3         innerH-2       card viewport last line
 h-2         innerH-1       key hints
 h-1         —              box bottom border
```

So `eventIndexAtRow` converts a screen `y` to a viewport line with
`y - notifyViewportOffset + nc.scroll`, where `notifyViewportOffset = 3`.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/notification_geometry_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// liveLoc reports every pane as alive, in project "proj", tab "tab".
func liveLoc(paneID string) (string, bool) { return "proj · tab", true }

// deadLoc reports every pane as gone.
func deadLoc(paneID string) (string, bool) { return "", false }

func testEvent(id, title, message string) ipc.PaneEventPayload {
	return ipc.PaneEventPayload{
		ID: id, PaneID: "pane-" + id, PaneName: "shell",
		Type: "process_exit", Title: title, Message: message,
		Severity: "info", Timestamp: 0,
	}
}

// A card with no Message is one line shorter than one with it. This is the
// whole reason the viewport is indexed in LINES rather than cards.
func TestNotificationLines_VariableCardHeight(t *testing.T) {
	withExcerpt := notificationLines(
		[]ipc.PaneEventPayload{testEvent("TEST-1", "Process exited", "the last output line")},
		28, 0, false, liveLoc,
	)
	without := notificationLines(
		[]ipc.PaneEventPayload{testEvent("TEST-1", "Process exited", "")},
		28, 0, false, liveLoc,
	)
	if len(withExcerpt) != len(without)+1 {
		t.Errorf("card heights: with excerpt %d lines, without %d; want exactly one more",
			len(withExcerpt), len(without))
	}
}

// Every line must name the event it belongs to, or -1 for chrome. This is what
// the mouse hit test reads.
func TestNotificationLines_EventIndexPerLine(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{
			testEvent("TEST-1", "first", "excerpt one"),
			testEvent("TEST-2", "second", ""),
		},
		28, 0, false, liveLoc,
	)

	seen := map[int]int{}
	for _, l := range lines {
		seen[l.eventIdx]++
	}
	if seen[0] != 4 {
		t.Errorf("event 0 lines: got %d, want 4 (name, title, location, excerpt)", seen[0])
	}
	if seen[1] != 3 {
		t.Errorf("event 1 lines: got %d, want 3 (name, title, location)", seen[1])
	}
	if seen[-1] < 2 {
		t.Errorf("separator lines: got %d, want at least 2 (one per card)", seen[-1])
	}
}

// The location line names project and tab, so the user knows where a click
// will take them.
func TestNotificationLines_ShowsLocation(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{testEvent("TEST-1", "Process exited", "")},
		40, 0, false, liveLoc,
	)
	joined := stripANSI(strings.Join(linesText(lines), "\n"))
	if !strings.Contains(joined, "proj · tab") {
		t.Errorf("rendered card has no location line:\n%s", joined)
	}
}

// A card whose pane is gone must not advertise a jump it cannot perform.
func TestNotificationLines_DeadPaneIsMarked(t *testing.T) {
	lines := notificationLines(
		[]ipc.PaneEventPayload{testEvent("TEST-1", "Pane closed", "")},
		40, 0, false, deadLoc,
	)
	joined := stripANSI(strings.Join(linesText(lines), "\n"))
	if !strings.Contains(joined, "(closed)") {
		t.Errorf("dead pane card has no (closed) marker:\n%s", joined)
	}
}

func TestScrollBy_ClampsAtBothEnds(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(testEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	const height = 20

	nc.ScrollBy(-5, height)
	if nc.scroll != 0 {
		t.Errorf("scroll after scrolling up from the top: got %d, want 0", nc.scroll)
	}

	nc.ScrollBy(10000, height)
	total := len(notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, liveLoc))
	viewportH := height - 2 - 2
	want := total - viewportH
	if want < 0 {
		want = 0
	}
	if nc.scroll != want {
		t.Errorf("scroll after scrolling past the end: got %d, want %d", nc.scroll, want)
	}
}

// Reading history is impossible on a busy workspace if every arriving event
// yanks the viewport back to the top.
func TestAddEvent_DoesNotResetScroll(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	for i := 0; i < 20; i++ {
		nc.AddEvent(testEvent("TEST-"+string(rune('a'+i)), "title", "excerpt"))
	}
	nc.ScrollBy(6, 20)
	before := nc.scroll
	if before == 0 {
		t.Fatal("fixture did not scroll; the test cannot detect a reset")
	}

	nc.AddEvent(testEvent("TEST-new", "arriving", "excerpt"))
	if nc.scroll != before {
		t.Errorf("scroll after a new event: got %d, want %d (unchanged)", nc.scroll, before)
	}
}

func TestEventIndexAtRow(t *testing.T) {
	nc := NewNotificationCenter(30, 50)
	nc.AddEvent(testEvent("TEST-1", "first", "excerpt"))
	nc.AddEvent(testEvent("TEST-2", "second", "excerpt"))
	const height = 20

	// Row 0 is the tab bar, 1 the top border, 2 the title — all chrome.
	for _, y := range []int{0, 1, 2} {
		if got := nc.eventIndexAtRow(y, height, liveLoc); got != -1 {
			t.Errorf("eventIndexAtRow(%d) = %d, want -1 (chrome)", y, got)
		}
	}
	// The first card's separator is viewport line 0 (screen row 3); its name
	// line is viewport line 1 (screen row 4).
	if got := nc.eventIndexAtRow(4, height, liveLoc); got != 0 {
		t.Errorf("eventIndexAtRow(4) = %d, want 0 (newest card's name line)", got)
	}
}
```

Add these two helpers to the same file:

```go
// linesText extracts the text of each rendered line.
func linesText(lines []renderedLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
	}
	return out
}
```

- [ ] **Step 2: Check whether `stripANSI` already exists**

Run: `grep -rn "func stripANSI" internal/tui/`

If it does not exist, add it to `internal/tui/notification_geometry_test.go`:

```go
// stripANSI removes SGR escape sequences so a test can assert on text.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: FAIL — `undefined: notificationLines`, `undefined: renderedLine`.

- [ ] **Step 4: Write the geometry function**

In `internal/tui/notification.go`, replace the whole `View` method with the following. Keep `firstNonEmptyLine`, `truncateRunes`, `severityNameStyle` and `relativeTime` as they are.

```go
// notifyViewportOffset is the screen row of the card viewport's first line.
//
//	row 0   tab bar (never the sidebar)
//	row 1   box top border
//	row 2   " Notifications " title
//	row 3   card viewport line 0   <- nc.scroll indexes from here
//
// Named rather than spelled 3 at each site: the mouse hit test and the
// renderer must agree about it, and a literal in two files is how they drift.
const notifyViewportOffset = 3

// paneLocator answers, for one pane id, where the pane lives and whether it
// still exists. Supplied by the Model, which owns the project/tab tree; the
// NotificationCenter deliberately does not reach into it.
//
// The two answers travel together because one lookup produces both, and
// because a card whose pane is gone must be rendered differently AND must not
// offer a jump — the same fact drives both decisions.
type paneLocator func(paneID string) (label string, alive bool)

// renderedLine is one screen line of the card viewport, tagged with the index
// (into the VISIBLE event list) of the card it belongs to. eventIdx is -1 for
// chrome — separators and padding.
type renderedLine struct {
	text     string
	eventIdx int
}

// notificationLines renders the card viewport to a flat line list.
//
// It is the SINGLE source of truth for sidebar geometry: View slices its output
// by nc.scroll, and eventIndexAtRow looks up by index. Two independent
// implementations of "which card owns screen row N" is how a click lands on the
// wrong card after the first variable-height change.
//
// Pure — no NotificationCenter receiver, no Model — so its tests assert against
// fixed expected output rather than against the other caller. A test that
// compares two callers of a shared helper is a self-comparison and passes on
// broken geometry.
func notificationLines(events []ipc.PaneEventPayload, innerW, cursor int, focused bool, loc paneLocator) []renderedLine {
	if innerW < 5 {
		return nil
	}
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	separator := sepStyle.Render(truncateRunes(strings.Repeat("·", innerW), innerW))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("243"))

	var out []renderedLine
	for i, e := range events {
		selected := i == cursor && focused

		var label string
		alive := true
		if loc != nil {
			label, alive = loc(e.PaneID)
		}

		out = append(out, renderedLine{text: separator, eventIdx: -1})

		// Line 1: pane name (severity-coloured, or grey when the pane is gone)
		// + right-aligned relative age.
		name := e.PaneName
		if name == "" {
			name = e.PaneID
			if len(name) > 12 {
				name = name[:12]
			}
		}
		name = sanitizeRemoteText(name)
		nameStyle := severityNameStyle(e.Severity)
		if !alive {
			// A card that cannot be jumped to must not wear an urgency colour.
			nameStyle = dim
		}
		if selected {
			nameStyle = nameStyle.Bold(true).Reverse(true)
		}
		age := relativeTime(time.UnixMilli(e.Timestamp))
		gap := innerW - len([]rune(name)) - len([]rune(age))
		if gap < 1 {
			gap = 1
		}
		out = append(out, renderedLine{
			text:     nameStyle.Render(name) + strings.Repeat(" ", gap) + dim.Render(age),
			eventIdx: i,
		})

		// Line 2: title + optional ×N aggregation badge.
		//
		// sanitizeRemoteText runs BEFORE truncation, and that order is
		// load-bearing: truncateRunes slices runes with no idea what an escape
		// is, so sanitising afterwards would leave a cut sequence swallowing
		// the styling bytes that follow it. A title comes from a pane's own
		// child via the hook spool and reaches the terminal with no VT
		// emulator in between, so U+202E — printable, and therefore past any
		// C0-only filter — would reverse the rendered line.
		titleBody := "  " + sanitizeRemoteText(e.Title)
		if e.Data != nil {
			if n, err := strconv.Atoi(e.Data["count"]); err == nil && n > 1 {
				titleBody += "  ×" + e.Data["count"]
			}
		}
		titleText := truncateRunes(titleBody, innerW)
		if selected {
			titleText = lipgloss.NewStyle().Reverse(true).Render(titleText)
		} else {
			titleText = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(titleText)
		}
		out = append(out, renderedLine{text: titleText, eventIdx: i})

		// Line 3: where the pane lives, so a click's destination is visible
		// before the click. A pane that is gone says so instead.
		locText := "  " + sanitizeRemoteText(label)
		if !alive {
			locText = "  (closed)"
		}
		locLine := dim.Render(truncateRunes(locText, innerW))
		if selected {
			locLine = dim.Reverse(true).Render(truncateRunes(locText, innerW))
		}
		out = append(out, renderedLine{text: locLine, eventIdx: i})

		// Line 4: excerpt — EMITTED ONLY WHEN THERE IS ONE. The old renderer
		// always emitted it, blank or not, to keep every card four lines and
		// the card-index pagination arithmetic simple. Line-based scrolling
		// removes that constraint, and dropping the blank line is most of the
		// extra events now on screen.
		if e.Message != "" {
			preview := truncateRunes("  "+sanitizeRemoteText(firstNonEmptyLine(e.Message)), innerW)
			st := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			if selected {
				st = st.Reverse(true)
			}
			out = append(out, renderedLine{text: st.Render(preview), eventIdx: i})
		}
	}
	if len(out) > 0 {
		out = append(out, renderedLine{text: separator, eventIdx: -1})
	}
	return out
}
```

- [ ] **Step 5: Write the scroll and hit-test helpers**

Append to `internal/tui/notification.go`:

```go
// viewportHeight is how many card lines fit: the box interior less the title
// row and the hints row.
func notifyViewportHeight(height int) int {
	h := height - 2 /* borders */ - 2 /* title + hints */
	if h < 1 {
		h = 1
	}
	return h
}

// ScrollBy moves the card viewport by delta lines, clamped to the content.
//
// height is passed in rather than stored because the sidebar is drawn at the
// tab area's height, which changes with the terminal, and a stored copy would
// be one frame stale exactly when the user resizes and scrolls together.
func (nc *NotificationCenter) ScrollBy(delta, height int) {
	nc.scroll += delta
	nc.clampScroll(height, nil)
}

// clampScroll bounds nc.scroll to the rendered content. loc may be nil: the
// locator only affects a line's TEXT, never how many lines a card occupies, so
// the line count is the same either way.
func (nc *NotificationCenter) clampScroll(height int, loc paneLocator) {
	total := len(notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, loc))
	max := total - notifyViewportHeight(height)
	if max < 0 {
		max = 0
	}
	if nc.scroll > max {
		nc.scroll = max
	}
	if nc.scroll < 0 {
		nc.scroll = 0
	}
}

// eventIndexAtRow maps a screen row to the index (into visibleEvents()) of the
// card drawn there, or -1 for chrome and out-of-range rows.
func (nc *NotificationCenter) eventIndexAtRow(y, height int, loc paneLocator) int {
	vy := y - notifyViewportOffset
	if vy < 0 || vy >= notifyViewportHeight(height) {
		return -1
	}
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, loc)
	idx := vy + nc.scroll
	if idx < 0 || idx >= len(lines) {
		return -1
	}
	return lines[idx].eventIdx
}

// SelectIndex moves the cursor to a visible-list index and brings it into view.
func (nc *NotificationCenter) SelectIndex(i, height int) {
	if i < 0 || i >= len(nc.visibleEvents()) {
		return
	}
	nc.cursor = i
	nc.revealCursor(height)
}

// revealCursor scrolls the minimum distance needed to show the selected card
// whole. Called after keyboard navigation, never after an arriving event: a
// new event landing at index 0 must not yank a user reading history back to
// the top.
func (nc *NotificationCenter) revealCursor(height int) {
	lines := notificationLines(nc.visibleEvents(), nc.width-2, nc.cursor, nc.focused, nil)
	first, last := -1, -1
	for i, l := range lines {
		if l.eventIdx != nc.cursor {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	if first < 0 {
		return
	}
	vh := notifyViewportHeight(height)
	if first < nc.scroll {
		nc.scroll = first
	} else if last >= nc.scroll+vh {
		nc.scroll = last - vh + 1
	}
	nc.clampScroll(height, nil)
}
```

- [ ] **Step 6: Rewrite `View` on top of the geometry**

Replace the body of `View` with:

```go
// View renders the sidebar at the given height.
//
// loc resolves each card's project/tab label and liveness; pass nil in a test
// that does not care (every pane then reads as alive with an empty label).
func (nc *NotificationCenter) View(height int, loc paneLocator) string {
	innerW := nc.width - 2
	innerH := height - 2
	if innerW < 5 || innerH < 3 {
		return ""
	}

	out := make([]string, 0, innerH)
	out = append(out, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).
		Render(truncateRunes(" Notifications ", innerW)))

	vh := notifyViewportHeight(height)
	lines := notificationLines(nc.visibleEvents(), innerW, nc.cursor, nc.focused, loc)

	if len(lines) == 0 {
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("238")).
			Render(truncateRunes(strings.Repeat("·", innerW), innerW)))
		empty := "No notifications"
		if !nc.showAll && nc.groups != nil {
			empty = "No notifications (a: show all)"
		}
		out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
			Render(truncateRunes(empty, innerW)))
	} else {
		nc.clampScroll(height, loc)
		for i := nc.scroll; i < nc.scroll+vh && i < len(lines); i++ {
			out = append(out, lines[i].text)
		}
	}

	for len(out) < innerH-1 {
		out = append(out, "")
	}

	hints := "^!N Focus  Enter Go"
	if nc.focused {
		hints = "↑↓ ⏎go d/D a:all Esc"
	}
	if nc.showAll {
		hints = "SHOWING ALL  a:filter"
	}
	out = append(out, lipgloss.NewStyle().Foreground(lipgloss.Color("243")).
		Render(truncateRunes(hints, innerW)))

	borderColor := lipgloss.Color("63")
	if nc.focused {
		borderColor = lipgloss.Color("57")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Width(nc.width).
		Height(height).
		Render(strings.Join(out, "\n"))
}
```

- [ ] **Step 7: Reveal the cursor after keyboard navigation**

`HandleKey` has no height. Rather than thread one through, have the Model call
`revealCursor` after dispatching a key. In `internal/tui/model.go`, at the top
of `handleNotificationKey` (`:2726`), replace the first line with:

```go
	action, eventID, paneID := m.notifications.HandleKey(key)
	m.notifications.revealCursor(m.tabAreaHeight())
```

If `tabAreaHeight()` does not exist, run `grep -n "tabH :=" internal/tui/model.go`
and use the same expression that feeds `m.notifications.View(tabH)` at
`model.go:4308`, extracting it into a `func (m Model) tabAreaHeight() int`
helper used by both sites.

- [ ] **Step 8: Update the one production `View` call site**

In `internal/tui/model.go:4308`:

```go
			tabContent = overlayRight(tabContent, m.notifications.View(tabH, m.paneLocator()), m.paneAreaWidth(), sw)
```

Add the locator to `internal/tui/model.go`, beside `ownerTabOfPane`:

```go
// paneLocator returns the notification sidebar's "where does this pane live"
// callback. Resolved through findPaneAndTab, so a pane in ANY project on ANY
// destination is found — the sidebar carries events from all of them.
func (m *Model) paneLocator() paneLocator {
	return func(paneID string) (string, bool) {
		pane, proj, idx := m.findPaneAndTab(paneID)
		if pane == nil || proj == nil || idx < 0 || idx >= len(proj.tabs) {
			return "", false
		}
		return proj.Name + " · " + proj.tabs[idx].Name, true
	}
}
```

- [ ] **Step 9: Fix every other `View(` call in tests**

Run: `grep -rn "notifications.View(\|\.View(tabH" internal/tui/`

Add `, nil` to each test call site, e.g. `nc.View(20)` becomes `nc.View(20, nil)`.

- [ ] **Step 10: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: PASS.

- [ ] **Step 11: Vet and format**

Run: `./scripts/dev.sh vet`

Expected: clean.

- [ ] **Step 12: Commit**

```bash
git add internal/tui/notification.go internal/tui/notification_geometry_test.go internal/tui/model.go
git commit -m "feat(tui): scroll notifications by line with variable cards"
```

---

## Task 5: Mouse — click to jump, wheel to scroll

**Files:**
- Modify: `internal/tui/model.go` (click swallow `:1613`, wheel swallow `:2008`)
- Test: `internal/tui/notification_mouse_test.go` (create)

**Interfaces:**
- Consumes: `eventIndexAtRow`, `SelectIndex`, `ScrollBy` (Task 4); `handleNotificationKey` (existing).
- Produces:
  - `func (m Model) handleNotificationClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd)`
  - `func (m Model) handleNotificationWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd)`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/notification_mouse_test.go`:

```go
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/ipc"
)

// mouseTestModel builds a Model with a visible, sized notification sidebar and
// a handful of events. It drives everything through Update, never by calling a
// handler directly: a direct-call test can pass against code the call site
// makes unreachable.
//
// A struct literal rather than NewModel, matching attention_test.go and the
// other Model fixtures in this package — NewModel takes six arguments and
// reads QUIL_HOME, neither of which these assertions care about. newFakeConn
// comes from router_test.go and satisfies the client interface, which the
// dismiss path needs: m.client.Send is called unguarded.
func mouseTestModel(t *testing.T) Model {
	t.Helper()
	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         120,
		height:        30,
		notifications: NewNotificationCenter(30, 50),
	}
	m.notifications.visible = true
	m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
	for i := 0; i < 8; i++ {
		m.notifications.AddEvent(ipc.PaneEventPayload{
			ID:       "TEST-" + string(rune('a'+i)),
			PaneID:   "pane-test-" + string(rune('a'+i)),
			PaneName: "shell",
			Type:     "process_exit",
			Title:    "Process exited",
			Message:  "an excerpt line",
			Severity: "info",
		})
	}
	return m
}

// sidebarX is a column inside the sidebar overlay.
func sidebarX(m Model) int { return m.width - 2 }

func TestWheelOverSidebar_Scrolls(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.scroll

	next, _ := m.Update(tea.MouseWheelMsg{
		Mouse:  tea.Mouse{X: sidebarX(m), Y: 6},
		Button: tea.MouseWheelDown,
	})
	got := next.(Model).notifications.scroll
	if got <= before {
		t.Errorf("scroll after wheel down: got %d, want > %d", got, before)
	}
}

func TestWheelOverSidebar_DoesNotReachPane(t *testing.T) {
	m := mouseTestModel(t)
	// With no active tab there is no pane to scroll; the assertion that
	// matters is that the sidebar consumed the notch rather than falling
	// through, which the scroll change above proves. Here we check the
	// opposite edge: one column LEFT of the strip must not scroll it.
	before := m.notifications.scroll
	next, _ := m.Update(tea.MouseWheelMsg{
		Mouse:  tea.Mouse{X: m.width - m.notifications.width - 1, Y: 6},
		Button: tea.MouseWheelDown,
	})
	if got := next.(Model).notifications.scroll; got != before {
		t.Errorf("scroll after a wheel notch outside the strip: got %d, want %d", got, before)
	}
}

func TestClickOnCard_SelectsAndFocuses(t *testing.T) {
	m := mouseTestModel(t)
	m.notifications.cursor = 0

	// Screen row 4 = viewport line 1 = the newest card's name line.
	next, _ := m.Update(tea.MouseClickMsg{
		Mouse:  tea.Mouse{X: sidebarX(m), Y: 4},
		Button: tea.MouseLeft,
	})
	nm := next.(Model)
	if !nm.sidebarFocused {
		t.Error("sidebar not focused after a click on a card")
	}
	if nm.notifications.cursor != 0 {
		t.Errorf("cursor: got %d, want 0", nm.notifications.cursor)
	}
}

func TestClickOnChrome_FocusesWithoutSelecting(t *testing.T) {
	m := mouseTestModel(t)
	m.notifications.cursor = 3

	// Screen row 2 is the " Notifications " title — chrome.
	next, _ := m.Update(tea.MouseClickMsg{
		Mouse:  tea.Mouse{X: sidebarX(m), Y: 2},
		Button: tea.MouseLeft,
	})
	nm := next.(Model)
	if !nm.sidebarFocused {
		t.Error("sidebar not focused after a click on chrome")
	}
	if nm.notifications.cursor != 3 {
		t.Errorf("cursor moved on a chrome click: got %d, want 3", nm.notifications.cursor)
	}
}

func TestRightClickOnCard_Dismisses(t *testing.T) {
	m := mouseTestModel(t)
	before := m.notifications.Count()

	next, _ := m.Update(tea.MouseClickMsg{
		Mouse:  tea.Mouse{X: sidebarX(m), Y: 4},
		Button: tea.MouseRight,
	})
	if got := next.(Model).notifications.Count(); got != before-1 {
		t.Errorf("Count after right-click: got %d, want %d", got, before-1)
	}
}
```

- [ ] **Step 2: Confirm the fixture compiles against the real types**

Run: `grep -n "func newFakeConn" internal/tui/router_test.go`

Expected: `func newFakeConn() *fakeConn`. Import
`"github.com/artyomsv/quil/internal/config"` in the new test file.

- [ ] **Step 3: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: FAIL — the wheel and the clicks are swallowed, so `scroll`,
`sidebarFocused` and `Count` are unchanged.

- [ ] **Step 4: Route the click**

In `internal/tui/model.go`, replace the sidebar swallow inside the
`tea.MouseClickMsg` case (`:1610-1615`):

```go
		// Sidebar overlay region: the press belongs to the sidebar, not the
		// pane rendered beneath it. Clear drag flags so no half-armed drag
		// survives the press, then let the sidebar act on it — the overlay
		// used to swallow every click and do nothing with it, which is why the
		// list was keyboard-only.
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			m.clearDragState()
			return m.handleNotificationClick(msg)
		}
```

Add the handler beside `handleNotificationKey`:

```go
// handleNotificationClick acts on a press inside the notification sidebar.
//
// Left on a card selects it and jumps to its pane; left on chrome focuses the
// sidebar only; right on a card dismisses it. Focus is taken on every press,
// including chrome, so the keyboard works immediately after — a strip that
// swallows a click without taking focus reads as dead.
//
// The jump reuses handleNotificationKey's "enter" path rather than
// re-implementing it, so the pane-still-exists guard, the history push and the
// focus-mode handling are inherited instead of duplicated.
func (m Model) handleNotificationClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	m.sidebarFocused = true
	m.notifications.focused = true

	h := m.tabAreaHeight()
	idx := m.notifications.eventIndexAtRow(msg.Y, h, m.paneLocator())
	if idx < 0 {
		return m, nil
	}
	m.notifications.SelectIndex(idx, h)

	switch msg.Button {
	case tea.MouseLeft:
		return m.handleNotificationKey("enter")
	case tea.MouseRight:
		return m.handleNotificationKey("d")
	}
	return m, nil
}
```

- [ ] **Step 5: Route the wheel**

Replace the sidebar swallow inside the `tea.MouseWheelMsg` case (`:2007-2010`):

```go
		// Wheel over the sidebar scrolls the sidebar, and must not reach the
		// pane beneath it. Both vertical buttons are matched EXPLICITLY, and
		// the swallow stays OUTSIDE the switch: tea.MouseWheelMsg also carries
		// MouseWheelLeft/Right from a trackpad or shift-scroll, and a
		// horizontal notch aimed at the strip must not fall through to the
		// pane area either.
		if m.sidebarSwallowsMouse(msg.X, msg.Y) {
			return m.handleNotificationWheel(msg)
		}
```

Add the handler beside `handleNotificationClick`:

```go
// handleNotificationWheel scrolls the sidebar by the configured step.
func (m Model) handleNotificationWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	lines := m.cfg.UI.MouseScrollLines
	if lines < 1 {
		lines = 3
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.notifications.ScrollBy(-lines, m.tabAreaHeight())
	case tea.MouseWheelDown:
		m.notifications.ScrollBy(lines, m.tabAreaHeight())
	}
	return m, nil
}
```

- [ ] **Step 6: Update the footer hint for right-click**

In `internal/tui/notification.go`'s `View`, change the unfocused hint:

```go
	hints := "^!N Focus  Click Go"
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/model.go internal/tui/notification.go internal/tui/notification_mouse_test.go
git commit -m "feat(tui): click and scroll the notification sidebar"
```

---

## Task 6: F1 → Settings → Notifications

**Files:**
- Modify: `internal/tui/model.go` (dialog enum `:320-343`)
- Modify: `internal/tui/dialog.go` (`settingsField:194`, `settingsFields:208`, the `Desktop notifications` row `:433-456`, `dispatchDialogKey:685`, `handleSettingsKey:924`, the render switch `:1396`)
- Create: `internal/tui/dialog_notifysettings.go`
- Test: `internal/tui/dialog_notifysettings_test.go` (create)

**Interfaces:**
- Consumes: `groupFilterFrom` (Task 2), `NotificationCenter.SetGroups` (Task 3).
- Produces:
  - `dialogNotifySettings` (a `dialogScreen` constant)
  - `settingsField.submenu bool`
  - `type notifyToggle struct { label, hint string; get func(*Model) string; set func(*Model); heading bool }`
  - `func notifySettingsRows() []notifyToggle`
  - `func (m Model) handleNotifySettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd)`
  - `func (m Model) renderNotifySettingsDialog() string`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/dialog_notifysettings_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: 0, Text: s} }

// notifyRowIndex finds a row by label so a test does not hard-code an index
// that a later row insertion would silently invalidate.
func notifyRowIndex(t *testing.T, label string) int {
	t.Helper()
	for i, r := range notifySettingsRows() {
		if r.label == label {
			return i
		}
	}
	t.Fatalf("no notify settings row labelled %q", label)
	return -1
}

func TestNotifySettingsRows_CoverEveryGroupAndToast(t *testing.T) {
	want := []string{
		"Enabled", "On blocked", "On done",
		"Agent turns", "Agent blocked", "Agent subagents", "Agent session",
		"Process", "Pane", "MCP", "System", "Commands", "Idle",
	}
	rows := notifySettingsRows()
	var got []string
	for _, r := range rows {
		if !r.heading {
			got = append(got, r.label)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("toggle rows: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNotifySettings_ToggleAppliesLiveAndMarksConfigChanged(t *testing.T) {
	m := NewModelForDialogTest(t)
	m.dialog = dialogNotifySettings
	m.dialogCursor = notifyRowIndex(t, "Idle")
	if m.cfg.Notification.Events.Idle {
		t.Fatal("fixture: idle already on")
	}

	next, _ := m.Update(key("enter"))
	nm := next.(Model)

	if !nm.cfg.Notification.Events.Idle {
		t.Error("idle not enabled after Enter")
	}
	if !nm.configChanged {
		t.Error("configChanged not set; the edit would be lost on exit")
	}
	if !nm.notifications.groups.shows("output_idle") {
		t.Error("filter not re-applied live; the sidebar still hides idle events")
	}
}

func TestNotifySettings_EscReturnsToSettings(t *testing.T) {
	m := NewModelForDialogTest(t)
	m.dialog = dialogNotifySettings

	next, _ := m.Update(key("esc"))
	if got := next.(Model).dialog; got != dialogSettings {
		t.Errorf("dialog after Esc: got %v, want dialogSettings", got)
	}
}

func TestSettings_NotificationsRowOpensSubmenu(t *testing.T) {
	m := NewModelForDialogTest(t)
	m.dialog = dialogSettings
	fields := settingsFields()
	found := -1
	for i, f := range fields {
		if f.submenu && f.label == "Notifications" {
			found = i
		}
	}
	if found < 0 {
		t.Fatal("no submenu row labelled \"Notifications\" in settingsFields()")
	}
	m.dialogCursor = found

	next, _ := m.Update(key("enter"))
	if got := next.(Model).dialog; got != dialogNotifySettings {
		t.Errorf("dialog after Enter on the submenu row: got %v, want dialogNotifySettings", got)
	}
}

// The old top-level row is gone; its toggle lives in the new screen.
func TestSettings_DesktopNotificationsRowRemoved(t *testing.T) {
	for _, f := range settingsFields() {
		if f.label == "Desktop notifications" {
			t.Error("\"Desktop notifications\" is still a top-level Settings row")
		}
	}
}

func TestRenderNotifySettingsDialog_ShowsBothSections(t *testing.T) {
	m := NewModelForDialogTest(t)
	out := stripANSI(m.renderNotifySettingsDialog())
	for _, want := range []string{"Desktop toasts", "Sidebar events", "Agent turns", "Idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered dialog is missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Add the shared dialog-test fixture**

Add it to `internal/tui/dialog_notifysettings_test.go`:

```go
// NewModelForDialogTest builds a Model carrying the shipped defaults, so these
// tests exercise the same config a user gets.
//
// A struct literal rather than NewModel, matching this package's other Model
// fixtures (attention_test.go and friends): NewModel takes six arguments and
// reads QUIL_HOME, and none of that affects a dialog's key handling.
// newFakeConn comes from router_test.go and satisfies the client interface.
func NewModelForDialogTest(t *testing.T) Model {
	t.Helper()
	m := Model{
		client:        newFakeConn(),
		cfg:           config.Default(),
		width:         120,
		height:        40,
		notifications: NewNotificationCenter(30, 50),
	}
	m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
	return m
}
```

Import `"github.com/artyomsv/quil/internal/config"` in the file.

**On the `key(s string)` helper:** the handlers read `msg.String()`. Copy the
shape this package's existing dialog tests use — run
`grep -rn "KeyPressMsg{" internal/tui/dialog_test.go | head -3` — and replace
the sketch in Step 1 with that shape if it differs.

- [ ] **Step 3: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: FAIL — `undefined: dialogNotifySettings`, `undefined: notifySettingsRows`.

- [ ] **Step 4: Add the dialog constant**

In `internal/tui/model.go`, append to the `dialogScreen` const block (after
`dialogWhatsNew`):

```go
	dialogNotifySettings // F1 -> Settings -> Notifications: toasts + sidebar event groups
```

- [ ] **Step 5: Add the `submenu` flag and the row**

In `internal/tui/dialog.go`, add the field to `settingsField`:

```go
	// submenu marks a row that OPENS ANOTHER SCREEN instead of editing a
	// value. get supplies the right-hand hint, set is never called. A flag
	// rather than a label comparison at the call site, for the same reason
	// relayout is one: the row that needs the behaviour is the row that
	// declares it, and renaming a label cannot silently break it.
	submenu bool
```

Replace the whole `Desktop notifications` row in `settingsFields()` with:

```go
		{
			// Opens the Notifications screen, which holds the desktop-toast
			// toggles this row used to carry plus the ten sidebar event
			// groups. Promoted to a submenu because settingsFields renders
			// unwindowed and unscrolled: thirteen more rows here would push
			// the box off the bottom of a normal terminal.
			label:   "Notifications",
			get:     func(m *Model) string { return "…" },
			set:     func(m *Model, _ string) {},
			submenu: true,
		},
```

- [ ] **Step 6: Handle the submenu row in `handleSettingsKey`**

In `handleSettingsKey`'s non-edit `switch key` block, replace the
`case "enter", " ":` arm with:

```go
	case "enter", " ":
		f := fields[m.dialogCursor]
		switch {
		case f.submenu:
			m.dialog = dialogNotifySettings
			m.dialogCursor = 0
		case f.isBool:
			f.set(&m, "")
		default:
			m.dialogEdit = true
			m.dialogInput = f.get(&m)
		}
```

- [ ] **Step 7: Write the new screen**

Create `internal/tui/dialog_notifysettings.go`:

```go
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// notifyToggle is one row of F1 -> Settings -> Notifications.
//
// Every row is a plain on/off switch, so there is no edit mode and no set(val)
// signature — set takes no value. A heading row is inert: it carries a label
// and nothing else, and the cursor skips over it.
type notifyToggle struct {
	label   string
	hint    string
	get     func(m *Model) string
	set     func(m *Model)
	heading bool
}

// notifySettingsRows returns the screen's rows, headings included.
//
// Sidebar-event toggles apply LIVE: the filter is re-projected onto the
// notification center in each setter, because a visible control that did
// nothing until relaunch reads as a broken dialog. The toast toggles apply
// live for free — raiseAttentionToast reads m.cfg on every edge.
func notifySettingsRows() []notifyToggle {
	// applyGroups re-projects the config onto the live filter. Called from
	// every sidebar-event setter; factored out so a new group added later
	// cannot be wired up without it.
	applyGroups := func(m *Model) {
		m.notifications.SetGroups(groupFilterFrom(m.cfg.Notification.Events))
		m.configChanged = true
	}
	group := func(label, hint string, read func(*config.EventGroupsConfig) *bool) notifyToggle {
		return notifyToggle{
			label: label,
			hint:  hint,
			get:   func(m *Model) string { return boolStr(*read(&m.cfg.Notification.Events)) },
			set: func(m *Model) {
				p := read(&m.cfg.Notification.Events)
				*p = !*p
				applyGroups(m)
			},
		}
	}

	return []notifyToggle{
		{label: "Desktop toasts", heading: true},
		{
			// Reports STATE, not the flag: with Enabled defaulting true,
			// enabled-but-unregistered is the DEFAULT on a fresh Windows
			// install, and a bare "on" there would claim toasts are working
			// when no toast can be displayed. The row does not perform
			// registration — `quil notify setup` is still the gate.
			label: "Enabled",
			hint:  "quil notify setup registers them",
			get:   func(m *Model) string { return m.desktopState().label() },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Enabled = !m.cfg.Notification.Desktop.Enabled
				m.configChanged = true
			},
		},
		{
			label: "On blocked",
			hint:  "a pane is waiting for you",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Blocked) },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Blocked = !m.cfg.Notification.Desktop.Blocked
				m.configChanged = true
			},
		},
		{
			label: "On done",
			hint:  "a turn finished while you were away",
			get:   func(m *Model) string { return boolStr(m.cfg.Notification.Desktop.Done) },
			set: func(m *Model) {
				m.cfg.Notification.Desktop.Done = !m.cfg.Notification.Desktop.Done
				m.configChanged = true
			},
		},
		{label: "Sidebar events", heading: true},
		group("Agent turns", "Working on… / Reply ready",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentTurn }),
		group("Agent blocked", "permission, waiting for you",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentBlocked }),
		group("Agent subagents", "subagent + task start/stop",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSubagent }),
		group("Agent session", "session end, compaction",
			func(c *config.EventGroupsConfig) *bool { return &c.AgentSession }),
		group("Process", "exited / failed",
			func(c *config.EventGroupsConfig) *bool { return &c.Process }),
		group("Pane", "closed, pinned, marked",
			func(c *config.EventGroupsConfig) *bool { return &c.Pane }),
		group("MCP", "an agent drove a pane",
			func(c *config.EventGroupsConfig) *bool { return &c.MCP }),
		group("System", "input blocked, worktree, unknown",
			func(c *config.EventGroupsConfig) *bool { return &c.System }),
		group("Commands", "every shell command",
			func(c *config.EventGroupsConfig) *bool { return &c.Commands }),
		group("Idle", "output idle",
			func(c *config.EventGroupsConfig) *bool { return &c.Idle }),
	}
}

// handleNotifySettingsKey drives the screen. Headings are skipped by the
// cursor in both directions, so Enter can never land on an inert row.
func (m Model) handleNotifySettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := notifySettingsRows()
	switch msg.String() {
	case "esc":
		m.dialog = dialogSettings
		m.dialogCursor = 0
	case "up", "k":
		for i := m.dialogCursor - 1; i >= 0; i-- {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
	case "down", "j":
		for i := m.dialogCursor + 1; i < len(rows); i++ {
			if !rows[i].heading {
				m.dialogCursor = i
				break
			}
		}
	case "enter", " ":
		if m.dialogCursor >= 0 && m.dialogCursor < len(rows) && !rows[m.dialogCursor].heading {
			rows[m.dialogCursor].set(&m)
		}
	}
	return m, nil
}

// renderNotifySettingsDialog paints the screen at the standard dialog width.
func (m Model) renderNotifySettingsDialog() string {
	var b strings.Builder
	b.WriteString(dialogTitle.Render("Notifications"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  changes persist to config.toml"))
	b.WriteString("\n\n")

	for i, r := range notifySettingsRows() {
		if r.heading {
			b.WriteString("\n  " + dialogTitle.Render(r.label) + "\n")
			continue
		}
		cursor := "    "
		labelStyle := dialogLabelStyle
		if i == m.dialogCursor {
			cursor = "  > "
			labelStyle = labelStyle.Foreground(lipgloss.Color("230")).Bold(true)
		}
		b.WriteString(cursor + labelStyle.Render(r.label) + dialogValStyle.Render(r.get(&m)) + "\n")
		if r.hint != "" {
			b.WriteString(dialogSubtle.Render("      " + r.hint))
			b.WriteByte('\n')
		}
	}

	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  Hidden events still reach MCP agents"))
	b.WriteByte('\n')
	b.WriteString(dialogSubtle.Render("  ↑↓ navigate  Enter toggle  Esc back"))
	return b.String()
}
```

Add the `config` import to the file's import block:
`"github.com/artyomsv/quil/internal/config"`.

- [ ] **Step 8: Wire the screen into the two switches**

In `dispatchDialogKey` (`dialog.go:685`), add above the closing brace of the
switch:

```go
	case dialogNotifySettings:
		return m.handleNotifySettingsKey(msg)
```

In the render switch (`dialog.go:1396`), beside `case dialogSettings:`:

```go
	case dialogNotifySettings:
		content = m.renderNotifySettingsDialog()
```

- [ ] **Step 9: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/tui`

Expected: PASS. `dialog_test.go` and `dialog_notify_test.go` may assert on the
old `Desktop notifications` row — update those assertions to point at the new
screen rather than deleting them.

- [ ] **Step 10: Vet**

Run: `./scripts/dev.sh vet`

Expected: clean.

- [ ] **Step 11: Commit**

```bash
git add internal/tui/dialog_notifysettings.go internal/tui/dialog_notifysettings_test.go internal/tui/dialog.go internal/tui/model.go internal/tui/dialog_test.go internal/tui/dialog_notify_test.go
git commit -m "feat(tui): add F1 Settings Notifications screen"
```

---

## Task 7: Daemon — `mcp_control`

**Files:**
- Modify: `internal/daemon/procreport.go` (after `putStat:288`)
- Modify: `internal/daemon/session.go` (`Pane` struct, beside `LastBellEventAt:138`)
- Modify: `internal/daemon/daemon.go` (`handlePaneInput:2687`, `handleRestartPaneReq:5502`)
- Test: `internal/daemon/event_mcp_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks (the daemon is independent of the TUI work).
- Produces:
  - `func (r *helloRegistry) roleOf(conn *ipc.Conn) string`
  - `Pane.LastMCPEventAt time.Time`
  - `func (d *Daemon) notifyMCPControl(pane *Pane, title string)`
  - event types `mcp_control`

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/event_mcp_test.go`:

```go
package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestHelloRegistry_RoleOf(t *testing.T) {
	r := newHelloRegistry()
	var tuiConn, bridgeConn, silent *ipc.Conn // nil pointers are distinct map keys

	// put ignores a nil conn, so use non-nil sentinels.
	tuiConn = &ipc.Conn{}
	bridgeConn = &ipc.Conn{}
	silent = &ipc.Conn{}

	r.put(tuiConn, ipc.ClientHelloPayload{Role: "tui"})
	r.put(bridgeConn, ipc.ClientHelloPayload{Role: "bridge"})

	if got := r.roleOf(tuiConn); got != "tui" {
		t.Errorf("roleOf(tui): got %q, want %q", got, "tui")
	}
	if got := r.roleOf(bridgeConn); got != "bridge" {
		t.Errorf("roleOf(bridge): got %q, want %q", got, "bridge")
	}
	if got := r.roleOf(silent); got != "" {
		t.Errorf("roleOf(never said hello): got %q, want %q", got, "")
	}
	if got := r.roleOf(nil); got != "" {
		t.Errorf("roleOf(nil): got %q, want %q", got, "")
	}
}

func TestNotifyMCPControl_EmitsOnce(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "pane-test-1", TabID: "tab-test-1", Name: "shell"}

	d.notifyMCPControl(pane, "MCP agent typed here")
	if got := d.events.Count(); got != 1 {
		t.Fatalf("events after first call: got %d, want 1", got)
	}

	// Inside the cooldown window: no second card.
	d.notifyMCPControl(pane, "MCP agent typed here")
	if got := d.events.Count(); got != 1 {
		t.Errorf("events after a second call inside the cooldown: got %d, want 1", got)
	}

	// Past the cooldown: a new card. Aggregation reuses the ID, so assert on
	// the count Data carries rather than on the queue length.
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
}
```

- [ ] **Step 2: Find or write `newTestDaemon`**

Run: `grep -rn "func newTestDaemon\|func testDaemon" internal/daemon/*_test.go | head`

Use the existing helper if there is one. If not, add to
`internal/daemon/event_mcp_test.go`:

```go
// newTestDaemon builds a Daemon with only the fields these tests touch. It has
// no IPC server, so every broadcast is the documented nil-server no-op.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		events:  newEventQueue(50),
		session: NewSessionManager(1024),
		hellos:  newHelloRegistry(),
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/daemon`

Expected: FAIL — `r.roleOf undefined`, `d.notifyMCPControl undefined`,
`pane.LastMCPEventAt undefined`.

- [ ] **Step 4: Add `roleOf`**

In `internal/daemon/procreport.go`, after `putStat`:

```go
// roleOf returns a connection's self-declared role ("tui" or "bridge"), or ""
// when it never said hello.
//
// This is the honest way to tell an MCP bridge from the TUI. The tempting
// alternative — treating an ID-bearing request as an agent — is wrong:
// Message.ID is a request-response correlation id, so any future
// request-response caller would be misreported.
func (r *helloRegistry) roleOf(conn *ipc.Conn) string {
	if conn == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byConn[conn].payload.Role
}
```

- [ ] **Step 5: Add the cooldown field**

In `internal/daemon/session.go`, beside `LastBellEventAt`:

```go
	LastMCPEventAt time.Time // Cooldown: last time an mcp_control event was emitted
```

- [ ] **Step 6: Add the emitter**

In `internal/daemon/daemon.go`, beside `notifyInputBlocked`:

```go
// mcpControlCooldown is how long one pane stays quiet after an mcp_control
// card. An agent driving a pane sends many messages per turn; the card exists
// to say "an agent is on this pane", which is a state, not a per-keystroke fact.
const mcpControlCooldown = 30 * time.Second

// notifyMCPControl surfaces an MCP agent acting on a pane.
//
// Cooldown bookkeeping happens under PluginMu; the emit happens AFTER the
// unlock. emitEvent re-locks this pane's PluginMu for its mute check and Go
// mutexes are not reentrant, so emitting while holding the lock self-deadlocks
// the calling goroutine and every subsystem queued behind that pane —
// the daemon-wide freeze of 2026-06-12. notifyInputBlocked is shaped the same
// way for the same reason.
func (d *Daemon) notifyMCPControl(pane *Pane, title string) {
	pane.PluginMu.Lock()
	if !pane.LastMCPEventAt.IsZero() && time.Since(pane.LastMCPEventAt) < mcpControlCooldown {
		pane.PluginMu.Unlock()
		return
	}
	pane.LastMCPEventAt = time.Now()
	tabID, name := pane.TabID, pane.Name
	pane.PluginMu.Unlock()

	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     tabID,
		PaneName:  name,
		Type:      "mcp_control",
		Title:     title,
		Severity:  "info",
		Timestamp: time.Now(),
	})
}
```

- [ ] **Step 7: Call it from the two bridge-reachable handlers**

In `handlePaneInput` (`daemon.go:2687`), immediately after the pane lookup
succeeds and before the `EnqueueInput` call:

```go
	if d.hellos.roleOf(conn) == "bridge" {
		d.notifyMCPControl(pane, "MCP agent typed here")
	}
```

In `handleRestartPaneReq` (`daemon.go:5502`), after its pane lookup succeeds:

```go
	if d.hellos.roleOf(conn) == "bridge" {
		d.notifyMCPControl(pane, "MCP agent restarted this pane")
	}
```

- [ ] **Step 8: Run the tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/daemon`

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/daemon/procreport.go internal/daemon/session.go internal/daemon/daemon.go internal/daemon/event_mcp_test.go
git commit -m "feat(daemon): notify when an MCP agent drives a pane"
```

---

## Task 8: Daemon — pane lifecycle and worktree events

**Files:**
- Modify: `internal/daemon/daemon.go` (`handleUpdatePane:2867`, `handleDestroyPane:2552`, `handleDestroyPaneReq:5741`)
- Modify: `internal/daemon/worktree_add.go` (`worktreeAddAndCreate:68`)
- Test: `internal/daemon/event_panelife_test.go` (create)

**Interfaces:**
- Consumes: `newTestDaemon` (Task 7).
- Produces:
  - `func (d *Daemon) notifyPaneMark(pane *Pane, eventType, title string)`
  - `func (d *Daemon) notifyPaneDestroyed(pane *Pane, by string)`
  - `func (d *Daemon) notifyWorktreeReady(pane *Pane, branch string)`
  - event types `pane_pinned`, `pane_unpinned`, `pane_marked_deletion`, `pane_unmarked_deletion`, `pane_destroyed`, `worktree_ready`

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/event_panelife_test.go`:

```go
package daemon

import "testing"

func TestNotifyPaneDestroyed_CarriesActorAndName(t *testing.T) {
	d := newTestDaemon(t)
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
	if evs[0].PaneName != "build" {
		t.Errorf("PaneName: got %q, want %q — the name must be read before DestroyPane", evs[0].PaneName)
	}
	if evs[0].Title != "Pane closed by MCP agent" {
		t.Errorf("Title: got %q, want %q", evs[0].Title, "Pane closed by MCP agent")
	}
}

func TestNotifyPaneDestroyed_UserActor(t *testing.T) {
	d := newTestDaemon(t)
	d.notifyPaneDestroyed(&Pane{ID: "pane-test-1", Name: "shell"}, "user")

	evs := d.events.Events()
	if len(evs) != 1 || evs[0].Title != "Pane closed" {
		t.Fatalf("events: %+v, want one titled %q", evs, "Pane closed")
	}
}

// An overlay pane is auto-destroyed on exit as ordinary lifecycle. A card per
// Alt+G toggle is exactly the telemetry this feature removes.
func TestNotifyPaneDestroyed_SkipsOverlay(t *testing.T) {
	d := newTestDaemon(t)
	d.notifyPaneDestroyed(&Pane{ID: "pane-test-1", Name: "lazygit", Overlay: true}, "user")

	if got := d.events.Count(); got != 0 {
		t.Errorf("events for an overlay pane: got %d, want 0", got)
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
		d := newTestDaemon(t)
		d.notifyPaneMark(&Pane{ID: "pane-test-1", Name: "api"}, c.eventType, c.title)

		evs := d.events.Events()
		if len(evs) != 1 {
			t.Fatalf("%s: events got %d, want 1", c.eventType, len(evs))
		}
		if evs[0].Type != c.eventType || evs[0].Title != c.title {
			t.Errorf("got (%q, %q), want (%q, %q)", evs[0].Type, evs[0].Title, c.eventType, c.title)
		}
	}
}

func TestNotifyWorktreeReady_CarriesBranch(t *testing.T) {
	d := newTestDaemon(t)
	d.notifyWorktreeReady(&Pane{ID: "pane-test-1", Name: "claude"}, "feat/example")

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
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test ./internal/daemon`

Expected: FAIL — `d.notifyPaneDestroyed undefined`, `d.notifyPaneMark undefined`,
`d.notifyWorktreeReady undefined`.

- [ ] **Step 3: Write the three emitters**

In `internal/daemon/daemon.go`, beside `notifyMCPControl`:

```go
// notifyPaneMark surfaces a pin or deletion mark the user set by hand.
//
// Four explicit event types rather than two with a boolean in Data: the sidebar
// title then needs no branching, and the queue's (PaneID, Title) aggregation
// cannot merge a pin with an unpin into one card wearing the wrong label.
//
// No cooldown. These are deliberate single acts, not a stream.
func (d *Daemon) notifyPaneMark(pane *Pane, eventType, title string) {
	pane.PluginMu.Lock()
	tabID, name := pane.TabID, pane.Name
	pane.PluginMu.Unlock()

	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     tabID,
		PaneName:  name,
		Type:      eventType,
		Title:     title,
		Severity:  "info",
		Timestamp: time.Now(),
	})
}

// notifyPaneDestroyed records a pane closing, and who closed it.
//
// The two destroy paths do not share a funnel — handleDestroyPane serves the
// TUI and takes no conn, handleDestroyPaneReq serves MCP and does — so this is
// called from both rather than from one common point that does not exist.
//
// It MUST run before session.DestroyPane: that call removes the pane from the
// session maps, and the name and tab id are gone with it.
//
// Overlay panes are skipped. They are auto-destroyed on exit by onPaneExit as
// ordinary lifecycle, so a card per Alt+G toggle would be exactly the telemetry
// the event filter exists to remove.
func (d *Daemon) notifyPaneDestroyed(pane *Pane, by string) {
	if pane == nil {
		return
	}
	pane.PluginMu.Lock()
	isOverlay := pane.Overlay
	tabID, name := pane.TabID, pane.Name
	pane.PluginMu.Unlock()
	if isOverlay {
		return
	}

	title := "Pane closed"
	if by == "mcp" {
		title = "Pane closed by MCP agent"
	}
	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     tabID,
		PaneName:  name,
		Type:      "pane_destroyed",
		Title:     title,
		Severity:  "info",
		Timestamp: time.Now(),
		Data:      map[string]string{"by": by},
	})
}

// notifyWorktreeReady says a git worktree finished preparing and its pane is
// live. Emitted only on the success path: a failed add already surfaces as
// SpawnError inside the placeholder pane, and a second telling of the same
// failure in the sidebar adds nothing.
func (d *Daemon) notifyWorktreeReady(pane *Pane, branch string) {
	if pane == nil {
		return
	}
	pane.PluginMu.Lock()
	tabID, name := pane.TabID, pane.Name
	pane.PluginMu.Unlock()

	d.emitEvent(PaneEvent{
		ID:        uuid.New().String(),
		PaneID:    pane.ID,
		TabID:     tabID,
		PaneName:  name,
		Type:      "worktree_ready",
		Title:     "Worktree ready: " + branch,
		Severity:  "info",
		Timestamp: time.Now(),
		Data:      map[string]string{"branch": branch},
	})
}
```

- [ ] **Step 4: Run the emitter tests to verify they pass**

Run: `./scripts/dev.sh test ./internal/daemon`

Expected: PASS.

- [ ] **Step 5: Call `notifyPaneMark` from `handleUpdatePane`**

In `handleUpdatePane` (`daemon.go:2867`), find where `PinnedAttention` and
`MarkedForDeletion` are applied. Wrap each with a change check. The pattern for
the pin (apply the same shape to the deletion mark):

```go
	if payload.PinnedAttention != nil {
		// Emit only on a real CHANGE. The pointers are what make this
		// possible: a no-op write from an OSC 7 CWD update carries nil for
		// both marks, which is also why they are pointers in the first place
		// (see UpdatePanePayload) — the panes carrying a deletion mark are
		// exactly the ones still reporting cd from a background job.
		if pane.PinnedAttention != *payload.PinnedAttention {
			pane.PinnedAttention = *payload.PinnedAttention
			if *payload.PinnedAttention {
				d.notifyPaneMark(pane, "pane_pinned", "Pane pinned for attention")
			} else {
				d.notifyPaneMark(pane, "pane_unpinned", "Pane attention cleared")
			}
		}
	}
```

and for the deletion mark:

```go
	if payload.MarkedForDeletion != nil {
		if pane.MarkedForDeletion != *payload.MarkedForDeletion {
			pane.MarkedForDeletion = *payload.MarkedForDeletion
			if *payload.MarkedForDeletion {
				d.notifyPaneMark(pane, "pane_marked_deletion", "Pane marked for deletion")
			} else {
				d.notifyPaneMark(pane, "pane_unmarked_deletion", "Pane deletion mark cleared")
			}
		}
	}
```

Preserve whatever the existing code does around these assignments (locking,
broadcast, snapshot request). Read `daemon.go:2867-2960` first and edit
in place rather than replacing the block.

`Unseen` is deliberately left alone: it is set and cleared constantly by
ordinary focus changes and would be pure telemetry.

- [ ] **Step 6: Call `notifyPaneDestroyed` from both destroy paths**

In `handleDestroyPane` (`daemon.go:2552`), inside the existing
`if pane := d.session.Pane(payload.PaneID); pane != nil {` block that already
captures `tabID`:

```go
		d.notifyPaneDestroyed(pane, "user")
```

In `handleDestroyPaneReq` (`daemon.go:5741`), after the `pane == nil` guard and
before `d.session.DestroyPane(req.PaneID)`:

```go
	d.notifyPaneDestroyed(pane, "mcp")
```

- [ ] **Step 7: Call `notifyWorktreeReady` from the success path**

Read `internal/daemon/worktree_add.go:68` onward and find the single successful
`return` of `worktreeAddAndCreate`. Immediately before it, once the created
pane is in hand:

```go
	d.notifyWorktreeReady(created, spec.Branch)
```

Use whatever the local variable for the created pane and the branch actually
are — `grep -n "return ipc.CreatePaneRespPayload{" internal/daemon/worktree_add.go`
finds the returns, and only the one with `Success: true` (or its equivalent) is
the success path.

- [ ] **Step 8: Run the full daemon suite**

Run: `./scripts/dev.sh test ./internal/daemon`

Expected: PASS, including `wedge_regression_test.go` and the existing event
tests.

- [ ] **Step 9: Vet**

Run: `./scripts/dev.sh vet`

Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/worktree_add.go internal/daemon/event_panelife_test.go
git commit -m "feat(daemon): notify on pane marks, close and worktree ready"
```

---

## Task 9: Documentation and changelog

**Files:**
- Modify: `docs/configuration.md`
- Modify: `docs/features.md`
- Modify: `docs/keybindings.md`
- Modify: `docs/roadmap/notification-center.md`
- Modify: `.claude/rules/tui-dialogs.md`
- Create: `changelog.d/changed-notification-timeline.md`

**Interfaces:**
- Consumes: everything from Tasks 1-8.
- Produces: no code.

- [ ] **Step 1: Document the config section**

In `docs/configuration.md`, find the `[notification]` section and add after it:

````markdown
### `[notification.events]`

Selects which kinds of event the sidebar shows. Every key is a boolean.

```toml
[notification.events]
agent_turn     = true   # "Working on…" / "Reply ready"
agent_blocked  = true   # permission prompts, "waiting for your input", terminal bell
agent_subagent = true   # subagent and task start/stop
agent_session  = true   # session end, compaction
process        = true   # a process exited or failed
pane           = true   # a pane was closed, pinned, or marked for deletion
mcp            = true   # an MCP agent drove a pane
system         = true   # input blocked, worktree ready, and any unrecognised event
commands       = false  # every shell command (needs shell integration)
idle           = false  # a pane went quiet
```

`commands` and `idle` are off by default. Both describe machine state rather
than news: `idle` fires for every quiet pane every 30 seconds for as long as
the pane exists, and `commands` fires on every shell command you run. Left on,
they occupy the sidebar permanently.

Edit these from `F1 → Settings → Notifications` rather than by hand; the
screen applies each change immediately.

**Hiding a group does not stop the event.** MCP agents reading
`get_notifications` and `watch_notifications` still receive everything. The
filter is a display preference on your client alone.

An unrecognised event type is treated as `system`, so a newer daemon paired
with an older client shows its new events rather than silently dropping them.
````

- [ ] **Step 2: Document the sidebar behaviour**

In `docs/features.md`, replace the notification-center description with:

```markdown
### Notification timeline

`Alt+N` opens a timeline of work events on the right edge: agent turns
starting and finishing, permission prompts, processes exiting, panes being
closed or pinned, MCP agents taking a pane. Newest first, across every project
and every destination.

- **Click a card** to jump to its pane. The card names the project and tab it
  will take you to, and a card whose pane has since closed says `(closed)` and
  does not jump.
- **Scroll with the wheel**, or `↑`/`↓` when the sidebar is focused
  (`Ctrl+Alt+N`).
- **Right-click a card** to dismiss it; `d` dismisses the selected one and `D`
  dismisses all.
- **`a`** temporarily reveals every event, ignoring your filter — useful when
  you are debugging a pane rather than working in it.
- Choose which kinds of event appear in `F1 → Settings → Notifications`.
```

- [ ] **Step 3: Document the key**

In `docs/keybindings.md`, in the notification-sidebar section, add:

```markdown
| `a` | Sidebar focused | Show every event, ignoring the configured filter (toggle) |
```

- [ ] **Step 4: Record the phase**

In `docs/roadmap/notification-center.md`, append:

```markdown
## Phase 4 — Timeline (done)

The sidebar became a user-filtered timeline. Ten event groups, chosen from
`F1 → Settings → Notifications`; `output_idle` and `command_complete` default
off. Mouse click-to-jump and wheel scrolling. Variable-height cards with a
line-based scroll offset, so roughly twice as many events fit. Four new event
kinds: `mcp_control`, the pin/deletion marks, `pane_destroyed` and
`worktree_ready`.

The daemon queue and the three MCP tools were deliberately left unfiltered:
one queue serves both an agent polling for machine state and a human reading a
timeline, and only the client knows which it is.

Design: `docs/superpowers/specs/2026-09-06-notification-timeline-design.md`.
```

- [ ] **Step 5: Note the dialog in the scoped rule**

In `.claude/rules/tui-dialogs.md`, add:

```markdown
**`dialogNotifySettings`** (`internal/tui/dialog_notifysettings.go`) is
`F1 → Settings → Notifications`: three desktop-toast toggles and ten sidebar
event groups. Reached through `settingsField.submenu`, a flag rather than a
label comparison, for the reason `relayout` is one — the row that needs the
behaviour declares it, so renaming a label cannot silently break it.

It exists because `renderSettingsDialog` paints every row unwindowed and
unscrolled: thirteen more rows in the top-level list would push the box off the
bottom of an ordinary terminal. Its own rows include inert headings, which the
cursor skips in both directions so `Enter` can never land on one.

Every sidebar-event setter re-projects the config onto the live filter
(`m.notifications.SetGroups(groupFilterFrom(...))`). A visible control that did
nothing until relaunch reads as a broken dialog — the same rule the `Sidebar
width` row states.
```

- [ ] **Step 6: Write the changelog fragment**

Create `changelog.d/changed-notification-timeline.md`:

```markdown
---
headline: The notification sidebar is now a timeline you can click
---
- **The notification sidebar shows work, not telemetry.** `Output idle` and
  `Command completed` fired for every quiet pane and every shell command, and
  because repeats jump back to the top of the queue they permanently occupied
  the ten rows the sidebar could draw. Both are now off by default. What is
  left is the timeline: turns starting and finishing, permission prompts,
  processes exiting, panes closed or pinned, and MCP agents taking a pane.
- **Click a card to jump to its pane, and scroll the list with the wheel.**
  Both gestures were previously swallowed. Cards name the project and tab they
  will take you to, shrink when they carry no excerpt — so about twice as many
  fit — and mark themselves `(closed)` when their pane is gone.
- **Choose what appears in `F1 → Settings → Notifications`.** Ten event groups
  plus the desktop-toast switches, which used to be reachable only by editing
  `config.toml`. Press `a` in the sidebar to reveal everything for a moment.
  Hiding a group never hides it from MCP agents.
```

Verify the fragment: `./scripts/promote-changelog.sh --validate`

- [ ] **Step 7: Check the agent-context file sizes**

Run: `./scripts/dev.sh docs-size`

Expected: PASS. `.claude/CLAUDE.md` under 100,000 bytes, each rule file under
140,000.

- [ ] **Step 8: Commit**

```bash
git add docs/configuration.md docs/features.md docs/keybindings.md docs/roadmap/notification-center.md .claude/rules/tui-dialogs.md changelog.d/changed-notification-timeline.md
git commit -m "docs: document the notification timeline and its settings"
```

---

## Task 10: Full verification

**Files:** none — this task runs checks and fixes what they surface.

**Interfaces:**
- Consumes: everything.
- Produces: a branch ready for review.

- [ ] **Step 1: Run every affected package's tests**

Run each separately — `dev.sh test` takes ONE package argument and silently
drops the rest:

```bash
./scripts/dev.sh test ./internal/config
./scripts/dev.sh test ./internal/tui
./scripts/dev.sh test ./internal/daemon
```

Expected: all PASS.

- [ ] **Step 2: Race detector**

Run: `./scripts/dev.sh test-race`

Expected: PASS. A green local race run is not proof — CI runs
`go test -race ./...` across every package — but a red one here is definitive.

- [ ] **Step 3: Vet**

Run: `./scripts/dev.sh vet`

Expected: clean.

- [ ] **Step 4: Format only the files you touched**

```bash
git diff --name-only master...HEAD -- '*.go'
```

Run `gofmt -l` on exactly those paths. Never run a directory-wide `gofmt -w`:
in the container it rewrites CRLF to LF and marks ~200 unrelated files
modified.

- [ ] **Step 5: Build**

Run: `./scripts/dev.sh build`

Expected: six binaries plus `quil-activate.exe`. If it refuses because a
running process holds a binary, close the dev TUI first — the build chains with
`&&`, so a refusal partway leaves a new TUI beside a stale daemon.

Confirm the binary timestamps actually moved before trusting the next step.

- [ ] **Step 6: Runtime verification in DEV MODE ONLY**

Never touch the production daemon or `~/.quil/`.

```bash
./scripts/quil-dev.ps1
```

Confirm `[dev]` appears in the status bar, then check each of these:

1. `Alt+N` opens the sidebar. No `Output idle` cards.
2. Run a few shell commands. No `Command completed` cards.
3. Wheel over the sidebar scrolls it. The pane beneath does not scroll.
4. Left-click a card. It jumps to the named pane.
5. `Alt+Backspace` returns.
6. Right-click a card. It is dismissed.
7. Press `a`. The idle cards appear. Press `a` again. They go.
8. `F1 → Settings → Notifications`. Turn `Idle` on. The idle cards appear
   immediately, without leaving the dialog.
9. Close the TUI and relaunch. `Idle` is still on.
10. `cat .quil/config.toml` shows `[notification.events]` with `idle = true`.
11. Turn `Idle` back off and relaunch once more to leave the dev config clean.

- [ ] **Step 7: Commit any fixes**

```bash
git add -- <specific paths only>
git commit -m "fix: <what the verification surfaced>"
```

Stage by path. Never `git add -A` — unrelated work may be staged in this
worktree already.

---

## Self-Review Notes

**Spec coverage:**

| Spec section | Task |
|---|---|
| 5.1 Event groups | 2 |
| 5.2 Config | 1 |
| 5.3 F1 → Settings → Notifications | 6 |
| 5.4 The timeline (filter, variable height, line scroll) | 3, 4 |
| 5.5 Mouse | 5 |
| 5.6 New daemon events | 7, 8 |
| 5.7 Events whose pane is gone | 4 (render), 5 (jump guard, inherited) |
| 5.8 Status-bar badge | 3 (`Count()` returns the visible count; the badge at `model.go:6434` already calls it) |
| 7 Error handling | 1 (defaults), 2 (unknown type), 3 (nil filter, enable reveals), 4 (clamping) |
| 8 Testing | every task, plus 10 |
| 9 Files touched | matches the File Structure table |

**Naming consistency check:** `visibleEvents()` (not `visible()` — it would
collide with the `visible bool` field), `SetGroups`, `groupFilterFrom`,
`eventGroup`, `notificationLines`, `renderedLine`, `paneLocator`,
`notifyViewportOffset`, `notifyViewportHeight`, `eventIndexAtRow`,
`SelectIndex`, `revealCursor`, `ScrollBy`, `clampScroll`, `clampCursor`,
`handleNotificationClick`, `handleNotificationWheel`, `paneLocator()` (Model
method), `notifyToggle`, `notifySettingsRows`, `handleNotifySettingsKey`,
`renderNotifySettingsDialog`, `roleOf`, `notifyMCPControl`, `notifyPaneMark`,
`notifyPaneDestroyed`, `notifyWorktreeReady`, `mcpControlCooldown`. Each is
defined in exactly one task and used with the same spelling everywhere after.

**Known follow-ups the plan does not cover** (deliberately, per the spec's
non-goals): per-event-type overrides, per-pane-type filtering, and a wire
`class` field on `PaneEventPayload`.
