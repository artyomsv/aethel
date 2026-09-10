---
headline: MCP gains projects, remote hosts and pane-to-pane tasks
---
- **`create_pane` accepts the dialog's options over MCP.** A pane name, plugin toggles by
  NAME (permission mode, `--chrome`, `--search` — resolved by the daemon, refused when
  unknown or contradictory), a Claude session to resume, a new git worktree branch, and a
  Docker sandbox image with its sign-in mode. `list_plugins` and `list_sessions` tell an
  agent what to pass. A spawn failure now reaches the response as `error`.
- **Projects and tabs are manageable over MCP.** `list_projects`, `create_project`,
  `update_project`, `switch_project`, `destroy_project`, `create_tab` (any first pane, in
  any project, without stealing focus), `rename_tab`, `destroy_tab`, `rename_pane`. Every
  mutation answers whether it applied; tabs and panes report their `project_id`.
- **Remote hosts.** The bridge dials every `[[destinations]]` host and every tool takes
  an optional `host`; ids discovered through the list tools route to their host on their
  own. `list_hosts` shows connection state. The list and notification tools aggregate
  across hosts.
- **`delegate_task`: hand another pane a job and hear when it is done.** The prompt is
  pasted as one block; the daemon follows the target's own agent state — a new
  `agent_state` (`working` / `blocked` / `idle`) on every AI pane — and marks the task
  `done` when the turn has settled idle, background subagents included, rather than on
  the raw `Stop`. A `task_done` event is queued, `wait_task` blocks on it, and with
  `notify` (default) a one-line `[quil task …] done` notice is typed into the requester's
  own prompt once it is idle, so an orchestrator can carry on and react as results land.
  `send_to_pane` gains `paste` for multi-line prompts; a queued `agent_idle` event marks
  every settled turn.
