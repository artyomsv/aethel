# Notification Timeline — Design

| Field | Value |
|---|---|
| Date | 2026-09-06 |
| Status | Draft — awaiting review |
| Milestone | M12 follow-up (Notification Center) |
| Branch | `feat/notification-events` |

## 1. Problem

The notification sidebar (`Alt+N`) has been shipped since M12 and is unused in
practice. Three things make it unusable, and they compound.

**It is dominated by telemetry, not news.** Seven producers push events. Two of
them account for almost every card:

| Producer | Site | Cadence |
|---|---|---|
| `output_idle` | `internal/daemon/daemon.go:4990` | any pane quiet for 5 s, 30 s cooldown, forever |
| `command_complete` | `internal/daemon/daemon.go:3399` | every shell command (OSC 133 `D`) |

A production screenshot shows twelve cards, eight of them `Output idle`, with
repeat counts of `×1088`, `×2423` and `×996`. An event that has fired 2423
times is a *status*, not a notification.

**The noise wins the top of the list.** `eventQueue.Push`
(`internal/daemon/event.go:65`) aggregates by `(PaneID, Title)` and re-prepends
the merged entry. `Output idle` carries a constant title, so each repeat jumps
that pane's card back to position 1. The queue holds 200 events; the sidebar
renders about ten. Real events are therefore pushed below the fold by design,
not by volume.

**One queue serves two readers with opposite needs.** The same
`eventQueue` feeds the sidebar *and* the MCP tools `get_notifications` /
`watch_notifications`. An agent polling for "has this pane gone quiet" wants
`output_idle`. A human does not. Today the agent's need is what the human sees.

Two smaller defects sit on top:

- **No mouse.** `sidebarSwallowsMouse` (`internal/tui/model.go:2921`) eats every
  click and wheel notch over the sidebar and does nothing with them. Clicks are
  swallowed at `model.go:1613`, wheel notches at `model.go:2008`.
- **No scrolling.** `NotificationCenter.View` derives its window from the
  keyboard cursor alone (`internal/tui/notification.go:186`). There is no scroll
  offset. Every card is a fixed four lines — including a blank line when the
  event has no excerpt — so the visible count is roughly `(height-5)/4`.

## 2. Goals

1. The sidebar reads as a **timeline of work**: what an agent started, what it
   finished, what it is blocked on, what a human or an agent did to a pane.
2. **The user decides what appears**, from `F1 → Settings → Notifications`, not
   from a hard-coded list.
3. **Mouse works**: wheel scrolls, left-click jumps to the pane.
4. **More events fit** on screen.
5. Notifications become useful **on every platform**, not only where Windows
   toasts are available.

## 3. Non-goals

- No change to the daemon `eventQueue`, its bound, its aggregation, or the
  attach replay.
- No change to the three MCP notification tools. Agents keep receiving every
  event, filtered or not.
- No change to the desktop-toast *trigger* model (`internal/tui/notify.go`),
  which is driven by the attention model (`blockedSince` / `unseen`) and not by
  the event queue. Only where its settings live changes.
- No per-pane-type filtering. One on/off per group, global.
- No new wire field on `ipc.PaneEventPayload`.

## 4. Current implementation

### 4.1 Producers

| Event `Type` | Emitted at | Meaning |
|---|---|---|
| `output_idle` | `daemon.go:4990` | pane silent 5 s |
| `command_complete` | `daemon.go:3399` | OSC 133 `D;<code>` |
| `process_exit` | `daemon.go:3215` | child exited |
| `bell` | `daemon.go:3371` | `\a` in pane output |
| `input_blocked` | `daemon.go:2766` | PTY stdin queue full |
| `hook.<src>.<event>` | `daemon.go:4902` (`emitHookEvent`) | Claude / OpenCode / Codex hooks |

`hook.*` titles come from the hook binaries: `Working on: …`
(`UserPromptSubmit`), `Reply ready` (`Stop`), `Session ended`, `Compaction
complete`, `Working` (`PreToolUse`), and the permission / notification titles.

`hookevents.IsWorkStateOnly` (`internal/hookevents/workstate.go:102`) already
excludes `PostToolUse` and `PreToolUse` from the queue — they drive the spinner
only. That predicate stays exactly as it is.

### 4.2 Consumer

