---
headline: Run an AI pane inside a Docker container
---
- **AI panes can run inside a Docker container.** The pane setup dialog gains a
  *Run in a Docker container* row when the daemon's machine has Docker with linux
  containers. The pane's checkout is bind-mounted into the container, so the agent
  edits your real files and its commits land in your real repository — but its
  filesystem reach stops at that checkout. The container image is yours to choose;
  Quil ships none and publishes none.
- **The repository's history is out of reach.** New git objects are written to a
  store belonging to the pane, with your repository's own object store mounted
  read-only, so nothing inside the container can delete the history of any branch.
  Quil copies the new objects across every 30 seconds and again when the pane
  closes. Only the repository's `.git` enters the container — never your main
  checkout's working tree.
- **Notifications, the working indicator, input history and session resume all
  keep working**, through a Linux hook binary Quil fetches once per release and
  mounts into the container. Each pane gets its own hook spool and its own Claude
  config directory, so one sandboxed pane cannot read or write another's.
- **Authentication follows Anthropic's dev-container guidance.** Sign in inside the
  container once per pane, or set `[sandbox] auth = "token"` to forward
  `CLAUDE_CODE_OAUTH_TOKEN` from the daemon's own environment — Quil never reads,
  copies, stores or refreshes a credential, and the token never reaches a command
  line or a log.
- Closing the pane removes its container. See `docs/sandbox-panes.md` for the
  Dockerfile to start from, the bind-mount performance cost, and why file watchers
  need polling mode inside a container.
