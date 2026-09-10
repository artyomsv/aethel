# MCP Enhancements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `quil mcp` so an agent can create AI panes with the dialog's options, manage projects and tabs, reach remote hosts, and delegate work to another pane with completion notification.

**Architecture:** Daemon-side additions are all request/response IPC pairs answered on `Message.ID` (`respondTo`), plus a per-pane work ledger fed from `emitEvent` and a runtime task registry. The bridge becomes a host router (`map[host]*mcpBridge`) with an id→host cache; tools gain an optional `host` input. Nothing new is fire-and-forget.

**Tech Stack:** Go 1.25, `github.com/modelcontextprotocol/go-sdk/mcp`, existing `internal/ipc` framing. Build/test only through `./scripts/dev.sh test <pkg>` (Docker; one package per call).

**Spec:** `docs/superpowers/specs/2026-09-10-mcp-enhancements-design.md`

## Global Constraints

- Every new daemon handler answers on the request ID, including decode failure, with an `Error` string.
- `InstanceArgs` are never accepted raw from MCP; only toggle NAMES, resolved daemon-side.
- `WorktreeSpec.RepoRoot` is always resolved by the daemon from the validated CWD.
- New `FirstPaneSpec`-like fields must be copied in `firstPanePayload` AND `createFirstPaneWorktree` (`internal/daemon/daemon.go:2066`, `:2157`).
- Tests that drive `handleMessage` use `newTestDaemon(t)` (no `t.Parallel()`); socket-level wiring tests use `overlayServerDaemonWithConfig`.
- Never touch `~/.quil`; tests set `QUIL_HOME` via `newTestDaemon`.
- `dev.sh test` takes ONE package argument.
- Commit messages: imperative, ≤72 chars, no AI attribution.

---

### Task 1: `hookevents.WorkLedger` — pure per-pane work state

**Files:**
- Create: `internal/hookevents/ledger.go`
- Test: `internal/hookevents/ledger_test.go`

**Interfaces:**
- Produces:
  ```go
  type WorkLedger struct { /* turnActive bool; subagents map[string]int; overflow bool; blockedSince time.Time; blockedReason string; lastIdleAt time.Time */ }
  type WorkState string // "" | "working" | "blocked" | "idle"
  type Transition struct { Was, Now WorkState; FellIdle bool; Aborted bool }
  func (l *WorkLedger) Apply(eventType string, data map[string]string, now time.Time) Transition
  func (l *WorkLedger) State() WorkState
  func (l *WorkLedger) BlockedReason() string
  func (l *WorkLedger) LastIdleAt() time.Time
  ```
- `State()` returns `""` until the first classified event (so a terminal pane stays `""`).