`NotificationCenter` (`internal/tui/notification.go`) holds a flat
`[]ipc.PaneEventPayload`, a `cursor`, `visible` and `focused` flags. `View`
renders four lines per card. `HandleKey` maps `up/down/enter/d/D/esc`.
`Model.handleNotificationKey` (`internal/tui/model.go:2726`) performs the jump,
the dismiss and the unfocus.

### 4.3 Settings

`settingsFields()` (`internal/tui/dialog.go:208`) returns 19 rows.
`renderSettingsDialog` (`dialog.go:1568`) renders all of them, unwindowed and
unscrolled. `Desktop notifications` is one of those rows and exposes only
`DesktopConfig.Enabled`; `Blocked` and `Done` are reachable only by editing
`config.toml` by hand.

`dialogPlugins` is the existing precedent for a nested screen reached from the
`F1` tree, with `Esc` returning to its parent.

## 5. Design

### 5.1 Event groups

A new file `internal/tui/notification_class.go` owns one table mapping an event
`Type` to exactly one **group**. Groups are the unit the user toggles.

| Group | Default | Event types |
|---|---|---|
| `agent_turn` | on | `hook.*.UserPromptSubmit`, `hook.*.Stop`, `hook.*.StopFailure`, `hook.opencode.chat.message`, `hook.opencode.session.idle`, `hook.opencode.session.error` |
| `agent_blocked` | on | `hook.*.PermissionRequest`, `hook.claude.Notification`, `hook.opencode.permission.ask`, `bell` |
| `agent_subagent` | on | `hook.*.SubagentStart`, `hook.*.SubagentStop`, `hook.claude.TaskCreated`, `hook.claude.TaskCompleted` |
| `agent_session` | on | `hook.*.SessionEnd`, `hook.*.PreCompact`, `hook.*.PostCompact` |
| `process` | on | `process_exit` |
| `pane` | on | `pane_destroyed`, `pane_pinned`, `pane_unpinned`, `pane_marked_deletion`, `pane_unmarked_deletion` |
| `mcp` | on | `mcp_control` |
| `system` | on | `input_blocked`, `worktree_ready`, **and every unrecognised type** |
| `commands` | **off** | `command_complete` |
| `idle` | **off** | `output_idle` |

Ten groups, not the nine sketched during brainstorming: `agent_subagent` was
split out of `agent_session` because Claude Code runs subagents in parallel and
a run with five of them produces ten cards that a user may reasonably want to
silence without losing session-level events.

**Unrecognised types map to `system`, which defaults on.** This follows the
rule already stated for plugin availability in `.claude/rules/remote-dialogs.md`:
a wrong extra card is visible and self-correcting, a wrong hidden card is
silent. It also makes a NEWER daemon paired with an OLDER TUI degrade safely —
new event types appear as ordinary cards rather than vanishing.

The mapping is **prefix-aware for the hook namespace**: `hook.<src>.<event>` is
matched on `<event>` after stripping the two leading segments, so a fourth agent
source needs no table change. A literal, non-hook type is matched whole.

### 5.2 Config

```toml
[notification.events]
agent_turn     = true
agent_blocked  = true
agent_subagent = true
agent_session  = true
process        = true
pane           = true
mcp            = true
system         = true
commands       = false
idle           = false
```

New struct in `internal/config/config.go`:

```go
// EventGroupsConfig selects which notification groups the sidebar shows.
// Consumed ONLY by the TUI: the daemon queue and the MCP tools are unfiltered
// by design, so an agent keeps seeing everything a human has hidden.
type EventGroupsConfig struct {
    AgentTurn     bool `toml:"agent_turn"`
    AgentBlocked  bool `toml:"agent_blocked"`
    AgentSubagent bool `toml:"agent_subagent"`
    AgentSession  bool `toml:"agent_session"`
    Process       bool `toml:"process"`
    Pane          bool `toml:"pane"`
    MCP           bool `toml:"mcp"`
    System        bool `toml:"system"`
    Commands      bool `toml:"commands"`
    Idle          bool `toml:"idle"`
}
```

Added to `NotificationConfig` as `Events EventGroupsConfig \`toml:"events"\``,
with the defaults above set in `config.Default()`.

