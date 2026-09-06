# Alt-screen replay detection (issue #172) — Spec + Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop replaying a ghost buffer into a pane whose child is on the alternate screen, by observing what the child is actually doing rather than trusting a per-plugin default that was measured against a different renderer.

**Architecture:** The daemon already scans every pane's output for `CSI ? <params> h/l` sequences (`scanMouseModes`) and its own comment lists `1049` among the modes it steps over. Track that mode, and let `handleAttach` treat "this pane is on the alt screen right now" as an override of the plugin's `ghost_buffer` default — falling back to the existing `redrawKick`. No new machinery, no client change: the TUI already keys its reattach reset off whether a replay actually arrived.

**Tech Stack:** Go 1.25, `internal/daemon` (`mousemode.go`, `daemon.go`). No new dependencies.

**Spec:** this document, section [Spec](#spec). Issue: https://github.com/artyomsv/quil/issues/172. Sibling: #171.

---

## Spec

### Problem

`claude-code` ships `ghost_buffer = true` (2026-08-01, schema_version 11). The measurement behind it was explicit: claude-code writes to the **main** screen and scrolls normally, so its output stream replays into coherent history.

That is a statement about Claude Code's **classic** renderer. Its **fullscreen** renderer draws on the alternate screen buffer like `vim`, and is the default for anyone whose first run was on or after 2026-05-06 — including the reporter of #169, whose pane was confirmed on the alt screen (`?1049h` in its own output stream).

A ring buffer of alt-screen output can begin mid-escape-sequence and encodes absolute cursor positioning against a screen the replay does not own — the exact condition `ghost_buffer = false` exists for. So the shipped default is probably wrong for most new users, in the direction of a *corrupted* replay rather than a missing one.

Quil has no renderer awareness at all today: `1049` appears once in the whole codebase, in a comment in `internal/daemon/mousemode.go` naming it as a mode the scanner steps over.

### Which replay path this is actually about

`handleAttach` has two replay sources, and only one of them is in scope:

| Source | When | Status |
|---|---|---|
| `ghostsnap` | first attach after a daemon restore | Already skipped for claude-code by `restoresOwnHistory` (`preassign_id`), because the respawned child repaints its own transcript |
| `outputbuf` | reconnect to a **live** child | **This is the one.** The daemon has watched the whole stream, so it also knows the child's current screen |

That is the fortunate part of the design: detection is available exactly where the replay still happens.

### Decision

Track the alternate-screen mode per pane in the existing scanner, and skip the ghost replay for a pane currently on it. The plugin's `ghost_buffer` stays a **default**; what the child is doing overrides it.

Chosen over the alternatives because:

- **Flipping `claude-code` to `ghost_buffer = false`** would need a schema bump (a user's own TOML overrides the embedded default) and would remove *working* history replay for every classic-renderer user, to fix a case that only exists in the other renderer.
- **Renderer detection by asking claude-code** is not available — the daemon spawns a process, not a protocol.
- Observing the mode generalises: `opencode`, `lazygit`, `k9s` and any future TUI get the same correctness for free, and a program that switches renderers mid-session (`/tui fullscreen` relaunches in place) is handled without configuration.

### What this does NOT change

- `ghost_buffer = false` plugins behave exactly as today.
- A pane on the main screen replays exactly as today.
- `ghostScrollOut` and the `restoresOwnHistory` skip are untouched.
- Buffer *persistence* for alt-screen panes is unchanged. Persisting a screenful of repaint noise is a real cost (called out in `claude-code.toml`), but it is a separate decision from whether to replay, and bundling them would put a disk-format change in a correctness fix.

### Open question the measurement must answer first

The premise — "replaying alt-screen output produces a corrupted pane" — is **reasoned, not measured**. Task 1 exists to settle it before any behaviour changes, and it is a real gate: if the replay turns out coherent, the correct outcome is to record the evidence, close #172, and write nothing.

## Global Constraints

- Go 1.25, module `github.com/artyomsv/quil`. No new dependencies.
- No Go or make on the host: everything runs through `./scripts/dev.sh` (Docker). `dev.sh test` takes exactly ONE package argument; extra args are silently dropped.
- Development runs in dev mode only. Never touch `~/.quil/`, the production daemon, or the `kill-daemon`/`reset-daemon` scripts.
- Any dev daemon spawning `claude` must start with `env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_SESSION_ID`, or the inherited marker turns transcript saving off in the pane and every `--resume` restore fails with "No conversation found".
- One `changelog.d/<type>-<slug>.md` fragment; `CHANGELOG.md` is never hand-edited.
- Commits: imperative, ≤72 chars, reference the issue, no AI/agent attribution of any kind.

## File Structure

| File | Responsibility |
|---|---|
| `internal/daemon/mousemode.go` *(modify)* | Track `?47` / `?1047` / `?1049` in the existing scan; `altScreen` field on `mouseModeState` |
| `internal/daemon/mousemode_test.go` *(modify)* | Scanner cases, including the split-chunk carry |
| `internal/daemon/daemon.go` *(modify)* | `handleAttach:1430` — alt screen overrides the plugin's `ghost_buffer` |
| `internal/daemon/altscreen_replay_test.go` *(new)* | Integration: a pane on the alt screen gets a redraw kick, not a replay |
| `docs/plugin-reference.md` *(modify)* | `ghost_buffer` is a default, not a decision |
| `.claude/rules/daemon-lifecycle.md` *(modify)* | Record the override and why detection beats configuration here |
| `changelog.d/fixed-alt-screen-replay.md` *(new)* | User-facing entry |
| `docs/superpowers/plans/2026-08-19-alt-screen-replay-detection.md` | This plan — Task 1 writes its findings back into it |

---

### Task 1: Measure what a replay actually does in each renderer

**This task is a gate, not a formality.** Its output decides whether Tasks 2-4 happen at all.

**Files:**
- Modify: this plan (the "Findings" block at the end of this task)

**Interfaces:**
- Consumes: `./scripts/dev.sh build`, a dev daemon, a real `claude` binary.
- Produces: a recorded answer to "is an outputbuf replay coherent under each renderer?", pasted into this document and onto issue #172.

- [ ] **Step 1: Build and start an isolated dev daemon**

```bash
cd E:/Projects/Stukans/quil-worktrees/fix-issue-172
./scripts/dev.sh build            # confirm the binary mtimes actually moved
env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_SESSION_ID \
  QUIL_HOME='E:\Projects\Stukans\quil-worktrees\fix-issue-172\.quil' \
  CLAUDE_CODE_NO_FLICKER=1 \
  ./quild-dev.exe --background
```

`CLAUDE_CODE_NO_FLICKER=1` puts spawned claude panes in the fullscreen renderer. Confirm per pane rather than trusting the variable:

```bash
grep -c $'\x1b\[?1049h' .quil/buffers/<pane-id>.bin    # 1 = on the alt screen
```

- [ ] **Step 2: Produce a populated fullscreen pane**

Launch the dev TUI in its OWN terminal window — `wt.exe -w -1 …`, because a plain `wt.exe` merges into the user's existing window, which is running their production session. Create a `claude-code` pane, and give it enough output to make a corrupted replay obvious (a short prompt whose answer fills the pane, or `/help`).

- [ ] **Step 3: Reattach in-memory and record the grid**

Close the dev TUI, relaunch it against the same daemon. This is the `outputbuf` path — a live child, no restore. Capture what the pane looks like:

```bash
# screenshot_pane has been observed to time out on claude panes; read_pane_output works
./quil-dev.exe mcp   # tools/call read_pane_output {"pane_id":"…","last_lines":60}
```

Record verbatim: is the pane coherent, blank, doubled, or overprinted? Note whether the prompt lands on the same row as earlier content.

- [ ] **Step 4: Repeat with the classic renderer**

Restart the dev daemon **without** `CLAUDE_CODE_NO_FLICKER`, create a fresh claude pane (confirm no `?1049h` in its buffer), populate it, and reattach the same way. This is the control: the 2026-08-01 measurement says this one replays into coherent history, and if that no longer holds the whole `ghost_buffer = true` decision is stale for a second, unrelated reason.

- [ ] **Step 5: Write the findings down, then decide**

Append to this document, and comment the same text on issue #172:

```markdown
## Findings (2026-__-__)

| Renderer | Alt screen? | Reattach replay result |
|---|---|---|
| fullscreen (`CLAUDE_CODE_NO_FLICKER=1`) | yes (`?1049h` present) | … |
| classic | no | … |
```

## Findings (2026-08-19) — measured, gate PASSED, continue to Task 2

Method deviates from Steps 3-4 as written, and the deviation is an improvement worth keeping: rather than eyeballing a reattached TUI window, the pane's captured ring buffer was replayed through the **real client emulator** (`PaneModel.AppendOutput` + `vtRow`, the harness `internal/tui/ghost_scrollout_grid_test.go` already uses) and the resulting grid dumped. That answers the same question — what does a reconnecting client see — reproducibly, and it is the only way to inspect the grid without screen-scraping a GUI window.

Both panes ran real `claude` v2.1.235/236 in a dev daemon, rendered at 120x30.

| Renderer | `?1049h` in buffer | Whole buffer | Wrapped (arbitrary head cut) |
|---|---|---|---|
| fullscreen (`CLAUDE_CODE_NO_FLICKER=1`) | **yes** | coherent | **corrupt** |
| classic (`CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1`) | no | coherent | clipped but clean |

Fullscreen, cut at three arbitrary points — row 0 of each:

```
30%:  ors"                                     ← tail of a truncated string, banner gone
55%:  [H                                       ← a torn escape sequence PAINTED AS TEXT
80%:  6;101m✔ Update installed · Restart…      ← SGR parameter residue painted as text
```

25-27 of 30 rows blank in each. Classic, same treatment, cut at 30%:

```
──────────────────────────────────────────────  ← a clipped separator, then coherent content
❯ /help
```

**The mechanism is sharper than the issue assumed.** It is not only that a ring buffer can begin mid-escape-sequence (though it does — see 55% and 80%). Claude Code's fullscreen renderer sends **only the cells that changed between frames**; its own docs say so, and the measurement agrees: several repaints grew the buffer by ~900 bytes total. So its byte stream is *not self-contained history*. Replaying any suffix of it applies a handful of deltas to a blank screen and can never reconstruct a frame, however the cut lands. The classic renderer writes whole lines to the main screen and scrolls, so a suffix is exactly what it looks like — history with the top clipped.

**Why the whole-buffer case looks fine, and why that does not save it:** a buffer that still contains the child's own `?1049h` and first paint replays coherently. That only holds until the ring wraps — 256 KB by default, and `claude-code.toml` already notes an AI pane spends that budget faster than a shell does. So the coherent case is the first few minutes of a pane's life, and the corrupt case is the rest of it.

**Second-order consequence, and the reason this is worth fixing rather than tolerating:** with `ghost_buffer = true` the pane receives a replay *instead of* a `redrawKick`. So the torn frame is not transient — nothing asks the child to repaint, and claude-code paints on its own render tick, which input drives. The pane sits corrupt until the user types.

**Gate:**
- Fullscreen replay is corrupted → continue to Task 2. ✅ **This is the outcome.**
- Fullscreen replay is coherent → **stop**. Close #172 with the evidence, note that `ghost_buffer = true` is correct for both renderers, and delete the unused branch. Record the classic-renderer result either way; it is the control that keeps this from being re-litigated.

- [ ] **Step 6: Commit the findings**

```bash
git add docs/superpowers/plans/2026-08-19-alt-screen-replay-detection.md
git commit -m "docs: record what a ghost replay does in each claude renderer

Refs #172"
```

---

### Task 2: Track the alternate screen in the output scanner

**Files:**
- Modify: `internal/daemon/mousemode.go` (`mouseModeState` struct; the `switch string(param)` inside `scanMouseModes`)
- Modify: `internal/daemon/mousemode_test.go`

**Interfaces:**
- Consumes: existing `scanMouseModes(m mouseModeState, data []byte) (mouseModeState, []byte)`.
- Produces: `mouseModeState.altScreen bool`, set by `?47`, `?1047`, `?1049`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/daemon/mousemode_test.go`:

```go
// The alternate screen rides this scanner for the same reason bracketed paste
// does: the daemon sees the pane's whole stream from spawn, and a client that
// attaches later never sees the one-time enable. handleAttach needs the answer
// to decide whether a ghost replay is history or garbage (issue #172).
func TestScanMouseModes_TracksTheAlternateScreen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"1049 enter", "\x1b[?1049h", true},
		{"1049 leave", "\x1b[?1049l", false},
		{"legacy 47 enter", "\x1b[?47h", true},
		{"legacy 1047 enter", "\x1b[?1047h", true},
		{"combined with mouse modes", "\x1b[?1049;1000;1006h", true},
		{"unrelated mode does not set it", "\x1b[?25h", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := scanMouseModes(mouseModeState{}, []byte(tt.in))
			if got.altScreen != tt.want {
				t.Errorf("altScreen = %v, want %v after %q", got.altScreen, tt.want, tt.in)
			}
		})
	}
}

