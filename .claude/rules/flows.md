---
description: Agent flow state, role spawning, reporting, configuration, and settings invariants
paths:
  - "internal/flow/**"
  - "internal/daemon/flow*.go"
  - "internal/daemon/task.go"
  - "internal/config/flows*"
  - "internal/ipc/flow.go"
  - "internal/tui/flow*.go"
  - "cmd/quil/mcp*"
---

## Agent flows

`internal/flow` is the stdlib-only built-in epic state machine. `internal/daemon/flow.go`
owns the registry under `SessionManager.mu`, persists `flows` beside tabs, and
uses the existing task delivery and idle ledger. `finishTask` forwards its locked
report snapshot after releasing the task registry mutex. Flow dispatch reserves
the task ID before delivery, so immediate completion can find the flow. Restore
pauses active stages and drops preparing/orphan flows. `Pane.FlowRole` is persisted
and opts only role panes into per-spawn MCP registration. `config.FlowsPath()` is
`$QUIL_HOME/flows.toml`; the daemon loads it at start/reload and the TUI settings
editor reads/writes it over destination-pinned IPC. See `docs/agent-flows.md`.

Role panes register `quil mcp --toolset flow`: only `report_step` and a local, caller-scoped `get_task`. Never expose the unrestricted workspace tool set through the role spawn adapters. `report_step` has its own 1.73.0 floor; existing project/tab/task tools retain 1.72.0.

Claude uses `--strict-mcp-config`. Codex probes effective server names with bounded `mcp list --json`, disables inherited servers, and registers a collision-free bridge name; whole-table overrides alone still merge. OpenCode can retain other configured servers. These adapters are role guardrails, not a security boundary against an agent with shell access.

Only tasks marked as daemon-owned flow steps accept reports. Fallback completion resolves the pane before taking the task registry lock and checks UNKNOWN under `workMu` atomically with completion. No `sm.mu` acquisition is allowed while that work lock is held.

`StartFlowReqPayload.CWD` names the repository; `startFlow` REFUSES an unusable directory rather than falling back to the project root the way `resolveRequestedCWD` does for an ordinary pane. `FlowRole.Model` is read at SPAWN from the daemon's flow config (`flowModelArgs`, inserted before a codex `--`), so an F1 edit applies on the pane's next restart; the charset is validated, the id is not. It is applied ONLY when the configured role's agent matches the pane's own `Type` — a role's agent can change while its panes live on, and those keep the type they were spawned with. The settings dialog clears the model with the toggles on an agent change for the same reason. Every flow-dialog text key and paste goes through `flowTextTarget`/`flowInsertText`/`flowPaste`: one focus answer for typing and pasting, or a paste that matches no field falls through to the pane behind the dialog. Use `msg.Text`, never `msg.String()`, for character input — the latter spells a space "space". `requestGitRepos` takes the caller's destination and stamps it, because `Router.Send` resolves an unstamped message against whatever destination is active when the send goroutine runs. The TUI places role panes with `splitForNewPane` (analyst | developer / reviewer) keyed on `flow.Panes`, at both insertion sites in `model.go`; ordinary panes keep stacking.

Reject terminal controls in feature, prompt, and result text; keep newlines as text. PR results must be a number, `owner/repo#N`, or a GitHub PR URL. Configuration requires nonempty prompts and an explicit permission-mode toggle when the plugin exposes that group.