`config.Load` starts from `Default()` and decodes over it
(`internal/config/config.go:597`), so a `config.toml` written before this change
keeps every default and no existing sidebar goes blank on upgrade. `Save`
serialises the whole struct, so the section appears on disk after the first
edit — the same behaviour every other section already has.

Plain `bool` rather than `*bool`: there is no meaningful third state here, and
the load path supplies the defaults.

### 5.3 F1 → Settings → Notifications

One new row in `settingsFields()` is **not** enough — `settingsField` describes
a value editor, not a navigation target. Instead:

- New `dialogScreen` constant `dialogNotifySettings` in `internal/tui/model.go`.
- New row in the `F1 → Settings` list rendered as a submenu entry
  (`Notifications  ▸`) whose `Enter` sets `m.dialog = dialogNotifySettings`.
  Implemented with a new `submenu bool` flag on `settingsField` so the row
  declares its own behaviour, matching how `relayout` is already handled
  (`dialog.go:200`) rather than comparing labels at the call site.
- The existing `Desktop notifications` row is **removed** from
  `settingsFields()` and re-homed in the new screen.
- `Esc` in the new screen returns to `dialogSettings`, matching `dialogPlugins`.

Layout:

```
Notifications

  Desktop toasts
    Enabled                     on
    On blocked (needs you)      on
    On done (turn finished)     on

  Sidebar events
    Agent turns                 on    Working on… / Reply ready
    Agent blocked               on    permission, waiting for you
    Agent subagents             on    subagent + task start/stop
    Agent session               on    session end, compaction
    Process                     on    exited / failed
    Pane                        on    closed, pinned, marked
    MCP                         on    an agent drove a pane
    System                      on    input blocked, worktree, unknown
    Commands                    off   every shell command
    Idle                        off   output idle

  ↑↓ navigate   Enter toggle   Esc back
```

Thirteen toggle rows plus two section headings and chrome. At
`dialogWidth` (60) with the standard 24-cell label column, the trailing
description fits in the remaining budget; it is truncated through the same
`dialogInnerWidth` helper the Shortcuts screen uses so a narrow terminal cannot
overflow the box.

Every toggle sets `m.configChanged = true`. Sidebar-event toggles apply
**live** — the filter is read on every render — which puts them in the same
class as `Sidebar width`, the one existing live row (`dialog.go:294`). Toast
toggles apply live too; `raiseAttentionToast` reads `m.cfg` per call.

### 5.4 The timeline

`NotificationCenter` gains:

```go
groups  map[string]bool // group -> shown; nil means show everything
showAll bool            // 'a' override, session-only, not persisted
scroll  int             // FIRST VISIBLE LINE, not card
```

**Filtering.** A new `visible() []ipc.PaneEventPayload` returns the events whose
group is enabled, or all of them when `showAll` is set. **Every existing
operation moves onto this list**: `cursor` indexes `visible()`, and
`DismissSelected`, `SelectedEvent` and `HandleKey` must all resolve through it.
This is the main correctness hazard in the change — `DismissSelected` currently
slices `nc.events` by `cursor` directly (`notification.go:88`), and left as-is it
would dismiss the wrong event as soon as anything is hidden. Dismissal resolves
the event **by ID** against `nc.events` after reading the ID from `visible()`.

`AddEvent` keeps storing everything unfiltered. Turning a group back on must
reveal the events that arrived while it was off.

**Variable card height.** A card is:

- line 1 — pane name (severity-coloured) + right-aligned relative age
- line 2 — title, plus `×N` badge when aggregated
- line 3 — location: `project · tab` (dim), **new**
- line 4 — excerpt, **only when `Message` is non-empty**

Cards are separated by the existing dotted rule. Dropping the always-emitted
blank excerpt line and adding the location line is height-neutral for events
that carry an excerpt and one line cheaper for those that do not; the real gain
comes from the scroll offset, which removes the need for the card window to
start at a card boundary.

**Line-based scrolling.** One pure function is the single source of truth for
geometry:

```go
// notificationLines renders the filtered events to a flat []renderedLine,
// where each line carries the index of the event it belongs to (-1 for
// chrome). View slices it by nc.scroll; the hit test looks up by index.
func notificationLines(events []ipc.PaneEventPayload, innerW int, selected int, focused bool) []renderedLine
```