- [ ] **Step 1: Write failing tests** — table cases: start→working; Stop→idle with `FellIdle`; SubagentStart(agent_type=qa)+Stop→still working, SubagentStop(qa)→idle; SubagentStop with empty agent_type ignored; StopFailure with agent_type drains that agent only; StopFailure without agent_type ends turn; PermissionRequest→blocked (turnActive kept); Notification with `notify_kind=idle` after Stop → stays idle; Notification without mark → blocked; process_exit → idle with `Aborted`; SessionEnd clears ledger + overflow; 65 distinct SubagentStarts → overflow sticky until SessionEnd; `coalesced` count honoured.
- [ ] **Step 2: Run** `./scripts/dev.sh test ./internal/hookevents` → FAIL (undefined).
- [ ] **Step 3: Implement** by porting `internal/tui/workstate.go:325-494`'s switch verbatim minus TUI fields (spinner, unseen). Derivation: `working = turnActive || len(subagents) > 0 || overflow`; `blocked = !blockedSince.IsZero()`; `State()` = blocked ? "blocked" : working ? "working" : seen ? "idle" : "".
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(hookevents): add pure per-pane work ledger`.

### Task 2: Daemon work state, `agent_state` on the wire, `agent_idle` event

**Files:**
- Modify: `internal/daemon/session.go` (Pane gets `Work hookevents.WorkLedger` + `workMu sync.Mutex`)
- Modify: `internal/daemon/daemon.go` — `emitEvent` (feed ledger BEFORE the mute/work-state-only branches), `buildPaneInfos`, `buildPaneStatus`
- Create: `internal/daemon/workstate.go` — `applyWorkEvent(pane, e)`, settle timer, `agent_idle` emission
- Modify: `internal/ipc/protocol.go` — `PaneInfo{AgentState, BlockedReason, LastIdleAt int64}`, `PaneStatusRespPayload` same, all omitempty
- Test: `internal/daemon/workstate_test.go`

**Interfaces:**
- Produces: `func (d *Daemon) paneWorkState(pane *Pane) (state, reason string, lastIdle int64)`; `func (d *Daemon) onPaneIdle(paneID string, fn func())` hooks used by Task 5 (a small subscriber list per pane, called after the settle window).
- Constant `agentIdleSettle = 2 * time.Second` (package var for tests).

- [ ] **Step 1: Tests** — through `d.emitEvent`: UserPromptSubmit then Stop on a claude pane → after settle, `d.events.Events()` holds Type `agent_idle`; a Start edge inside the settle window cancels it; `buildPaneInfos` reports `agent_state`; muted pane still updates ledger; terminal pane reports `""`.
- [ ] **Step 2: Run** `./scripts/dev.sh test ./internal/daemon` → FAIL.
- [ ] **Step 3: Implement.** In `emitEvent`, first line after the PaneID guard: `d.applyWorkEvent(pane, e)`. `applyWorkEvent` locks `pane.workMu`, calls `Apply`, and on `FellIdle && !Aborted` arms `time.AfterFunc(agentIdleSettle, …)` stored on the pane (`idleTimer *time.Timer`); a later transition to working stops the timer. The timer body re-checks `State()=="idle"`, pushes `PaneEvent{Type:"agent_idle", Title:"Turn finished", Severity:"info", Data{"hook_source":…}}` via `d.emitEvent` (it classifies as `WorkEventNone`, so no recursion) and runs the idle subscribers.
- [ ] **Step 4: Run** → PASS. Also `./scripts/dev.sh test ./internal/ipc`.
- [ ] **Step 5: Commit** `feat(daemon): track agent work state and emit agent_idle`.

### Task 3: Projects and tabs answer requests

**Files:**
- Modify: `internal/ipc/protocol.go` — add:
  ```go
  MsgListProjectsReq="list_projects_req"; MsgListProjectsResp="list_projects_resp"
  MsgCreateProjectReq="create_project_req"; MsgCreateProjectResp="create_project_resp"
  MsgProjectOpResp="project_op_resp"; MsgTabOpResp="tab_op_resp"; MsgPaneOpResp="pane_op_resp"
  type ProjectInfo struct{ID,Name,RootDir string; Active,Bootstrap bool; TabIDs []string; ActiveTab string}
  type ListProjectsRespPayload struct{Projects []ProjectInfo; ActiveProject string}
  type CreateProjectReqPayload = CreateProjectPayload; type CreateProjectRespPayload struct{ProjectID,Name,Error string}
  type OpRespPayload struct{ID string; OK bool; Error string}
  ```
  `TabInfo.ProjectID`, `PaneInfo.ProjectID` (omitempty). `ListTabsReqPayload{ProjectID string}` (new, optional payload).
- Modify: `internal/daemon/daemon.go` dispatch arms for `MsgUpdateProject/DestroyProject/MergeProjects/SwitchProject/ReorderProject`, `MsgUpdateTab`, `MsgDestroyTab`, `MsgUpdatePane`: when `msg.ID != ""`, `respondTo(conn, msg.ID, Msg…OpResp, OpRespPayload{…})`. `handleUpdateTab`/`handleDestroyTab`/`handleUpdatePane` currently return nothing; make them return `(ok bool, err string)`.
- Create: `internal/daemon/project_req.go` — `handleListProjectsReq`, `handleCreateProjectReq`.
- Test: `internal/daemon/project_req_test.go`

- [ ] **Step 1: Tests** — list returns the bootstrap project with its tab ids and `active`; create returns an id and the project appears; update with unknown id answers `OK=false` + Error; update WITHOUT an ID answers nothing (count conn sends via socket test `overlayServerDaemonWithConfig`); `list_tabs_req` with `project_id` filters; `TabInfo.ProjectID` populated.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(daemon): request-response project and tab operations`.

### Task 4: Rich pane creation and `create_tab_req`

**Files:**
- Modify: `internal/ipc/protocol.go`:
  ```go
  type CreatePaneReqPayload struct{ TabID, CWD, Type, InstanceName string; InstanceArgs []string // existing
      Name string `json:"name,omitempty"`; Toggles []string `json:"toggles,omitempty"`
      ResumeSessionID string `json:"resume_session_id,omitempty"`; WorktreeBranch string `json:"worktree_branch,omitempty"`
      Sandbox *SandboxSpec `json:"sandbox,omitempty"` }
  type CreateTabPayload struct{ Name string; ProjectID string `json:"project_id,omitempty"`; FirstPane *FirstPaneSpec }
  MsgCreateTabReq="create_tab_req"; MsgCreateTabResp="create_tab_resp"
  type CreateTabReqPayload struct{Name, ProjectID string; FirstPane *CreatePaneReqPayload}
  type CreateTabRespPayload struct{TabID, PaneID, PreparingWorktree, Error string}
  MsgPluginCatalogReq/Resp; type PluginToggleInfo{Name,Label,Group string; Default bool}
  type PluginCatalogEntry{Name,DisplayName,Category string; Available,PromptsCWD,Sessions,Sandboxable bool; Toggles []PluginToggleInfo}
  type PluginCatalogRespPayload{Plugins []PluginCatalogEntry}
  ```
