# MCP enhancements: richer pane creation, projects, remote hosts, agent tasking

Date: 2026-09-10. Status: approved for implementation (autonomous goal session).

## Why

The MCP bridge (`quil mcp`) exposes 18 tools. They cover one local daemon, one
tab-and-pane model, and a "type text, then poll" way of talking to another AI
pane. Four things a user of Quil-as-orchestrator needs are missing:

1. **AI pane options.** The Ctrl+N dialog collects CWD, worktree, sandbox, auth
   mode, resume-session and per-plugin toggles (permission mode, `--chrome`,
   `--search`). `create_pane` sends `tab_id`, `cwd`, `type` and nothing else.
2. **Projects.** Every project message is fire-and-forget and there is no
   list. Tabs and panes do not say which project they belong to.
3. **Remote hosts.** The TUI drives N daemons through its Router; the bridge
   dials the local socket only. Nothing in MCP can see or act on a remote
   project.
4. **Agent-to-agent work.** Pane A "asks" pane B for work by simulating
   keystrokes and then polls. Nothing correlates the request with B's
   completion, `hook.claude.Stop` fires before background subagents drain,
   and no signal ever reaches A unless A blocks in `watch_notifications`.

## Facts the design rests on (from the code survey)

- `CreatePanePayload` already carries `ResumeSessionID`, `Worktree *WorktreeSpec`
  and `Sandbox *SandboxSpec`. The daemon validates each. Their doc comments say
  "the MCP bridge deliberately does not expose this field"; this spec reverses
  that decision on purpose, keeping the daemon-side validation as the guard.
- `InstanceArgs` REPLACE the plugin's `Command.Args`. Exposing them raw lets an
  agent spawn any program with any argv under the plugin's name. The dialog
  never sends free-form args for AI panes: it sends the `ArgsWhenOn` of named
  TOML toggles. MCP will do the same, by toggle NAME, resolved daemon-side.
- `WorktreeSpec.RepoRoot` must be the daemon's own answer. The daemon has the
  resolver behind `MsgWorktreeListReq`; MCP callers pass a branch and the
  daemon resolves the root from the CWD.
- A worktree-bearing create answers on `MsgCreatePaneResp` with `Error`; an
  ordinary create is answered by the next broadcast. `create_pane_req` never
  sets `Error`. It will.
- `handleCreateTab` files the tab under the ACTIVE project. `CreateTabPayload`
  has no `ProjectID`.
- Project handlers never call `respondTo`. The TUI sets no `Message.ID`.
- `Project` has no `Dest`; remoteness is a client concept. Hosts live in the
  bridge machine's `config.toml` as `[[destinations]]`, and `dialExtra` /
  `dialRemoteTransportFn` + `gateExtraVersion` already dial and gate one.
- The daemon holds NO work state. `working` / `blocked` are derived in
  `internal/tui/workstate.go` from hook events; the classifier is already in
  the shared `internal/hookevents` package. `PreToolUse` heartbeats are
  broadcast-only, so MCP cannot see "still working".
- Every AI pane's child gets `QUIL_PANE_ID` in its environment. The bridge is
  that child's child, so `os.Getenv("QUIL_PANE_ID")` tells the bridge which
  pane it is running inside.
- `send_to_pane` appends `\n` with no bracketed paste and no delay.

## Assumptions

- One PR, one changelog fragment. Docs (`docs/mcp.md`) updated in the same PR.
- Remote hosts for MCP are the `[[destinations]]` of the machine that runs
  `quil mcp`. `quil --remote` sessions stay refused (`refuseRemoteMCP`).
- "Task done" means: the target pane's work ledger fell to idle (turn over AND
  no live subagents) and stayed idle for a settle window (2 s). For a terminal
  pane it means OSC 133 `command_complete` or process exit.
- Notify-back to the requesting pane is a typed line, delivered only while the
  requester is idle; otherwise it waits for the requester's next idle edge.

## Area 1: pane creation with the dialog's options

### Tool changes

`create_pane` gains these optional inputs (all omitempty):

| Input | Type | Meaning |
|---|---|---|
| `name` | string | pane label (same as Alt+F2) |
| `toggles` | []string | plugin toggle names, e.g. `dangerously_skip_permissions`, `chrome`, `search` |
| `resume_session_id` | string | Claude session UUID to `--resume` |
| `worktree_branch` | string | create a NEW linked worktree on this branch off the repo that contains `cwd`, spawn there |
| `sandbox_image` | string | run in a Docker container from this image |
| `sandbox_auth` | string | `token` or `browser`; empty follows `[sandbox] auth` |
| `host` | string | see Area 3 |

New tools:

- `create_tab` — `name`, `project_id` (default: active project), `host`, plus a
  `first_pane` object with the same fields as `create_pane` minus `tab_id`.
  Returns `{tab_id, pane_id, preparing_worktree?, error?}`.