`View` calls it and slices `[scroll : scroll+innerH]`. The hit test calls it and
indexes. They cannot disagree about which card owns a screen row.

`scroll` is clamped to `[0, max(0, len(lines)-innerH)]`. Keyboard `up`/`down`
moves `cursor` and then adjusts `scroll` by the minimum needed to bring the
selected card fully into view. Arrival of a new event at index 0 does **not**
move `scroll` when the user has scrolled away from the top — otherwise reading
history is impossible on a busy workspace.

> Note for the implementation plan: `notificationLines` is shared geometry, and
> the failure mode recorded in project memory is that a test comparing two
> callers of a shared helper becomes a self-comparison. Its tests must assert
> against **fixed expected output**, not against the other caller.

### 5.5 Mouse

In `internal/tui/model.go`, the two blanket swallows are replaced by handlers.
Ordering relative to the viewer, modal, overlay and context-menu checks is
unchanged — the sidebar keeps its current place in the priority chain.

**Click** (`model.go:1613`, currently `clearDragState(); return m, nil`):

| Button | Behaviour |
|---|---|
| Left, on a card | focus the sidebar, move `cursor` to that card, jump to the pane (the existing `navigate` path) |
| Left, on chrome | focus the sidebar only |
| Right, on a card | dismiss that card |
| Any, pane gone | focus + select, no jump (see 5.7) |

`clearDragState()` is still called first: a press over the sidebar must not
leave a half-armed pane drag behind.

**Wheel** (`model.go:2008`): scroll by `m.cfg.UI.MouseScrollLines`. Both
vertical buttons are matched explicitly and the swallow stays outside the
switch, exactly as `projectSidebarSwallowsMouse` does at `model.go:2015` — a
horizontal notch must not fall through to the pane beneath.

Motion and release stay swallowed, unchanged.

**Footer hints** become `↑↓ ⏎ go  d/D  a all  Esc` when focused.

### 5.6 New daemon events

All four are ordinary `d.emitEvent(PaneEvent{…})` calls. No new IPC message
types, no queue changes.

**`mcp_control`** — an MCP bridge acted on a pane.

Detection is honest rather than heuristic: `ClientHelloPayload.Role`
(`internal/ipc/protocol.go:928`) is already `"tui"` or `"bridge"`, and
`helloRegistry` (`internal/daemon/procreport.go:253`) records it per connection.
A new accessor `helloRegistry.roleOf(conn) string` is added, and
`handlePaneInput` (`daemon.go:2687`) and `handleRestartPaneReq`
(`daemon.go:5502`) — both of which already receive `conn` — emit when the role
is `"bridge"`.

Deliberately not "an ID-bearing request": `Message.ID` is a correlation id, and
any future request-response caller would be misreported as an agent.

Destroy is **not** covered here. The two destroy paths are split —
`handleDestroyPane` (`daemon.go:2552`, TUI, takes no `conn`) and
`handleDestroyPaneReq` (`daemon.go:5741`, MCP, takes `conn`) — so a
`mcp_control` card would be a second card describing the same act. The
`pane_destroyed` event below carries the actor instead.

Rate limit: a new `Pane.LastMCPEventAt` beside `LastBellEventAt`
(`internal/daemon/session.go:138`), guarded by `PluginMu`, 30 s cooldown,
copying `notifyInputBlocked` (`daemon.go:2757`) exactly. **The emit happens
after `PluginMu` is released** — `emitEvent` re-locks the same mutex for the
mute check, and Go mutexes are not reentrant. This is the 2026-06-12
daemon-wide freeze recorded in the `bell` handler's comment
(`daemon.go:3358`), and it is the single easiest way to reintroduce it.

Title: `MCP agent typed here` / `MCP agent restarted this pane`. Severity
`info`.

**`pane_pinned` / `pane_unpinned` / `pane_marked_deletion` /
`pane_unmarked_deletion`** — emitted from `handleUpdatePane` (`daemon.go:2867`)
when `PinnedAttention` or `MarkedForDeletion` is non-nil **and the value differs
from the pane's current one**. Four explicit types rather than two with a
boolean in `Data`, so the sidebar title needs no branching and the aggregation
key `(PaneID, Title)` cannot merge a pin with an unpin.