- Create: `internal/daemon/create_req.go`:
  ```go
  func (d *Daemon) buildCreatePayload(req ipc.CreatePaneReqPayload, tabID string) (ipc.CreatePanePayload, error)
  func resolveToggles(p *plugin.PanePlugin, names []string) ([]string, error) // unknown → error; same Group twice → error
  func (d *Daemon) resolveWorktreeRoot(cwd string) (string, error) // worktreeListFn(ctx,cwd) → WorktreeRoot/Root; empty → "not a git repository"
  func (d *Daemon) createPaneFromReq(req ipc.CreatePaneReqPayload) ipc.CreatePaneRespPayload // sync path answers PaneID/TabID/Error (SpawnError); worktree path calls worktreeAddAndCreate
  func (d *Daemon) handleCreateTabReq(conn, msg)   // CreateTabInProject; first pane via createPaneFromReq; worktree → placeholder + createFirstPaneWorktree
  func (d *Daemon) handlePluginCatalogReq(conn, msg)
  ```
- Modify: `internal/daemon/daemon.go` — `handleCreatePaneReq` becomes `respondTo(conn, msg.ID, MsgCreatePaneResp, d.createPaneFromReq(req))` (worktree path in a goroutine, like `handleCreatePane`); `handleCreateTab` uses `payload.ProjectID` via `CreateTabInProject`; dispatch new messages.
- Test: `internal/daemon/create_req_test.go`

- [ ] **Step 1: Tests** — toggles resolve `dangerously_skip_permissions` → `--dangerously-skip-permissions`; unknown toggle → Error, no pane created; two `permission_mode` toggles → Error; `Name` lands on `pane.Name`; `worktree_branch` in a non-repo → Error and no pane; `create_tab_req` with `project_id` files the tab under it and answers both ids; `create_tab` (legacy) with `project_id` honours it; catalog lists claude-code toggles.
- [ ] **Step 2–4:** FAIL → implement → PASS (`./internal/daemon`, `./internal/ipc`).
- [ ] **Step 5: Commit** `feat(daemon): create panes and tabs with dialog options over MCP`.

### Task 5: Task registry and delegation

**Files:**
- Modify: `internal/ipc/protocol.go`:
  ```go
  MsgDelegateTaskReq/Resp, MsgGetTaskReq/Resp, MsgWaitTaskReq/Resp, MsgListTasksReq/Resp
  type DelegateTaskReqPayload struct{ToPane, Prompt, FromPane string; Notify bool; TimeoutMs int}
  type TaskInfo struct{ID, FromPane, ToPane, ToPaneName, State, Prompt, Result, Error string; CreatedAt, StartedAt, EndedAt int64}
  type DelegateTaskRespPayload struct{Task TaskInfo; Error string}
  type GetTaskReqPayload{TaskID}; GetTaskRespPayload{Task TaskInfo; Error}
  type WaitTaskReqPayload{TaskID; TimeoutMs}; WaitTaskRespPayload{Task TaskInfo; Timeout bool; Error}
  type ListTasksReqPayload{PaneID}; ListTasksRespPayload{Tasks []TaskInfo}
  ```
- Create: `internal/daemon/task.go`:
  ```go
  type taskState string // sent, working, done, failed, timeout
  type task struct{ id, from, to string; prompt string; notify bool; created, started, ended time.Time; state taskState; result string; done chan struct{}; timer *time.Timer; pendingNotify bool }
  type taskRegistry struct{ mu sync.Mutex; byID map[string]*task; order []string; max int }
  func (d *Daemon) delegateTask(req ipc.DelegateTaskReqPayload) ipc.DelegateTaskRespPayload
  func (d *Daemon) deliverPrompt(pane *Pane, text string) bool // AI: "\x1b[200~"+text+"\x1b[201~" then after 100ms "\r"; terminal: text+"\n"; both via pane.EnqueueInput
  func (d *Daemon) taskObserve(paneID string, e PaneEvent, tr hookevents.Transition) // called from applyWorkEvent + command_complete/process_exit
  func (d *Daemon) finishTask(t *task, st taskState) // capture result (last 30 stripped lines), emit task_done event on To, close done, notify-back
  func (d *Daemon) notifyRequester(t *task) // if From is a live AI pane: deliver when its State()!="working", else pendingNotify and deliver from the From pane's idle subscriber
  ```
