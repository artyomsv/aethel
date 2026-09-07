# Sandbox panes

Run an AI pane inside a local Docker container, with its git checkout
bind-mounted in. The agent edits your real files and its commits land in your
real repository, but its filesystem reach stops at that checkout.

Quil publishes no container image. You supply one.

## Requirements

- Docker running **linux** containers on the machine the daemon runs on — not
  necessarily the machine you are typing on. Docker Desktop in Windows-containers
  mode answers a probe and then fails every image, so Quil treats it as
  unavailable.
- On Windows, the drive holding your repository must be shared in Docker
  Desktop's file-sharing settings.

## Using it

Open the pane dialog (`Ctrl+N`), pick an AI plugin, choose a directory, and turn
on **Run in a Docker container**. Type the image name. Press Continue.

Closing the pane removes its container.

## The image

Start from Anthropic's own dev-container feature, which installs Claude Code
into any base image:

```dockerfile
FROM mcr.microsoft.com/devcontainers/base:ubuntu

# The devcontainer feature is the supported install path. If you are building a
# plain image instead, install Node and then:
#   npm install -g @anthropic-ai/claude-code
RUN useradd -m agent
USER agent
```

Two things the image must provide:

- **A non-root user.** Claude Code refuses `--dangerously-skip-permissions` as
  root, and the whole point of a sandbox is to use it.
- **The agent binary on `PATH`** — `claude`, `opencode` or `codex`, matching the
  plugin you picked. A missing binary shows as
  `exec: "claude": executable file not found in $PATH` and the pane exits.

## Signing in

Two options, both from Anthropic's dev-container documentation. Quil never
reads, copies, stores or refreshes a credential in either.

**Sign in inside the container** (default). Run `claude`, follow the browser
prompt. If the browser callback cannot reach the container, copy the code shown
in the browser and paste it at the `Paste code here if prompted` prompt. Each
pane has its own config directory, so this is once per pane.

**Forward a token** (recommended for more than one pane). Run
`claude setup-token` once, export the result in the environment the *daemon*
runs in, and set:

```toml
[sandbox]
auth = "token"
```

Quil passes the variable name to Docker and lets Docker read the value from its
own environment, so the token never appears in a command line or in
`quild.log`. This costs the pane Remote Control and claude.ai connectors.

`shared_claude_config = true` gives every sandbox pane one config directory, so
you sign in once. It also merges them into **one trust domain**: any sandbox
pane can then write a hook or an MCP server that every other sandbox pane's
claude runs. Off by default.

## What the sandbox does and does not bound

**Bounded.** The agent's process reach is the container. Its filesystem reach is
the checkout (read-write), the repository's `.git` (read-only, with four narrow
exceptions git needs to commit), and its own directory. Your main checkout's
working tree never enters the container. No other pane's data is reachable. Your
repository's git objects — every commit on every branch — cannot be deleted from
inside.

**Not bounded.** Branch pointers: a sandbox pane can move or delete any branch
including `main`. That is recoverable — the objects survive, and `git reflog`
finds them — and making refs read-only would make committing impossible. The
Claude credential in the pane's own config directory, per Anthropic's own
warning about dev containers. Network access, which is the image's business.

## Things to know

- **Do not run `git gc`, `git repack` or `git commit-graph write`** inside a
  sandbox pane. New objects live in a per-pane store that Quil copies into your
  repository; repacking rearranges it underneath that.
- **File watchers need polling mode.** inotify events do not cross a Docker
  Desktop bind mount on Windows — measured. `tsc --watch`, `jest --watch`,
  `vite` and `nodemon` will sit silent and the agent will conclude its edits had
  no effect. Set `CHOKIDAR_USEPOLLING=1`, `--watch-poll` or the equivalent.
- **Bind-mount IO is roughly 20× slower.** Counting 1102 files: 0.14 s on the
  host, 4.5 s in the container. On a 100k-file monorepo, `git status`, ripgrep
  and a test run become minutes. A repository living inside WSL2 does not pay
  this cost.
- **Repositories with submodules are not supported.** A submodule's `.git` in a
  linked worktree is a relative path that escapes the worktree, and the mount
  layout does not preserve that offset.
- **Usage limits.** Anthropic's documentation notes that advertised Pro and Max
  limits "assume ordinary, individual usage of Claude Code". A multiplexer makes
  many concurrent agents easy.

## If a repository starts erroring

Every git command in a repository failing with:

```
error: object directory …/sandbox/panes/<id>/objects does not exist;
       check .git/objects/info/alternates
```

means a sandbox pane's object store was removed while its reference remained.
Quil repairs this at daemon start — unless `$QUIL_HOME` itself was wiped
(`reset-daemon`), which also removes the record of where to look.

The manual fix is one line: open `<repo>/.git/objects/info/alternates` and
delete the line naming the missing directory. If it is the only line, delete the
file.
