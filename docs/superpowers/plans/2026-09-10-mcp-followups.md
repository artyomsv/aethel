# MCP follow-ups after v1.72.0 — work order

**Status:** TODO. **Branch:** `fix/mcp-followups` (off `542a679`, `chore(release): v1.72.0`).
**Baseline:** PR #212 shipped 34 MCP tools, the host router and pane-to-pane tasks in v1.72.0.
A live test on 2026-09-10 (Windows laptop, five Linux remotes, all on 1.72.0) found three
follow-ups. They are independent; do them in order, each with its own commit.

Ground rules that apply to all three (see `.claude/rules/dev-environment.md`):

- Never touch the production daemon or `~/.quil/`. Build with `./scripts/dev.sh build`,
  run `quil-dev.exe` from this worktree (`.quil/` here), stop a dev daemon only by the PID in
  `./.quil/quild.pid`.
- CI-exact tests: `docker run --rm -v "$PWD":/src -w /src -v quil-gomod:/go/pkg/mod golang:1.25 go test -tags=integration -race -timeout 300s ./...`
  (`dev.sh test` takes ONE package and runs neither `-race` nor the integration tag).
- The remote test VM is `artyom@192.168.6.12` (project `cluster-management`,
  `proj-41897a27`). Never call `screenshot_pane` from the session's own MCP tools.
- Items 1 and 2 need a `changelog.d/fixed-*.md` fragment each (with a `headline:` line).
  Item 3 is docs-only and must NOT add a fragment.

---

## 1. The bridge never redials a host that came back

### Observed

At 17:28–17:29Z every remote daemon was restarted on 1.72.0. At 17:31Z, from a bridge that
had been running since before the upgrade:

- `list_hosts` reported all five hosts `connected: false` with
  `runs 1.71.0, this client runs 1.72.0; run quil remote setup …` — stale text from the
  dial that failed before the upgrade.
- Unscoped `list_projects` returned only the local projects.
- `list_projects` with `host` set, once per host, connected all five; after that
  `list_hosts` showed `connected: true`, `daemon_version: 1.72.0` everywhere.

### Cause

`cmd/quil/mcp_hosts.go`: the redial lives only in `bridgeFor(host)` (called for a NAMED
host or a cached id). `connected()` — what every unscoped aggregate and `targets("")` use —
and `statuses()` — what `list_hosts` uses — only READ `h.bridge`. `connectAll()` runs once
at startup. So a host that was down at startup, or dropped later and was never named, is
never retried, and its error text never refreshes.

### Fix

- In `connected()` (or one helper `connected` and `statuses` share): for a host with no live
  bridge, not `dialing`, and past `r.backoff` since `lastTry`, start `r.connect(h, false)`
  on a goroutine. NEVER wait for it — `TestMCPRouter_StatusReadsDoNotWaitForADial` pins
  that these reads return while a dial is in flight. The current call still skips the host;
  the next one includes it. `statuses()` reports `connecting` for it meanwhile (already
  does).
- Keep the 30 s backoff: a host that refuses must not be dialled on every `list_panes`.
- Confirm `connect` clears `h.err` / `h.reqErr` on success (it should; assert it in the test).

### Tests (`cmd/quil/mcp_hosts_test.go`)

- Dial fails once (the `TestMCPRouter_UnknownAndUnreachableHosts` shape), sleep past
  `r.backoff`, call `r.connected()` with NO named call in between: the dial counter goes
  1 → 2, and after the dial completes `connected()` lists the host and `statuses()` shows it
  connected with an empty `Error`.
- Inside the backoff `connected()` does not dial (counter stays 1).
- `statuses()` alone also triggers the retry (that is the `list_hosts` path).

### Acceptance

Restart a remote daemon while a bridge is up; within ~30 s an unscoped `list_projects`
includes it again and `list_hosts` shows the new version, without naming the host.

---

## 2. A Claude Code pane created over MCP on a remote host paints with artifacts

### Observed

From the laptop's bridge: `create_tab` (default terminal first pane) in `proj-41897a27`
on `artyom@192.168.6.12` → `tab-82cb379c`; then `create_pane type=claude-code
name=mcp-claude cwd=/home/artyom tab_id=tab-82cb379c` → `pane-74524201` (routed to the
host by the cached tab id, no `host` given); then `destroy_pane` of the terminal. The tab was
NOT visible in the TUI at creation. Daemon log (remote `~/.quil/quild.log`):