The tri-state pointers are what make this correct: a no-op write from an OSC 7
CWD update carries `nil` for both fields and emits nothing. That is the reason
those fields are pointers in the first place (`internal/ipc/protocol.go:527`),
and it is load-bearing here — panes carrying a deletion mark are exactly the
ones still reporting `cd` from a background job.

`Unseen` is deliberately excluded even though it rides the same payload: it is
set and cleared constantly by ordinary focus changes, and would be pure
telemetry.

**`pane_destroyed`** — there are two destroy paths and they do not share a
funnel, so a new helper `d.notifyPaneDestroyed(pane, by string)` is called from
both, immediately before `session.DestroyPane`:

- `handleDestroyPane` (`daemon.go:2552`) → `by = "user"`
- `handleDestroyPaneReq` (`daemon.go:5741`) → `by = "mcp"`

`Name` and `TabID` must be read inside the helper, before the call — the pane is
removed from the session maps by `DestroyPane` and the record is then gone.
`Data["by"]` carries the actor; the card title reads `Pane closed` or
`Pane closed by MCP agent`.

Not emitted for overlay panes (`pane.Overlay`): those are auto-destroyed on
exit by `onPaneExit` (`daemon.go:3215`) as ordinary lifecycle, and a card per
`Alt+G` toggle is exactly the telemetry this change removes.

**`worktree_ready`** — emitted from `worktreeAddAndCreate`
(`internal/daemon/worktree_add.go:68`) on the success return, carrying the
branch in `Data["branch"]`. Not on the failure returns: those already surface as
`SpawnError` in the placeholder pane.

### 5.7 Events whose pane is gone

`pane_destroyed` is the first event type that intentionally outlives its pane,
and `mcp_control` on a closed pane is reachable too.

`handleNotificationKey`'s `navigate` case already guards this
(`model.go:2735`): it looks the pane up first and returns without pushing
history when the pane is nil. The click path reuses that same function, so the
guard is inherited rather than duplicated.

Additionally, such a card renders **dim** — the pane-name style drops to grey
regardless of severity — so the sidebar never offers a jump it cannot perform.
The check is `m.findPaneAndTab(e.PaneID) == nil`, evaluated at render time.

### 5.8 Status-bar badge

`model.go:6434` counts `m.notifications.Count()`. It changes to count the
**visible** set, so the badge and the sidebar cannot disagree. A workspace with
nothing but hidden idle chatter shows no badge, which is the point.

## 6. Data flow

```
hook / PTY / IPC handler
        │
        ▼
daemon.emitEvent ────► eventQueue.Push (unfiltered, bounded, aggregating)
        │                      │
        │                      └──► MCP: get_notifications / watch_notifications
        ▼                            (UNFILTERED — agents see everything)
   broadcast MsgPaneEvent
        │
        ▼
Model.Update ──► NotificationCenter.AddEvent   (stores everything)
                          │
                          ▼
                 visible()  ← cfg.Notification.Events + showAll
                          │
                          ▼
                 notificationLines(...)  ← ONE geometry function
                     │            │
                   View        hit test
```

The filter sits at exactly one point, on the render side of the client. Nothing
upstream of it changes.

## 7. Error handling and edge cases

| Case | Behaviour |
|---|---|
| `config.toml` with no `[notification.events]` | defaults from `Default()`; nothing hidden that was visible before |
| All ten groups off | sidebar shows the "No notifications" empty state, badge shows nothing; `a` still reveals everything |
| Unknown event `Type` (newer daemon) | group `system`, shown |
| Old daemon (no new event types) | fewer kinds of card, no error |
| Group toggled on after events arrived | events reappear — `AddEvent` never dropped them |
| Event's pane destroyed | card dims, click and `Enter` are no-ops |
| Terminal too narrow | `sidebarOverlayWidth()` returns 0 and the overlay is not drawn; the mouse handlers inherit that check via `sidebarSwallowsMouse` and never claim a click |
| `scroll` beyond the end after a dismiss | clamped on every render |
| Remote daemon | unchanged; the filter is client-side and needs nothing from the daemon |

## 8. Testing

**`internal/config`**

- `Default()` returns the ten documented defaults.
- A `config.toml` with no `[notification.events]` round-trips to the defaults.
- A partial section (`idle = true` only) keeps the other nine defaults.

**`internal/tui` — classification**

