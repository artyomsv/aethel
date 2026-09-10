---
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

Only tasks marked as daemon-owned flow steps accept reports. Fallback completion resolves the pane before taking the task registry lock and checks UNKNOWN under `workMu` atomically with completion. No `sm.mu` acquisition is allowed while that work lock is held.

Reject terminal controls in feature, prompt, and result text; keep newlines as text. PR results must be a number, `owner/repo#N`, or a GitHub PR URL. Configuration requires nonempty prompts and an explicit permission-mode toggle when the plugin exposes that group.