```
17:31:51.697Z pane created: pane-74524201 (type=claude-code, tab=tab-82cb379c, overlay=false)
17:31:51.697Z spawn: pane pane-74524201 cmd=/home/artyom/.local/bin/claude args=[--settings … --session-id 3ad6a1fd-…] cwd=/home/artyom restoring=false
17:32:47.584Z ipc recv: restart_pane_req
17:32:47.584Z spawn: pane pane-74524201 … args=[--settings … --resume 3ad6a1fd-…] cwd=/home/artyom restoring=false
```

When the user switched to the tab, Claude Code's screen was corrupted: the input box painted
three times down the screen with fragments of earlier frames between them (`he`, `rc`,
`effort`, `/rc`, a second `← for agents` line), and one banner line wrapped one character
past the pane edge (`… runs the ones it assesse` / `s`). The user restarted the pane
(`Alt+R`, the 17:32:47Z line above); the fresh process painted the SAME kind of corruption
(`Brewed for 2s · done 5:32 PM` sits inside it). A screenshot is attached to the PR.

Measured on the remote after the restart, `stty -F /proc/<pid>/fd/0 size`:

| pane | created by | PTY rows × cols |
|---|---|---|
| `pane-046a346f` (tab `New Tab`, renders fine) | TUI | 54 × 176 |
| `pane-74524201` (`mcp-claude`, corrupted) | MCP | 54 × 176 |

So after the restart the daemon-side PTY size is identical to a healthy pane. The daemon
log has no resize lines at INFO level.

### Known facts in the code

- `createPaneAt` spawns EVERY pane at 80×24 (`internal/daemon/daemon.go:2376`,
  `newSessionFn(80, 24)`); only a client's `resize_pane` corrects it. A TUI-created pane is
  resized within the same frame. An MCP-created pane in a hidden tab is resized only when
  the tab is first shown — after the child has painted at 80×24.
- Claude Code does not repaint on SIGWINCH (measured earlier: resize → 0 bytes,
  memory `claude-code-no-sigwinch-repaint`).
- The TUI builds one `vt.SafeEmulator(w, h)` per pane (`internal/tui/pane.go:327`,
  `newVTEmulator`) and `diffResizes` (`internal/tui/model.go:8554`) sends a resize only when
  the daemon's reported `Cols/Rows` differ from the layout's size for that pane.
- `handleRestartPaneReq` (`internal/daemon/daemon.go:6126`) respawns at the pane's last
  known size and resets `appliedCols/appliedRows` so the next `resize_pane` applies.

### Hypotheses — verify with a measurement, do not pick by reading

1. **Late first resize.** Painted at 80×24, resized to 176×54 later, no repaint → Ink erases
   the wrong number of previous lines on every later frame. Explains the first corruption;
   does NOT explain why the restart (at 176×54 from the start) was corrupted too.
2. **Emulator/PTY size split on the TUI side.** The TUI's emulator for a pane it did not
   create is built from the broadcast's `Cols/Rows` (80×24 or 0×0) and something skips the
   resize of the EMULATOR while the daemon PTY is already 176 wide — a child writing
   176-column lines into an 80-column emulator produces exactly the wrap-and-duplicate
   signature, and survives a restart because the restart keeps the emulator. Check
   `installVT` / `ResetVT` and where `PaneModel` first receives its size for a pane that
   appears in a broadcast rather than from `Ctrl+N`.
3. **Remote-only.** The pane runs through the ssh transport; the local dev daemon may not
   show it. Test both.

### Repro

1. Dev TUI (`quil-dev.exe`) with two tabs; stay on tab 1.
2. From a second bridge (`claude mcp add -s user quil-dev -- <worktree>\quil-dev.exe mcp`, or
   the `scripts` under memory `drive-dev-daemon-via-mcp-bridge`): `create_tab` then
   `create_pane type=claude-code` into that new tab, tab not visible.
3. Switch to the tab. Compare against a pane created with `Ctrl+N` in the same tab.
4. Log, for the MCP pane: emulator `w,h` at creation and after the switch, the daemon's
   `Cols/Rows` in each broadcast, and every `resize_pane` sent (`diffResizes` output).
