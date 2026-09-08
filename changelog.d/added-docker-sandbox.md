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
- **Authentication follows Anthropic's dev-container guidance**, and each pane
  picks its own in the create dialog. The default forwards
  `CLAUDE_CODE_OAUTH_TOKEN` from the daemon's own environment; the alternative
  signs in inside the container, once per pane, and is what a pane needs for
  Remote Control, claude.ai connectors and Fable. Quil never reads, copies,
  stores or refreshes a Claude credential in either mode, and the token never
  reaches a command line or a log.
- **`scripts/sandbox-image.sh` builds the image**, locally, from
  `docker/sandbox/Dockerfile`, and then verifies it actually satisfies what a pane
  needs — a non-root user, a working `claude`, and `git` — rather than reporting
  success from a clean build log. `--check <tag>` runs the same assertions against
  an image you built yourself. Quil still publishes no image and pulls none: there
  is no official Claude Code image to pull, and the official-looking name on Docker
  Hub is a security researcher's honeypot containing no Claude Code at all.
- Closing the pane removes its container. See `docs/sandbox-panes.md` for the
  image recipe, the bind-mount performance cost, why sandbox panes have no network
  egress restriction, and why file watchers need polling mode inside a container.
- **`quil sandbox login` does the token setup for you.** It runs Anthropic's own
  `claude setup-token` under a pseudo-terminal, reads the token out of its output,
  saves it to your user environment, and restarts the daemon — no token to copy,
  no environment variable to set, no config edit. `quil sandbox status` says
  whether it is in place. **Quil keeps no copy of the token**: it goes to the OS
  store (`HKCU\Environment` on Windows), so Quil is a UI over somewhere you could
  have typed it yourself rather than a credential store of its own. The token flow
  is the default; the per-pane sign-in inside the container is the fallback, and
  a daemon that cannot find a token now says so in `quild.log` instead of silently
  asking you to log in again.
