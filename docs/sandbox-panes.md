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

Build one:

```bash
scripts/sandbox-image.sh
```

That produces `quil-sandbox:latest` on your machine from
`docker/sandbox/Dockerfile`, then verifies it before telling you it worked.
Point the dialog at it once:

```toml
[sandbox]
  default_image = "quil-sandbox:latest"
```

Useful flags: `--tag`, `--base`, `--claude-version` (pin an exact version for a
reproducible image), and `--check <tag>` to test an image you built yourself.

### Why you build it rather than pull it

There is no image to pull. Quil publishes none — shipping Claude Code
preinstalled inside a vendor image is "preinstalling or running Claude Code in
your products or services" under Anthropic's Commercial Terms, while an image
you build yourself means Quil preinstalls nothing. Anthropic publishes none
either: their guidance is a dev-container *recipe* you copy into your own
repository.

**Do not trust an official-looking registry name.** `anthropics/claude-code` on
Docker Hub is not Anthropic's. It is a security researcher's honeypot,
published to study exactly this assumption; it runs as root and contains no
Claude Code at all. Docker Hub namespaces are first-come and unrelated to the
GitHub organisation of the same name.

### What any image must provide

If you bring your own, `scripts/sandbox-image.sh --check <tag>` asserts all
three:

- **A non-root user.** Claude Code refuses `--dangerously-skip-permissions` as
  root, and the whole point of a sandbox is to use it.
- **The agent binary on `PATH`** — `claude`, `opencode` or `codex`, matching the
  plugin you picked. A missing binary shows as
  `exec: "claude": executable file not found in $PATH` and the pane exits.
- **`git`.** The pane's checkout is a linked git worktree and its object store
  is wired up with git commands inside the container.

### Network

Quil passes no `--network` and no `--cap-add`, so a sandbox pane reaches
whatever the host can. The mount set is the boundary this feature enforces;
egress is not. If you need a default-deny firewall, start from Anthropic's
example dev container, which ships one — it needs `NET_ADMIN`, which Quil does
not grant, so that firewall has to be applied by the image's own runtime rather
than by Quil.

## Signing in

Two options, both from Anthropic's dev-container documentation. Quil never
reads, copies, stores or refreshes a credential in either. Copying
`~/.claude/.credentials.json` is deliberately not implemented — it reads as
"collect, store, or intermediate … session tokens" under Anthropic's
authentication policy, and it cannot work on a macOS host at all (Keychain,
not a file).

**Forward a token** — the default, and the one to set up. Run it once:

```bash
quil sandbox login
```

That drives Anthropic's own `claude setup-token`, takes the token it
prints, saves it to your user environment, and restarts the daemon so it picks
it up. Every sandbox pane is then signed in.

`quil sandbox status` says where you stand:

```
auth mode : token   (default)
token in this process : yes
token persisted       : yes
```

You do not copy the token anywhere. Quil runs `claude setup-token` under a
pseudo-terminal, mirrors it to your terminal so the browser step is still
visible and interactive, and reads the token out of the output. A plain pipe
cannot do this — measured: with its output redirected the command prints
nothing at all, opens the browser, and waits.

On Windows the token goes to `HKCU\Environment`, the same place the System
Properties dialog writes. Elsewhere Quil has nowhere it can reliably put it — a
daemon started by launchd or systemd reads no shell profile — so it prints the
`export` line and lets you place it. Doing the setup by hand is always an
option: run `claude setup-token`, export the result as
`CLAUDE_CODE_OAUTH_TOKEN` where the *daemon* runs, and restart it.

Quil passes the variable *name* to Docker and lets Docker read the value from
its own environment, so the token never appears in a command line or in
`quild.log`. This costs the pane Remote Control and claude.ai connectors.

If the token is not set, the pane falls through to the sign-in below and
`quild.log` says so, naming this command — a pane that silently asks you to log
in when you thought a token was configured is the confusing case, so it is
never silent.

**Sign in inside the container** — the fallback. Run `claude`, follow the
browser prompt. If the browser callback cannot reach the container, copy the
code shown in the browser and paste it at the `Paste code here if prompted`
prompt. Each pane has its own config directory, so this is **once per pane**.

Choose it deliberately with:

```toml
[sandbox]
auth = "browser"
```

`auth` accepts `"token"` (or, for every config written before `"browser"`
existed, the empty string) and `"browser"`. Anything else is treated as
`"browser"` and reported in `quild.log`.

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