- Modify: `internal/daemon/daemon.go` — dispatch 4 new messages; call `taskObserve` from Task 2's `applyWorkEvent` and from the `command_complete` emitter (`daemon.go:3868`).
- Test: `internal/daemon/task_test.go`

- [ ] **Step 1: Tests** (with `agentIdleSettle` shortened to 10 ms): delegate to a live claude pane writes a bracketed-paste then `\r` to the fake session; state `sent`→`working` on UserPromptSubmit→`done` on Stop after settle, `task_done` event queued with `task_id`; Stop then SubagentStop keeps `working` until the agent drains; process_exit → `failed`; timeout → `timeout`; notify-back types `[quil task …]` into the FROM pane when it is idle and DEFERS while it is working (delivered on its Stop); delegate to a placeholder pane → Error; `wait_task` returns on completion; registry evicts oldest done past `max`.
- [ ] **Step 2–4:** FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(daemon): delegate tasks between panes with completion events`.

### Task 6: Bridge host router

**Files:**
- Create: `cmd/quil/mcp_hosts.go`:
  ```go
  type hostConn struct{ dest string; label string; bridge *mcpBridge; version string; err error; lastTry time.Time }
  type mcpRouter struct{ mu sync.Mutex; local *mcpBridge; hosts map[string]*hostConn; order []string; idHost map[string]string; selfPane string; dialFn func(config.Destination) (*ipc.Client, string, error) }
  func newMCPRouter(local *mcpBridge, cfg config.Config, dial …) *mcpRouter
  func (r *mcpRouter) connectAll() // background, best effort
  func (r *mcpRouter) bridgeFor(host, id string) (*mcpBridge, string, error) // resolution order per spec; redial with 30 s backoff
  func (r *mcpRouter) remember(host string, ids ...string)
  func (r *mcpRouter) connected() []*hostConn
  ```
  `dialFn` default: `dialRemoteTransportFn(ctx, cfg, dest, true, sink)` + `gateExtraVersion` + `sendClientHello(client, helloRoleBridge)`; returns the daemon version from `versionHandshakeWithin`.
- Modify: `cmd/quil/mcp.go` — build the router; `registerMCPTools(s, router, mcpLog)`; `mcpBridge` unchanged.
- Test: `cmd/quil/mcp_hosts_test.go` — resolution order (explicit host wins; cached id; local fallback); unknown host error; disconnected host error includes dial error; backoff prevents a second dial inside 30 s (fake dialFn counts calls).

- [ ] **Step 1–4:** tests → FAIL → implement → PASS (`./cmd/quil`).
- [ ] **Step 5: Commit** `feat(mcp): route tools to configured remote hosts`.

### Task 7: Bridge tools

**Files:**
- Modify: `cmd/quil/mcp_tools.go` — every `Input` gains `Host string \`json:"host,omitempty"\``; calls go through `router.bridgeFor`; `list_panes`/`list_tabs` aggregate and stamp `host`, `self`; `send_to_pane` gains `Paste bool`; `create_pane` gains the Task 4 inputs; `watch_notifications` fans out; `get_notifications` aggregates; `dismiss_notifications` takes `host`.
- Create: `cmd/quil/mcp_tools_projects.go` — `list_hosts`, `list_projects`, `create_project`, `update_project`, `destroy_project`, `switch_project`, `create_tab`, `rename_tab`, `destroy_tab`, `rename_pane`, `list_plugins`, `list_sessions`.
- Create: `cmd/quil/mcp_tools_tasks.go` — `delegate_task`, `get_task`, `wait_task`, `list_tasks`.
- Modify: `cmd/quil/mcp.go` — server `Instructions` text: mention `list_plugins` before `create_pane`, `delegate_task` + `wait_task` for pane-to-pane work, `host`.
- Test: `cmd/quil/mcp_tools_test.go` additions — a fake daemon conn (existing pattern in `mcp_paneinput_test.go`) answering `delegate_task_req`; `from_pane` comes from `QUIL_PANE_ID` (`t.Setenv`); `send_to_pane` with `paste` wraps bytes; list aggregation stamps `host`.

- [ ] **Step 1–4:** tests → FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(mcp): project, tab, host and task tools`.

### Task 8: Docs, changelog, full verification

**Files:**
- Modify: `docs/mcp.md` (tool tables, count, new "Projects and hosts" + "Delegating work" sections), `.claude/CLAUDE.md` (MCP section: tool count + new files), `docs/features.md` if it lists the count.
- Create: `changelog.d/feat-mcp-projects-hosts-tasks.md` with a `headline:` line.

- [ ] Run `./scripts/dev.sh test` (all), `./scripts/dev.sh vet`, `./scripts/dev.sh build`.
- [ ] Commit `docs(mcp): document project, host and task tools`.
