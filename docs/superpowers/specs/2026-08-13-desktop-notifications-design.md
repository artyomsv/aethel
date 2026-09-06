# Desktop Notifications from the Attention Model — Design

**Date:** 2026-08-13
**Status:** Approved
**Milestone:** M17 item 14 (`docs/roadmap.md`), priority-matrix row 7
**Extends:** [notification-center](../../roadmap/notification-center.md) (M12, Done)

## Problem

Quil already knows when a pane needs the user. `applyWorkTransition`
(`internal/tui/workstate.go`) derives per-pane attention state from the hook
event stream, `ProjectModel.counts()` rolls it up into the project sidebar
badges, and `blockedPanes()` sorts every waiting pane oldest-first for
`Alt+Shift+A`.

None of it leaves the TUI window. There is no OS notification path of any kind:
no OSC 9, no `notify-send`, no `terminal-notifier`, no Win32 toast. The daemon
even *consumes* the terminal bell — `detectBellEvent` turns `\x07` into an
in-app event and nothing re-emits it — so the free taskbar-flash path is closed
too. A user working in another window learns that an agent has been parked for
ten minutes only by switching back to Quil and looking.

The notification sidebar is not the answer to this. It carries every event kind
at every severity, which makes it a log rather than an alert; the signal the
user reacts to in practice is the sidebar badge, not the event list.

## Decision

Raise a **native Windows toast** on the attention transitions the project
sidebar already renders, gated on the terminal being unfocused. Clicking the
toast routes Quil to the exact project, tab and pane that raised it.

The toast is fired by the **TUI client**, not the daemon. This is forced rather
than chosen: `blockedSince` and `unseen` are fields on `PaneModel` in
`internal/tui`, derived client-side with no daemon involvement, so the daemon
cannot compute the trigger. It is also correct for remote mode — the toast
belongs on the machine with the screen, not the machine with the panes.

### Decisions captured during design

1. **Triggers are `blocked` (▲) and `done` (✓ unseen).** Not `working` (◐ —
   noise) and not `pinned` (◆ — the user set it by hand while looking at the
   screen).
2. **Setup is an explicit `quil notify setup` command.** Windows toasts from an
   unpackaged `.exe` require artifacts outside `QUIL_HOME`; nothing is written
   until the user asks. This is unchanged by decision 6 — the config flag
   expresses intent, registration remains the gate, and no code path registers
   as a side effect of a flag.
3. **One toast per pane, rate-limited per pane.** Precise routing is the
   feature; coalescing destroys it. Windows collapses overflow into Action
   Center on its own.
4. **Routing ships first; raising the terminal window is a follow-up task.**
   A failure to locate the window degrades to "routed but not raised" rather
   than blocking the feature.
5. **The click returns over a TUI-local named pipe**, not through the daemon.
6. **Notifications default to ENABLED, and are togglable from the F1 → Settings
   dialog as well as the TOML config.** The Settings row reports registration
   state rather than the raw flag, and never performs registration itself.

## Architecture

### Package layout

```
internal/notify/
  notify.go            # Notification struct, New() → Notifier or nil
  toastxml.go          # toast XML construction        ← no build tag
  activate.go          # quil:// URI build + parse     ← no build tag
  toastxml_test.go
  activate_test.go

  notify_windows.go    # WinRT toast emit + withdraw
  notify_other.go      # no-op Notifier
  listener_windows.go  # \\.\pipe\quil-activate-<pid>
  listener_other.go    # no-op
  setup_windows.go     # .lnk with AUMID + HKCU quil:// key
  setup_other.go       # ErrUnsupported

cmd/quil/notify.go     # quil notify setup|--remove|status, quil activate <uri>
```

**Everything with logic is platform-neutral; everything platform-specific is
nearly logic-free.** This is the load-bearing property of the layout, not a
stylistic preference. `dev.sh test` runs in a Linux container, so any file
behind `//go:build windows` is never compiled by CI — and a `GOOS=windows vet`
proves only that it compiles. This repo has already paid for that gap once:
`out.RootsTruncated = !complete` in `browse_roots.go` was statically dead code
on the platform CI runs, and inverting it passed every test in the tree until a
package-var seam existed. The XML builder, URI codec, edge detector, cooldown
and focus gate therefore all live in neutral files with real tests.

### Consumer-side interface

Defined at the consumer per `~/.claude/rules/go-conventions.md`:

```go
// internal/tui
type desktopNotifier interface {
	Notify(notify.Notification) error
	Withdraw(tag string) error
}
```