- `list_plugins` — every plugin the daemon knows: `name`, `display_name`,
  `category`, `available`, `prompts_cwd`, `sessions` (resume supported),
  `toggles[]` `{name, label, group, default}`. Lets an agent discover what
  `toggles` accepts instead of guessing.
- `list_sessions` — `cwd` → the Claude sessions the daemon lists for that
  directory (`{id, title, modified_ms, in_use_pane_id}`), for
  `resume_session_id`.
- `rename_pane` — `pane_id`, `name`.

### Wire changes (`internal/ipc/protocol.go`)

`CreatePaneReqPayload` gains `Name`, `Toggles []string`, `ResumeSessionID`,
`WorktreeBranch`, `Sandbox *SandboxSpec`. `CreatePaneRespPayload` is reused as
is (`Error`, `PaneID`, `TabID`).

`CreateTabPayload` gains `ProjectID string` (omitempty; empty keeps today's
active-project behaviour). New `MsgCreateTabReq` / `MsgCreateTabResp` with
`CreateTabReqPayload{Name, ProjectID, FirstPane *CreatePaneReqPayload-shaped
spec}` and `CreateTabRespPayload{TabID, PaneID, PreparingWorktree, Error}`.

New `MsgPluginCatalogReq` / `MsgPluginCatalogResp` (the existing
`MsgPluginListReq` answers availability only and is remote-mode scoped; a new
message keeps that contract untouched).

`MsgUpdatePane` answers `MsgPaneOpResp{OK, Error}` when the request carries an
ID (same pattern as `pane_input_resp`).

### Daemon changes

- `handleCreatePaneReq` is rewritten to BUILD a `CreatePanePayload` and hand it
  to the same path the TUI uses (`handleCreatePane`'s body, factored into
  `createPaneFromPayload(payload) (CreatePaneRespPayload)`), so worktree,
  sandbox and resume all go through the validated code. `Toggles` are resolved
  to `InstanceArgs` by `resolveToggles(plugin, names)`: unknown name → error,
  two names in one group → error. `Name` is applied after construction.
- `WorktreeBranch` → the daemon resolves the repo root of the (validated) cwd
  with the existing worktree-list resolver; not a repository → error. The
  resulting `WorktreeSpec{RepoRoot, Branch}` goes through
  `worktreeAddAndCreate` unchanged. The response is the worker's own.
- Spawn failure sets `Error` from `pane.SpawnError` in the response.
- `handleCreateTabReq` → `CreateTabInProject`, then the first pane via the
  same builder. Worktree first pane returns the placeholder id and
  `preparing_worktree` (the pane that replaces it carries a new id; the agent
  watches the `worktree_ready` event, which names both).

### Bridge

Input validation stays thin; the daemon owns every refusal. The tool
description tells the agent to call `list_plugins` first.

## Area 2: projects

New tools: `list_projects`, `create_project`, `update_project`,
`destroy_project` (destructive — description says confirm), `switch_project`.

Wire: `MsgListProjectsReq/Resp` with `ProjectInfo{ID, Name, RootDir, Active,
Bootstrap, TabIDs, ActiveTab}`; `MsgCreateProjectReq/Resp` returning the id;
the four fire-and-forget mutations (`update`, `destroy`, `merge`, `switch`,
`reorder`) answer `MsgProjectOpResp{ProjectID, OK, Error}` whenever the message
carries an ID (the TUI never sets one, so nothing changes for it).

`TabInfo` and `PaneInfo` gain `ProjectID` (omitempty). `list_tabs` gains an
optional `project_id` filter.

Tabs: `rename_tab` (`MsgUpdateTab` + op-resp), `destroy_tab`
(`MsgDestroyTab` + op-resp; destructive), `create_tab` (Area 1).

Not in scope: moving a single tab between projects (no daemon primitive
exists; `MergeProjects` moves all tabs). Recorded as a follow-up.

## Area 3: remote hosts

### Model

The bridge becomes a small router: `map[host]*mcpBridge`, host `""` = local.
At startup it reads `cfg.Destinations` and dials each in the background with
`dialExtra`'s ingredients (`dialRemoteTransportFn` batch=true, then
`gateExtraVersion`, then `sendClientHello(bridge)` and `declinePaneOutput`).
A host that fails to dial is recorded with its error; a later tool call that
names it retries (30 s backoff).

### Addressing

Every tool that takes an id gains optional `host`. Resolution order:

1. `host` given → that host (error if unknown / disconnected).
2. Else the bridge's id→host cache (filled by every list call and every
   create response) → that host.
3. Else local.

`list_panes`, `list_tabs`, `list_projects` aggregate across every connected
host; each remote entry carries `host`. `list_hosts` returns every configured
destination with `connected`, `version`, `error`.