5. Repeat with the remote (`quil-dev.exe --remote artyom@192.168.6.12` is NOT a thing for
   the bridge — the bridge is local-only; use the production layout: laptop bridge + remote
   destination, or a second dev daemon on the VM under `QUIL_HOME`).

### Fix direction (after the measurement)

- Daemon: when `create_pane_req` / `create_tab_req` carry no size, spawn at the size of a
  sibling pane in the tab, else the last attached client's `attach.Cols/Rows`
  (`daemon.go:1709`), else 80×24 — so an agent-created pane paints at a realistic size
  before any client sees it. This is cheap and right regardless of which hypothesis holds.
- TUI: whatever the measurement shows for hypothesis 2. A restart must heal a corrupted
  pane (it is the user's recovery path).
- Regression test driven through `Update` (memory `unit-test-bypassing-call-site`), not
  through the sizing function directly.

### Acceptance

An MCP-created Claude Code pane in a hidden tab, local and remote, renders like a
`Ctrl+N` pane after switching to it; `Alt+R` on a corrupted pane produces a clean screen.

---

## 3. Docs, roadmap and the site still say "18 tools"

`docs/mcp.md`, `docs/README.md`, `docs/quick-start.md`, `docs/roadmap.md` (M10 bullet)
and the `.claude/CLAUDE.md` "MCP Server" section were updated in #212. Everything below was
not. Source of truth: `docs/mcp.md` (34 tools, four groups: workspace control,
interaction/introspection, notifications, memory, projects and tabs, discovery, tasking).

| File | Line(s) | Says | Should say |
|---|---|---|---|
| `README.md` | 8 | badge `MCP-18%20tools` | `MCP-34%20tools` |
| `.claude/CLAUDE.md` | 243 | `docs/mcp.md — … all 18 tools` | 34 |
| `.claude/CLAUDE.md` | 275 | M10 row: `18 tools` | 34, note the 2026-09 additions (projects, hosts, tasks) |
| `docs/roadmap/mcp-server.md` | "MCP Tools (13 total)" table and everything after | 13 tools, phases A/B | 34 tools; add the projects/tabs, discovery, hosts and tasking sections; keep it a PRD (what and why), link `docs/mcp.md` for the reference |
| `docs/prd.md` | 636 | `exposes 18 tools` | 34 |
| `docs/competitive-analysis.md` | 32, 146, 221 | `18 tools` | 34; the 221 bullet can add "projects, remote hosts and pane-to-pane task delegation" |
| `docs/features.md` | ~475 (MCP paragraph in the notification section) and the MCP intro at 3 | only the notification tools | add one paragraph: projects/tabs, multi-host routing, `delegate_task` with notify-back; link `docs/mcp.md` anchors |
| `docs/roadmap.md` | 322 | "Deferred: per-project MCP scoping" | still true — leave; but check the M14 table row that lists "MCP project scoping" as deferred reads well next to the new M10 bullet |
| `site/src/data/features.ts` | 113 | `18 tools exposed over the Model Context Protocol` | 34; mention projects, hosts, tasks |
| `site/src/data/faq.ts` | 33 | `exposes 18 tools so any MCP-capable client can read pane output, send keystrokes, snapshot a workspace, and query per-pane memory usage` | 34; add "create tabs and AI panes with the dialog's options, manage projects across remote hosts, and delegate work between AI panes" |
| `site/src/data/competitors.ts` | 410 | `Quil exposes 18 tools over the Model Context Protocol` | 34 |
| `site/src/pages/docs.astro` | 53 | `all 18 tools` | 34 |
| `site/src/pages/index.astro` | 77 | `… snapshot your workspace. 18 tools.` | 34, and one clause on delegation between AI panes |
| `site/src/pages/features.astro` | `#mcp-server` section | check the body text for the count and the tool list | match `docs/mcp.md` |

Rules for the site: do not run `npm install` on Windows (memory `npm-lock-is-platform-shaped`);
text-only edits need no lock change. `ci.yml` has a `site` job that builds it.

Do not touch `CHANGELOG.md` (release-owned) and do not add a changelog fragment for this
item (a docs-only change must not cut a release of byte-identical binaries).

### Acceptance

`grep -rn "18 tools\|18%20tools\|13 total" README.md docs site/src .claude` returns nothing;
`docs/roadmap/mcp-server.md` lists all 34 tools by group; the site builds in CI.