`Withdraw` is on the interface from the start rather than added later. Windows
toasts persist in Action Center indefinitely, so without a falling-edge
withdrawal, answering a prompt leaves a toast that still claims the pane needs
attention — a notification surface that accumulates lies, which is the
complaint that motivated this work.

### Toast emission mechanism

Direct WinRT interop (`RoInitialize` / `RoGetActivationFactory` / `HSTRING`)
following the existing house pattern in `internal/clipboard/clipboard_windows.go`,
which already does raw `user32.NewProc` syscall interop.

Rejected: shelling out to PowerShell. It costs 200–500 ms per toast and risks
the console-window flash that made every `gitinfo` exec need `CREATE_NO_WINDOW`.

**Task 0 is a spike** to confirm the pure-Go interop path before committing to
it. If it proves unreasonable, the fallback is a vendored pure-Go WinRT helper —
never PowerShell, and never a borrowed AUMID (see below).

## Windows prerequisites

Windows refuses to show a toast from an unpackaged `.exe` with no
**AppUserModelID**, and only trusts an AUMID backed by a Start Menu shortcut
carrying `System.AppUserModel.ID`. That shortcut also supplies the toast's
displayed app name and icon.

Two artifacts, both user-scope, no admin:

| Artifact | Purpose |
|---|---|
| `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Quil.lnk` | Carries the AUMID; supplies toast name + icon (reuses `winres/`) |
| `HKCU\Software\Classes\quil\shell\open\command` | Makes `quil://` clickable |

