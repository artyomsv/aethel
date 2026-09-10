# Agent flows

Open the command palette and choose **New flow**. Enter the feature request,
press Tab to edit the suggested `feat/<slug>` branch, and press Ctrl+S to start.
Quil creates a worktree tab in the active project with analyst, developer, and
reviewer panes. The new tab becomes active in the client that requested it.
Other clients keep their focus. The destination is fixed when the dialog opens.

The analyst plans the whole epic and files its tickets; the developer implements
it on one branch and opens one PR; the reviewer reviews that PR. Changes go back
to the developer, then back to the reviewer. Approval marks the tab **✓ PR N**.
Quil itself never calls GitHub: the agents use `gh` from their panes.

The sidebar shows the current stage, review round, and a pause mark when help is
needed. An agent's blocked question, an unreported idle turn, a missing required
result, process failure, step timeout, or exhausted review limit pauses the flow.
Choose **Resume flow** to send the current step again. Nothing retries by itself.
Resume preserves the review-round count. **Cancel flow** uses the existing tab
close confirmation, including the worktree-removal choices.

Flows survive daemon restarts. A step that was running is paused with
`daemon restarted`; resuming is explicit because repeating an agent's work could
post a second PR or comment. A half-created flow is dropped on restart, leaving
its tab under the existing interrupted-worktree recovery rules. Closing a role
pane cannot be repaired by the flow; close its tab and start another flow.

## Configuration

**F1 → Settings → Flows** edits the selected daemon's `$QUIL_HOME/flows.toml`.
Choose each role's available AI plugin, named toggles, task prompt, developer fix
prompt, maximum review rounds, and step timeout in minutes. Arrow keys select
rows; Enter edits a prompt or cycles an option; Ctrl+S saves a prompt back to the
settings page, and Ctrl+S there saves the file atomically and reloads the daemon.
Unknown `{{placeholders}}` are retained and flagged in the prompt editor.
Prompts cannot be empty. Changing an agent seeds its plugin's default-on toggles;
select a permission-mode toggle before saving if the plugin offers that group.

A missing file uses the embedded defaults: Claude Code for analyst/reviewer
with `dangerously_skip_permissions`, Codex for developer with
`auto_workspace_write`, three change rounds, and no step timeout. Role panes are
plain AI panes; sandbox panes and flows spanning multiple hosts are not supported.
Unavailable agents and unknown/conflicting toggle names refuse creation before a
tab or worktree is made. Supported per-spawn MCP adapters are Claude Code, Codex,
and OpenCode (the shipped V1 configuration schema).

Complete each agent's login and first-use workspace trust prompts in its pane.
The permission toggles do not dismiss these setup screens. If a step times out
during setup, finish setup and choose **Resume flow**. A nonzero step timeout
is useful while setting up agents; the default of zero allows unlimited time.

Prompts support `{{feature}}`, `{{plan}}`, `{{pr}}`, and `{{review}}`. Substitution
is one pass so placeholders inside reported text remain literal. Every prompt
receives an immutable tail telling the agent to report through `report_step`,
stay within its role, and avoid doing another role's work through subagents.

## Step reporting

Role panes receive a restricted `quil mcp --toolset flow` server exposing only
`report_step` and `get_task` for their own local task. It does not connect to
remote hosts or expose workspace control tools. No global agent configuration
is changed. The bridge is the `quil` executable beside the running `quild`, with
the same dev/debug suffix. It uses that pane's `QUIL_HOME` and `QUIL_PANE_ID`.
Codex receives these names in the server's `env_vars` allowlist because it
filters the environment inherited by MCP subprocesses.

Call `report_step` with `status: "done"` and string-valued `result` entries:

| Step | Required result |
| --- | --- |
| plan | `plan` |
| build | `pr`: number, `owner/repo#N`, or GitHub PR URL |
| review | `verdict`: `approved` or `changes`; optional `notes` |
| fix | none (`{}`) |

For help, report `status: "blocked"` with `result: {"question": "…"}`. Each value
is limited to 8 KiB and there may be at most 16 entries. An optional `task_id`
must target the caller's own pane; otherwise the daemon resolves that pane's
one live **flow** task; ordinary delegated tasks cannot be completed by reporting.
Feature text, prompts, and report values reject ESC, CSI, and CR controls.
A report may be corrected before the task ends. Reporting on an
ended task is refused.

With hooks, completion requires both a report and settled idle. Without any
ledger edge, a report starts a two-second fallback; hooks appearing during the
window restore the strict idle rule. Reported text is stored raw and sanitized
when displayed. `report_step` requires daemon version 1.73.0 or newer and always
uses the local daemon of its bridge, never a remote route. Existing project,
tab, and task tools retain their separate 1.72.0 daemon floor.

Workspace broadcasts carry display state, PR, and pause reason. Full feature,
plan, and review notes are retained in the persisted snapshot, not broadcast.

## Adapter references

Verified on 2026-09-10: installed Claude Code 2.1.267 accepts `--mcp-config`
with JSON; installed Codex 0.154.0 accepts dotted `-c` TOML overrides.
The adapter formats follow the [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference),
[Codex MCP configuration](https://developers.openai.com/codex/mcp), and
[OpenCode MCP configuration](https://opencode.ai/docs/mcp-servers/).

## Validation

The pure transition tests cover each stage, pause guard, explicit resume, and
round counting. IPC integration tests drive creation, reporting, idle edges,
ownership refusal, worktree failure, restart snapshots, settings writes, and
tab removal. TUI tests enter the dialogs through `Update`, verify sidebar and
palette state, and check save/reload and desktop attention wiring.

A Windows dev-mode smoke run on 2026-09-10 completed plan → build → review →
ready with three real Claude Code panes and a disposable local repository.
The developer created `hello.txt`, the reviewer verified it, and both used
`report_step`. This used a synthetic PR number and did not exercise GitHub.
Initial startup required submitting the analyst's queued prompt after setup.
The default Claude/Codex/Claude run verified the analyst handoff and timeout
pause, but the installed Codex 0.154.0 stayed on its startup animation, so its
full live completion remains unverified. Adapter tests cover its MCP arguments
and environment forwarding.