`get_notifications` aggregates. `watch_notifications` fans out one watcher per
connected host and returns the first event; the others are one-shot and expire
on their own timeout. `dismiss_notifications` needs `host` for a remote event
id (the event carries it).

Task tools (Area 4) are per-host: requester and target must be on the same
host; the tool refuses otherwise.

## Area 4: agent tasking

### Daemon-side work ledger

A new pure type `hookevents.WorkLedger` with `Apply(eventType string, data
map[string]string) Transition` implements the turn / subagent / park logic that
`internal/tui/workstate.go` holds today (the TUI is NOT refactored in this PR;
the ledger is a second consumer of the same classifier). The daemon keeps one
per pane, fed from `emitEvent` (including the broadcast-only heartbeats and
muted-pane events, so the ledger sees what the TUI sees) and from
`process_exit`.

Exposed: `PaneInfo.AgentState` and `PaneStatusRespPayload.AgentState` —
`working` | `blocked` | `idle` | `""` (not an AI pane or no hooks yet) — plus
`BlockedReason` and `LastIdleAt` (Unix ms).

The daemon emits a queued event `agent_idle` ("Turn finished") on the falling
edge of `working` after the settle window, and `agent_blocked` is NOT added
(the existing `PermissionRequest` / `Notification` events already queue).

### Tasks

`MsgDelegateTaskReq{ToPane, Prompt, FromPane, Notify bool, TimeoutMs}` →
`MsgDelegateTaskResp{TaskID, Error}`.

The daemon:

1. Refuses when the target has no process, or is a worktree placeholder.
2. Records `Task{ID "task-xxxxxxxx", From, To, Prompt (first 200 chars),
   CreatedAt, State}` in a bounded runtime registry (200, oldest done evicted).
3. Delivers the prompt: AI pane → bracketed paste of the text, 100 ms, then
   `\r`; terminal → text + `\n`. Same per-pane input queue as every write.
4. State machine per target edges: `sent` → `working` (first start edge or
   heartbeat) → `done` (falling edge + 2 s settle, or `command_complete` for a
   terminal) / `failed` (process exit, or `StopFailure` with no live agents)
   / `timeout`. On `done` the last 30 stripped lines of the target's output
   are captured as `Result`.
5. On a terminal state: push a `task_done` PaneEvent on the TARGET pane with
   `data{task_id, state, from_pane, to_pane}` and the result excerpt, and
   wake any `wait_task` waiter. If `Notify` and `From` is a live AI pane,
   type into `From`: `[quil task <id>] pane <to> (<name>) <state>. Last
   output: <one line>. Use get_task <id> for details.` — delivered when
   `From` is idle; queued on the task until then (a start edge on `From`
   defers, its next falling edge delivers).

Tools:

- `delegate_task` — `pane_id`, `prompt`, `notify` (default true), `timeout`
  (seconds, default 0 = none), `host`. `from_pane` is the bridge's own
  `QUIL_PANE_ID` (a bridge outside any pane has none; then `notify` is
  ignored).
- `get_task` — `task_id` → full record incl. result excerpt.
- `wait_task` — `task_id`, `timeout` (≤300 s) → blocks until terminal.
- `list_tasks` — optional `pane_id` (as target or requester).
- `send_to_pane` gains `paste` (bool): bracketed paste + delayed Enter.
- `list_panes` marks the calling pane with `"self": true`; `agent_state` is
  included for every pane.

### What this does not do

It does not parse the target's reply. The result excerpt is raw output; the
requester decides what to do with it (read more with `read_pane_output`,
follow up with another `delegate_task`).

## Error handling

Every new daemon handler answers on the request's ID, including on decode
failure, with an `Error` string; no new fire-and-forget paths. Every tool turns
a non-empty `Error` into a tool error so the agent sees a failure, not a
success string.

## Testing

- `internal/hookevents`: `WorkLedger` table tests mirroring the TUI cases
  (start/stop, subagent ledger with empty-key stop, StopFailure with
  agent_type, park vs idle Notification, abort, overflow).
- `internal/daemon`: request/response tests for every new `*_req` (through
  `handleMessage`, per the "test through the call site" memory): create with
  toggles (unknown, group clash), create with `worktree_branch` outside a repo,
  `create_tab_req` with `project_id`, project list/create/op-resp, task
  lifecycle driven by synthetic hook events including the settle window and
  the deferred notify-back.
- `cmd/quil`: bridge routing (host resolution order, id cache), `delegate_task`
  from-pane resolution from `QUIL_PANE_ID`, aggregation of list calls.
- Docs: `docs/mcp.md` tool table updated; tool count in `CLAUDE.md` and
  `docs/mcp.md` updated.