- Table test: every event type listed in 5.1 maps to its documented group.
- `hook.<newsource>.Stop` maps to `agent_turn` (prefix stripping works).
- An invented type maps to `system`.

**`internal/tui` — filtering**

- With `idle` off, an `output_idle` event is stored but not visible.
- Toggling `idle` on reveals the previously hidden event.
- `showAll` reveals everything without mutating the config.
- **Dismiss selects the right event when the list is filtered** — the regression
  this refactor is most likely to introduce. Fixture: hidden, visible, hidden,
  visible; cursor on the second visible; assert the dismissed ID.

**`internal/tui` — geometry**

- `notificationLines` output for a fixed three-event fixture, asserted against
  literal expected lines (not against `View`).
- A card with no `Message` is one line shorter than one with it.
- `scroll` clamps at both ends.
- A new event arriving while scrolled away does not move `scroll`.

**`internal/tui` — mouse**

- Driven through `Update` with a real `tea.MouseClickMsg`, not by calling the
  handler directly. Project memory: a direct-call test can pass against code the
  call site makes unreachable.
- Left-click on a card's second line selects that card and jumps.
- Left-click on the title row (chrome) focuses without jumping.
- Wheel over the sidebar changes `scroll` and does **not** reach the pane.
- Wheel just outside the sidebar's left edge still reaches the pane.

**`internal/daemon`**

- `mcp_control` fires for a `"bridge"` role and not for `"tui"`.
- Second bridge write inside the cooldown emits nothing.
- `pane_pinned` fires on a real change, and not when the pointer is nil or the
  value is unchanged.
- `pane_destroyed` carries `Data["by"] == "user"` from `handleDestroyPane` and
  `"mcp"` from `handleDestroyPaneReq`, and carries the pane's name in both.
- `pane_destroyed` is not emitted for an overlay pane.
- `worktree_ready` fires on success and not on the failure returns.

**Commands**

```bash
./scripts/dev.sh test ./internal/tui
./scripts/dev.sh test ./internal/config
./scripts/dev.sh test ./internal/daemon
./scripts/dev.sh vet
```

`dev.sh test` takes **one** package argument — extra arguments are silently
dropped, so these are four separate runs.

**Runtime check** (dev daemon only, never production):

1. `./scripts/dev.sh build`, confirm binary mtimes moved.
2. `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar.
3. Click a card → lands on the named pane.
4. Wheel over the sidebar → it scrolls; the pane beneath does not.
5. `F1 → Settings → Notifications`, turn `Idle` on → idle cards appear.
6. Restart the TUI → the setting persisted.

## 9. Files touched

| File | Change |
|---|---|
| `internal/config/config.go` | `EventGroupsConfig`, field on `NotificationConfig`, defaults |
| `internal/tui/notification_class.go` | **new** — type → group table, prefix stripping |
| `internal/tui/notification.go` | filter, line scroll, variable height, `notificationLines`, dim-when-gone |
| `internal/tui/model.go` | `dialogNotifySettings` const, click + wheel routing, badge counts visible |
| `internal/tui/dialog.go` | `submenu` flag, new screen handler + renderer, `Desktop notifications` row re-homed |
| `internal/daemon/daemon.go` | `mcp_control` (`handlePaneInput`, `handleRestartPaneReq`), pin/mark events in `handleUpdatePane`, `notifyPaneDestroyed` from both destroy paths |
| `internal/daemon/worktree_add.go` | `worktree_ready` |
| `internal/daemon/session.go` | `Pane.LastMCPEventAt` |
| `internal/daemon/procreport.go` | `helloRegistry.roleOf` |
| `docs/configuration.md` | `[notification.events]` reference |
| `docs/features.md` | timeline + mouse description |
| `docs/keybindings.md` | `a` in the sidebar |
| `docs/roadmap/notification-center.md` | Phase 4 entry |
| `changelog.d/feat-notification-timeline.md` | fragment with `headline:` |
| `.claude/rules/tui-dialogs.md` | the new nested dialog |

## 10. Open question deferred

Per-event-type control (rather than per-group) is deliberately out of scope.
Event types are internal strings that change whenever an upstream tool adds a
hook, so a config keyed on them rots. Groups are stable and a new type joins an
existing one. If group control proves too blunt in use, a `[notification.types]`
override map can be layered on top later without changing anything specified
here.