// Enter-then-leave in one chunk must end on the LAST state, not the first —
// a program that redraws through an alt-screen round trip is on the main
// screen afterwards and its buffer replays fine.
func TestScanMouseModes_AlternateScreenRoundTripEndsOff(t *testing.T) {
	got, _ := scanMouseModes(mouseModeState{}, []byte("\x1b[?1049h...content...\x1b[?1049l"))
	if got.altScreen {
		t.Error("altScreen = true after an enter/leave round trip, want false")
	}
}

// The carry path: claude-code's startup burst is long enough to be split by
// the 32 KB read boundary, and a missed enter would let a corrupted replay
// through — the exact failure this tracking exists to prevent.
func TestScanMouseModes_AlternateScreenSplitAcrossChunks(t *testing.T) {
	m, tail := scanMouseModes(mouseModeState{}, []byte("noise\x1b[?10"))
	if m.altScreen {
		t.Fatal("altScreen set from an incomplete sequence")
	}
	joined := append(append([]byte{}, tail...), []byte("49h")...)
	m, _ = scanMouseModes(m, joined)
	if !m.altScreen {
		t.Error("altScreen = false, want true — the split enable was lost")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `got.altScreen undefined (type mouseModeState has no field or method altScreen)`.

- [ ] **Step 3: Add the field**

In `internal/daemon/mousemode.go`, inside `mouseModeState`:

```go
	// altScreen is ?1049 (and the older ?47 / ?1047). Not a mouse mode, and it
	// rides this scanner for the same reason bracketedPaste does: it needs the
	// daemon-side lifetime described in the header comment — a client that
	// attaches to an already-running pane never sees the enable.
	//
	// handleAttach consumes it. A pane on the alternate screen has no
	// replayable history: its ring buffer can begin mid-escape-sequence and
	// positions absolutely against a screen the replay does not own, which is
	// the condition ghost_buffer = false exists for (issue #172).
	altScreen bool
```

- [ ] **Step 4: Track the modes**

In the `switch string(param)` inside `scanMouseModes`:

```go
				case "47", "1047", "1049":
					m.altScreen = set
```

`?1049` is what every current program emits; `?47` and `?1047` are the older forms, tracked because they mean the same thing to the grid and cost one case label.

- [ ] **Step 5: Run the tests**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS, including the pre-existing `TestScanMouseModes*` cases.

- [ ] **Step 6: Check the broadcast side-effect**

`flushPaneOutput` broadcasts workspace state when `newModes != pane.mouseBroadcast`. Adding a field to the struct means an alt-screen toggle now also triggers that broadcast. Confirm this is acceptable: an enter/leave pair happens once at program start and once at exit, the same order of magnitude as the mouse-enable burst the throttle was designed for.

Run: `./scripts/dev.sh test internal/daemon` (the `mousemode_flush_test.go` cases cover the throttle) and confirm no test asserts an exact broadcast count that this changes. If one does, that test is the place to record the new behaviour.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/mousemode.go internal/daemon/mousemode_test.go
git commit -m "feat(daemon): track the alternate screen per pane

The scanner already walked every private-mode sequence and stepped over
1049. Tracking it is what lets attach tell a replayable buffer from a
screenful of absolute-positioned repaint.

Refs #172"
```

---

### Task 3: Let the alternate screen override the plugin default

**Files:**
- Modify: `internal/daemon/daemon.go` (`handleAttach`, the `ghostEnabled` decision at `:1430`)
- Create: `internal/daemon/altscreen_replay_test.go`

**Interfaces:**
- Consumes: `mouseModeState.altScreen` from Task 2; existing `redrawKick`.
- Produces: no new exported surface.

- [ ] **Step 1: Write the failing integration test**

Create `internal/daemon/altscreen_replay_test.go`:

```go
//go:build integration

package daemon

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
)

// Issue #172: claude-code ships ghost_buffer = true, measured when it wrote to
// the MAIN screen. Its fullscreen renderer — the default for installs first run
// on or after 2026-05-06 — draws on the alternate screen, where a ring buffer
// begins mid-escape-sequence and positions absolutely against a screen the
// replay does not own.
//
// The plugin default stays; what the child is DOING overrides it. The terminal
// pane in the same attach is the control: a shell on the main screen must still
// get its history, or this fix would trade one silent loss for another.
func TestHandleAttach_SkipsReplayForAPaneOnTheAlternateScreen(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	mkPane := func(typ string, alt bool) *Pane {
		t.Helper()
		pane, err := d.session.CreatePane(tab.ID, "/tmp")
		if err != nil {
			t.Fatalf("CreatePane: %v", err)
		}
		pane.PluginMu.Lock()
		pane.Type = typ
		pane.MouseModes.altScreen = alt
		pane.PluginMu.Unlock()
		// OutputBuf, not GhostSnap: this is the reconnect path, where the child
		// is alive and the replay is the only history there is. The ghostsnap
		// path is already skipped for claude-code by restoresOwnHistory.
		pane.OutputBuf.Write(bytes.Repeat([]byte{'g'}, 4096))
		return pane
	}
	fullscreen := mkPane("claude-code", true)
	shell := mkPane("terminal", false)

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	// Guard: both plugins must resolve and both must declare ghost_buffer =
	// true, or this test proves nothing about an override.
	for _, typ := range []string{"claude-code", "terminal"} {
		p := d.registry.Get(typ)
		if p == nil || !p.Persistence.GhostBuffer {
			t.Fatalf("setup: %s must resolve with ghost_buffer = true", typ)
		}
	}

	conn := dialDaemon(t, filepath.Join(tmp, "quild.sock"))
	defer conn.Close()

	attach, err := ipc.NewMessage(ipc.MsgAttach, ipc.AttachPayload{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("NewMessage attach: %v", err)
	}
	if err := ipc.WriteMessage(conn, attach); err != nil {
		t.Fatalf("write attach: %v", err)
	}

	// Read until the shell's replay has fully arrived. The alt-screen pane's
	// frames, if the daemon wrongly sent any, precede it in the same pane loop.
	ghost := map[string]int{}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for ghost[shell.ID] < 4096 {
		msg, err := ipc.ReadMessage(conn)
		if err != nil {
			t.Fatalf("read after %v: %v", ghost, err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		var p ipc.PaneOutputPayload
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode pane output: %v", err)
		}
		if p.Ghost {
			ghost[p.PaneID] += len(p.Data)
		}
	}

	if n := ghost[fullscreen.ID]; n != 0 {
		t.Errorf("alt-screen pane received %d ghost bytes; that buffer can begin "+
			"mid-sequence and positions against a screen the replay does not own", n)
	}
	if n := ghost[shell.ID]; n != 4096 {
		t.Errorf("main-screen pane received %d ghost bytes, want 4096 — a shell "+
			"reprints none of its scrollback, so this replay is its only history", n)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `./scripts/dev.sh test-integration internal/daemon`
Expected: FAIL — `alt-screen pane received 4096 ghost bytes`.

- [ ] **Step 3: Apply the override**

In `internal/daemon/daemon.go`, replace the `ghostEnabled` block at `:1430`:

```go
			ghostEnabled := true
			if p := d.registry.Get(typ); p != nil && !p.Persistence.GhostBuffer {
				ghostEnabled = false
			}
			// A plugin's ghost_buffer is a DEFAULT; what the child is doing
			// right now overrides it. A pane on the alternate screen has no
			// replayable history — its ring buffer can begin mid-escape-
			// sequence and positions absolutely against a screen the replay
			// does not own, which is the condition ghost_buffer = false exists
			// for. claude-code's value was measured against its classic
			// renderer and its fullscreen one is now the default for new
			// installs (issue #172); reading the mode covers both, and covers
			// any other program that switches mid-session. The pane falls
			// through to redrawKick below, exactly as an opted-out plugin does.
			//
			// Read here rather than in the registry lookup: this is a fact
			// about the process, not about the plugin. MouseModes is
			// PluginMu-protected and this span already holds it.
			if pane.MouseModes.altScreen {
				ghostEnabled = false
			}
```

Confirm the read sits inside the existing `pane.PluginMu` span that reads `Type` and `GhostSnap`; if it does not, move it in rather than taking the lock twice — the two must describe the same instant.

- [ ] **Step 4: Run both suites**

Run: `./scripts/dev.sh test-integration internal/daemon`
Expected: PASS.
Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS.
Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 5: Verify the test is mutation-resistant**

Temporarily invert the new condition (`if false && pane.MouseModes.altScreen`), re-run `./scripts/dev.sh test-integration internal/daemon`, and confirm the new test fails with its intended message. Restore, re-run, confirm green. A test that passes either way is not protecting anything — this project has shipped one of those before.

- [ ] **Step 6: Confirm it end-to-end against a real claude**

Repeat Task 1's fullscreen scenario against the built binary and confirm the daemon log now reads `attach: redraw kick pane … (type=claude-code, no ghost replay, …)` for the fullscreen pane, and that the pane comes back coherent rather than corrupted. This is the same loop Task 1 used, so it costs one more run of an environment you already have.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/altscreen_replay_test.go
git commit -m "fix(daemon): do not replay a buffer into an alt-screen pane

ghost_buffer for claude-code was measured when it wrote to the main
screen. Its fullscreen renderer draws on the alternate screen and is the
default for new installs, where a replay begins mid-sequence and paints
against a screen it does not own.

The plugin value stays a default; the mode the child actually set wins.

Fixes #172"
```

---

### Task 4: Documentation and changelog

**Files:**
- Modify: `docs/plugin-reference.md` (the `ghost_buffer` field entry and its prose)
- Modify: `.claude/rules/daemon-lifecycle.md` (the ghost-replay section, near the `claude-code ships ghost_buffer = true` paragraph)
- Create: `changelog.d/fixed-alt-screen-replay.md`

- [ ] **Step 1: Document that `ghost_buffer` is a default**

In `docs/plugin-reference.md`, under `ghost_buffer`:

```markdown
This is a **default, not a decision.** Quil watches each pane's output for the
alternate-screen mode (`?1049`, and the older `?47` / `?1047`), and a pane whose
program is on the alternate screen never receives a replay regardless of this
setting — its scrollback is a screenful of absolute-positioned repaint, not
history. Set `ghost_buffer = true` when your program's stream replays into
coherent history *when it is on the main screen*; the alt-screen case is handled
for you, including a program that switches at runtime.
```

- [ ] **Step 2: Record the invariant where daemon work loads it**

In `.claude/rules/daemon-lifecycle.md`, beside the existing `claude-code ships ghost_buffer = true` paragraph:

```markdown
**`ghost_buffer` is overridden at attach by the pane's live alternate-screen state** (`handleAttach`, via `mouseModeState.altScreen`). The 2026-08-01 measurement behind `claude-code`'s `true` was taken against its CLASSIC renderer — "writes to the MAIN screen and scrolls normally" — and its fullscreen renderer, an alt-screen app, became the default for every install first run on or after 2026-05-06, which made the shipped default wrong for most new users in the direction of a CORRUPTED replay rather than a missing one (issue #172). Detection rather than configuration, for three reasons: a user's own plugin TOML overrides the embedded default, so flipping the value reaches no existing install; the same value is right for the classic renderer, which is still in use; and `/tui fullscreen` relaunches a session in place, so the answer can change mid-pane. Only the `outputbuf` path is affected in practice — the `ghostsnap` path is already skipped for claude-code by `restoresOwnHistory`. The mode rides `scanMouseModes` because the daemon sees a pane's stream from spawn while a later-attaching client never sees the one-time enable, which is the same argument bracketed paste already lives there on.
```

- [ ] **Step 3: Add the changelog fragment**

Create `changelog.d/fixed-alt-screen-replay.md`:

```markdown
---
headline: Full-screen panes come back clean after a reconnect
---
- **Reconnecting no longer paints garbage into a full-screen pane.** Quil replays
  a pane's recent output when a client reconnects, which reproduces history for a
  normal shell but not for a program drawing on the alternate screen — there the
  saved bytes can begin mid-sequence and position against a screen the replay
  does not own. Claude Code's newer full-screen renderer is one of those, and it
  is the default for recent installs.

  Quil now notices which panes are on the alternate screen and asks those
  programs to repaint instead of replaying at them.
```

Headline is 51 bytes (limit 64), no `"` or `\`.

- [ ] **Step 4: Validate the gates**

Run: `sh scripts/promote-changelog.sh --validate` → accepts the fragment.
Run: `./scripts/dev.sh docs-size` → `.claude/rules/daemon-lifecycle.md` still under its limit.

- [ ] **Step 5: Commit**

```bash
git add docs/plugin-reference.md .claude/rules/daemon-lifecycle.md changelog.d/fixed-alt-screen-replay.md
git commit -m "docs: record that ghost_buffer is a default, not a decision

Refs #172"
```

---

## Risks and things that could go wrong

1. **The premise fails.** Task 1 may show the fullscreen replay is fine. That is a success, not a wasted branch — the measurement is the deliverable and #172 closes with evidence instead of a guess.
2. **A pane legitimately leaves the alt screen before reattach.** Then `altScreen` is false and the replay proceeds — correct, and the buffer's alt-screen segments are entered and discarded by the client's emulator on the way through.
3. **A restored pane has no mode state.** After a daemon restart the child is respawned and the daemon has seen nothing yet, so `altScreen` is false until the child re-emits its enable. Harmless here: that is the `ghostsnap` path, already skipped for claude-code by `restoresOwnHistory`, and any other plugin gets the same behaviour it has today.
4. **Adding a field changes broadcast throttling.** `flushPaneOutput` compares `mouseModeState` values, so an alt-screen toggle now also triggers a workspace broadcast. Step 6 of Task 2 exists to confirm that is acceptable rather than to discover it in production.
5. **Buffer cost is untouched.** An alt-screen pane still fills and persists a ring buffer nobody replays. Deliberately out of scope (see Spec); worth a follow-up techdebt entry if Task 1 shows the buffers are large.

## Open decisions for review

1. **Scope to `claude-code`, or let it apply to every plugin?** The plan applies it to every pane, which is what makes it a correctness property rather than a special case — and `opencode`/`lazygit` already declare `ghost_buffer = false`, so nothing changes for them today. Say so if you would rather gate it on plugin type.
2. **Should the flag reach clients?** Not needed for this fix (the TUI keys its reattach reset off whether a replay arrived), and it rides the workspace snapshot anyway once it is in `mouseModeState`. No client change is planned.