**Borrowing another app's AUMID is rejected.** It is the common workaround in
toast libraries (PowerShell's AUMID is the usual choice), but the toast then
appears in Action Center as that app and activates that app on click — which
destroys click routing, the entire point of the feature.

**Protocol activation is chosen over a COM activator.** A COM activator means
registering a CLSID and hand-implementing `INotificationActivationCallback`
vtables with no CGo available. Protocol activation
(`<toast launch="quil://…" activationType="protocol">`) needs only an `HKCU`
key and delivers the same result.

### Dev-mode namespacing

The toast artifacts are **machine-global** and therefore the first thing Quil
writes that `QUIL_HOME` cannot redirect. Every other piece of dev isolation
works because `QUIL_HOME` covers it; this is the exception, and left unhandled
a `quil-dev.exe notify setup` would overwrite production's registration and
point `quil://` at the dev binary — precisely what `.claude/rules/dev-environment.md`
forbids.

Artifacts are namespaced by build variant, selected off the existing
`buildDevMode` ldflag. No new build machinery.

| | prod | dev |
|---|---|---|
| AUMID | `artyomsv.quil` | `artyomsv.quil.dev` |
| Scheme | `quil://` | `quil-dev://` |
| Shortcut | `Quil.lnk` | `Quil (dev).lnk` |

This also makes the feature testable in dev mode, which it otherwise would not
be.

## Trigger and gating

### Edge detection

`applyWorkTransition` (`internal/tui/workstate.go:233`) already captures
`wasWorking` at the top and has a single derivation point at line 400 with an
edge switch below it, carrying the comment *"the edge actions below key off the
before/after pair so they fire exactly once per transition."* That is exactly
the contract a toast needs, so the toast extends that pattern rather than
introducing a second one:

```go
wasWorking := pane.working
wasBlocked := !pane.blockedSince.IsZero()   // new, beside it
wasUnseen  := pane.unseen                   // new, beside it
...
// after the existing edge switch, ONE call:
m.raiseAttentionToast(pane, proj, wasBlocked, wasUnseen)
```

**One call site, not three.** `blockedSince` is written at lines 350 and 384 and
`unseen` at line 421; emitting at each write would mean a fourth write site
added later silently emits nothing. Deriving from the before/after pair is
structurally immune to that, which is why the spinner already works this way.

### Withdrawal is a sweep, not an edge

The rising edge has a single choke point. **The falling edge does not**, and
that asymmetry is the reason withdrawal cannot mirror emission.

`blockedSince` and `unseen` are cleared in at least three places outside
`applyWorkTransition`:

| Site | Trigger |
|---|---|
| `PaneModel.answerBlockedByInput` | The user types an answer into the pane |
| `Model.ackFocusedPane` | The user focuses the pane (clears `unseen`) |
| `ctxmenu.go:634` | The "Clear attention" context-menu row |

The first is the common case and the one that matters most: approving a
Bash/Edit/Write prompt **fires no hook at all**, so `applyWorkTransition` is
never reached and an edge-based withdrawal would leave the toast standing for
the pane's entire remaining turn. Pane destruction is a fourth path with no
transition of its own.

Withdrawal is therefore a **sweep over outstanding toasts**, not a hook on each
clear site:

```go
// Model
outstandingToasts map[string]struct{}   // paneID → a toast is live for it
```

`raiseAttentionToast` adds to the set. A sweep runs beside `ackFocusedPane` at
the top of `Update` — the existing choke point every message already passes
through — and withdraws any tag whose pane is gone or no longer blocked and no
longer unseen. It returns immediately when the set is empty, which is the
overwhelmingly common state, so it costs nothing on the 100 ms spinner tick.

A sweep rather than four call sites for the same reason the rising edge is one
call site: a fifth clear path added later is covered automatically, whereas a
forgotten `Withdraw` is invisible until someone notices Action Center is lying.

Two properties fall out of the existing code:

- **`done` is already focus-gated.** Line 419 sets `unseen` only when the pane
  is not the focused pane of the active tab, so a `done` toast inherits that
  gate for free.
- **`blocked` deliberately is not.** `blockedSince` is stamped regardless of
  focus — `paneRow` suppresses the *presentation* for the focused pane while
  every counter keeps reading the live flag. Terminal-blur gating is what
  prevents a toast for the pane being looked at.

### Focus gating

`View()` sets `v.ReportFocus = true`; `Update` gains `tea.FocusMsg` /
`tea.BlurMsg` arms writing `m.termFocused`.

**The failure mode that matters is a terminal without DEC 1004 support**, where
neither message ever arrives and `m.termFocused` keeps its initial value
forever. Both defaults are bad in different ways: initialise `true` and the
feature silently never fires; initialise `false` and it toast-storms the user
while they are looking at the screen.

Resolution: initialise `true` (never toast) **plus explicit detection**. A
`focusEverReported bool`, a WARN to the log if no focus event has arrived some
seconds after launch, and `quil notify status` reporting it in plain words. That
converts a silent no-op into a diagnosable one. `require_blur = false` is the
config escape hatch for anyone who wants toasts regardless.

### Cooldown

`lastToastAt time.Time` on `PaneModel`, 30 s, **one cooldown per pane shared by
both event kinds** — matching the existing `LastBellEventAt` convention.

Shared rather than per-kind deliberately: a pane that blocks and then completes
five seconds later should produce one toast, not two.

A still-blocked pane never re-toasts, because suppression keys on *edges* and a
pane that stays blocked produces no new edge.

## Data flow

```
claude hook → spool → daemon → PaneEvent → IPC → TUI
                                                  │
                                    applyWorkTransition
                                    (before/after pair)
                                                  │
                                       rising edge only
                                       blocked / done
                                                  │
                gates: enabled? kind on?
                  blurred? cooldown passed?
                            │
                    Notify(Notification{
                      Title:  "quil · claude-code"
                      Body:   "▲ Waiting for your input"
                      Tag:    paneID
                      Launch: "quil://activate?pid=…&pane=…"
                    })
                            │
                       Windows toast
                            │  ← user clicks
              quil.exe activate "quil://activate?pid=…&pane=…"
                            │
              \\.\pipe\quil-activate-<pid>   (handler exits immediately)
                            │
                  p.Send(activatePaneMsg{PaneID})
                            │
                    m.jumpToPane(paneID)
```

Withdrawal runs on the other side, independently of this path:

```
every Update  →  sweepOutstandingToasts()
                 (returns immediately if the set is empty)
                        │
          pane gone, or no longer blocked and no longer unseen
                        │
                  Withdraw(tag)  →  clears Action Center
```

`jumpToPane` (`workstate.go:89`) is called **unmodified**. It already performs
notes-mode teardown before the tab moves, the project switch, the
`sidebarScroll` reset ordering, `syncActiveDest`, `notifyTabSwitch` for lazily
restored tabs, and the overlay transition report — roughly fifty lines of
constraints each of which was a bug first. Reimplementing any of it in an
activation handler would reintroduce them.

### Why the pipe rather than the daemon

Considered and rejected: route the click through the daemon as a new
`MsgActivatePane`, reusing `internal/ipc` wholesale.

- In remote mode it sends a click that happened on the user's laptop across SSH
  to a server and back, to move a cursor that never left the local machine.
- A daemon broadcast reaches every attached client, so all of them would jump.
  Fixing that needs client targeting — which is the PID already in the URI.
- It costs a protocol bump for a message no other client needs.

Also rejected: a file drop polled by the TUI. It adds latency to a click — the
one interaction where latency reads as brokenness — plus stale-file cleanup and
a read/delete race.

The pipe approach additionally degrades correctly: if the TUI exited between
toast and click, the pipe is gone and `quil activate` fails to connect and exits
silently. Routing through the daemon would accept and broadcast the activation
into the void.

## Trust boundary

Project and pane names go into the toast XML, and they arrive from a daemon that
may be remote — outside the user's trust per the Phase 3 boundary documented in
`.claude/rules/remote-dialogs.md`.

The toast body requires **all three**, in this order, in the neutral
`toastxml.go` where a Linux test can reach them:

1. `sanitizeRemoteText` — drops C0/DEL/C1 (including U+009B, the C1 CSI
   introducer) and bidi overrides.
2. XML escaping — `&`, `<`, `>`, `"`.
3. A length bound.

These are three different threats, not redundancy. XML escaping stops `&`/`<`
from breaking the document but leaves `U+202E` untouched, because it is
*printable* — it would silently reverse the pane name in the toast. This is the
same defect shape as the OSC 52 leak the sidebar shipped, where a width check
was mistaken for a sanitiser. And sanitizing does not shorten anything: a
megabyte of ordinary printable text survives it whole, so the bound is a third,
separate concern.

## Security: what registering `quil://` exposes

A registered URI scheme is invokable by **any local process, and by any web page
behind a browser confirmation**. The capability must therefore be capped by
construction, not by intent.

- `quil activate` does exactly one thing: parse, validate, write a pane ID to
  the pipe. There is no code path from the URI to spawning a pane, sending
  input, or running a command.
- The pane ID is validated on **both** sides — in the handler and again in the
  TUI on receipt — because the pipe is independently reachable by any same-user
  process.
- The pipe carries a pane ID and nothing else. It is not a command channel.
- The named pipe ACL is set explicitly to current-user-only rather than relying
  on the default.

Worst case for a fully hostile caller: the cursor moves to a pane the user
already owns.

That ceiling is a design constraint, and it is why **toast action buttons**
(inline "Approve" / "Deny") are out of scope despite being the obvious next
feature. They would require exactly the input-sending capability this boundary
exists to withhold — and an AI pane is a process that acts on what it reads.

## Configuration

```toml
[notification.desktop]
enabled      = true    # master switch, default ON
blocked      = true    # ▲ toasts
done         = true    # ✓ toasts
cooldown     = "30s"   # per pane, shared by both kinds
require_blur = true    # only toast when the terminal is unfocused
```

Nests under the existing `NotificationConfig`, so `[notification.hooks]` gains a
sibling rather than the config growing a new top-level table.

**`enabled` defaults TRUE and that does not conflict with the explicit-setup
decision.** The flag expresses intent; the Start Menu shortcut and `HKCU` key
remain the gate, and nothing writes them as a side effect of a flag being set.
The consequence is that *"enabled but not registered"* is the DEFAULT state on a
fresh Windows install rather than an edge case — which is what forces the
Settings row below to report state instead of the flag.

### There is exactly one authority for these values

`raiseAttentionToast` reads `m.cfg.Notification.Desktop` directly. The Model
keeps no second copy.

An earlier draft cached the struct on the Model, which would make the Settings
toggle a dual write — and two copies of one value with independent writers is
the shape behind both the `applyBrowseDir` staleness bug and the
`Pane.PinnedAttention` local-write bug, where the repo's answer each time was
to leave exactly one authority. `m.cfg` is already that authority, because
every other Settings setter writes it. Reading it directly is also what makes
the toggle apply **live by construction** rather than by remembering to sync.

## Settings dialog row

`settingsFields()` (`internal/tui/dialog.go:191`) is a declarative table of
`{label, get, set, isBool}` rows, so this is one entry.

The row is **tri-state, reporting what the system is actually doing**:

| Rendered value | State |
|---|---|
| `on` | enabled and registered — toasts will fire |
| `on (run notify setup)` | enabled, no registration — nothing will fire yet |
| `off` | disabled |
| `unsupported` | not Windows; the row is inert |

A plain boolean would render `on` for the commonest first-run state on Windows,
where nothing happens — the same defect the Sidebar-width setter's own comment
refuses: *the dialog must never show a value the layout is not using.*

**The row does not perform registration.** Enter on `on (run notify setup)`
names the command and nothing else. Registering from a config toggle is exactly
the auto-register behaviour rejected in decision 2, reached through a different
door.

**Live, unlike most Settings rows.** `settingsFields`' documented default is
that changes take effect on the next launch. The two existing exceptions —
sidebar width and overlay retention — are both cases where a visible control
that did nothing until relaunch would read as broken, and an on/off switch for
notifications is squarely in that category. Reading `m.cfg` directly is what
delivers this; there is no separate apply step.

The setter sets `m.configChanged = true`, per the rule every Settings setter
follows, or the edit is silently lost on exit.

## Commands

All Windows-only; elsewhere they return `ErrUnsupported` and exit 1 with a
plain sentence.

- `quil notify setup` — writes both artifacts, then prints exactly what it
  wrote and the undo command. No silent success.
- `quil notify setup --remove` — deletes both. A true inverse, pinned by a
  round-trip test.
- `quil notify status` — reports registration state, focus-reporting detection,
  and the config switch. This is the diagnosis path for "why don't I see
  toasts", and the reason the command is a triple rather than a single verb.

## Error handling

Every failure degrades; none blocks.

| Failure | Behaviour |
|---|---|
| Not Windows | `notify.New()` returns nil; every emit is a no-op. No error, no log spam. |
| `enabled = true`, setup never run | DEBUG log only. With `enabled` defaulting true this is the state of every fresh Windows install, so a WARN would be noise on a machine whose owner never wanted toasts. The **Settings row** is the discovery surface — it says `on (run notify setup)` — with `quil notify status` as the CLI equivalent. Never a dialog: a dialog about notifications is the wrong medicine. |
| Toast emit fails | Debug log, no retry. The sidebar event already landed; a retried toast is worse than a missing one. |
| Pipe will not bind | WARN, and **toast anyway**. The primary value is "an agent needs you"; routing is the bonus. Disabling the feature over a pipe failure is the larger silent loss. |
| Click arrives, TUI has exited | `quil activate` exits 0 silently. A stale toast must never pop an error window. |
| Malformed URI | Reject, non-zero exit, nothing user-visible. |

## Testing

### Runs in CI (`dev.sh test`, Linux container)

- URI build/parse round-trip; malformed-input rejection table
- Toast XML golden tests: `<` / `&` / `"` escaping, `U+202E` bidi override, C1
  introducer, oversized input
- Edge detection driven **through `Update`**, not by calling
  `applyWorkTransition` directly — a direct-call test can pass against code the
  call site makes unreachable
- Cooldown and focus gate as table tests with an injected clock and a recording
  fake notifier
- **Withdrawal sweep**, driven through `Update` for each of the four clear
  paths — typing an answer (`answerBlockedByInput`), focusing the pane
  (`ackFocusedPane`), the "Clear attention" menu row, and pane destruction.
  The typing path is the regression test that matters: it fires no hook, so it
  is the one an edge-based implementation silently misses.

### Native Windows only

Built with `go test -c` in Docker and run as an `.exe` on the host, per the
established workaround for having no local Go toolchain.

- Registry write / read / remove round-trip against a scratch key
- `.lnk` creation with AUMID property readback
- Pipe listen → connect → parse round-trip
- Manual smoke: real toast, real click

## Out of scope

- **Raising the terminal window on click** — agreed follow-up task. The
  mechanism is a process-tree walk to an ancestor `WindowsTerminal.exe` plus
  `EnumWindows` PID matching, because `GetConsoleWindow()` returns a ConPTY
  ghost window under Windows Terminal. It also has a ceiling nothing can lift:
  WT hosts many tabs in one window and exposes no API to select one, so
  "raised" means the window, not necessarily the Quil tab.
- **macOS / Linux transports** (OSC 9, `notify-send`, `terminal-notifier`).
  None supports click routing, so they are a separate, lesser feature.
- **Sound notifications** — M17 item 13, its own work.
- **Toast action buttons** — refused on the security grounds above, not
  deferred.

## Success criteria

1. With setup run, parking an agent while the terminal is unfocused raises a
   Windows toast naming the project and pane — with no config edit, since
   `enabled` defaults true.
2. Clicking that toast leaves Quil on that exact project, tab and pane.
3. With the terminal focused, no toast fires.
4. Six panes changing state at once produce six independently-routable toasts,
   with at most one per pane per 30 s.
5. Answering a prompt withdraws its toast from Action Center.
6. `quil notify setup --remove` leaves the machine as it was before setup.
7. A dev build's registration does not disturb a production one.
8. `dev.sh test` covers the XML, URI, edge, cooldown and gating logic on Linux.
9. F1 → Settings shows `Desktop notifications`; toggling it stops and starts
   toasts **without a relaunch**, and the setting survives exit.
10. On a Windows machine that has never run setup, the row reads
    `on (run notify setup)` rather than `on`.
