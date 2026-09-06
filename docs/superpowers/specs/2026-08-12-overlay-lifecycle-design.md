# Overlay lifecycle — idle eviction and a live cap

**Date:** 2026-08-12
**Status:** approved, ready for planning

## Problem

A lazygit overlay (`Alt+G`) is never reclaimed. Closing it only sets the TUI's
`overlayVisible = false`; the daemon pane and its lazygit process keep running.
The three existing destroy paths are all conditioned on something else
happening — opening a *different* repo in that tab (`overlay.go:154`), the
process exiting on its own (`daemon.go:2321`), or the tab losing its last
normal pane (`ensureTabNotEmpty`, `daemon.go:1897`). None is triggered by the
user's own "close" gesture, so the one action meaning "I am done with this" is
the only one that reclaims nothing.

Measured on the reporter's live daemon (2026-08-11): **7 lazygit panes, all
`running: true`**, one per tab that had ever opened the overlay — 116.1 MB RSS
for the one inspected, and a ceiling of ~3.8 GB across 33 tabs.

A second, independent defect from the same investigation: the memory report
lists an invisible overlay identically to a pane the user is looking at, which
is why a tab with one visible pane reported two. That is a reporting fix and is
**out of scope here** — this spec covers retention only.

## Non-goals

- **Pane → overlay destroy.** `ensureTabNotEmpty` already reclaims the overlay
  with the tab's last normal pane; extending it to every pane close was
  considered and declined.
- **Labelling overlays in the memory report.** Separate change.
- **Overlay persistence across a daemon restart.** Unverified; if overlays are
  respawned at restart, the timer and cap reclaim them within the timeout
  anyway, which removes the urgency. To be checked and reported separately.

## Design

### Ownership

The **daemon** owns the policy and the timer. It owns pane lifetime, and it
keeps running when no TUI is attached — which is exactly when reclaiming is
most wanted.

The daemon does not know whether an overlay is on screen: `overlayVisible` is
TUI state. So the TUI reports it.

### Visibility signal

`UpdatePanePayload` gains `OverlayVisible *bool`.

That message is already the partial-update carrier for `Muted *bool` and
`PinnedAttention *bool`, and `handleUpdatePane` already treats a nil pointer as
"unchanged" — which is the property that matters, since a plain bool would
clear the flag on every rename and every OSC 7 CWD change. No new message type.

`showOverlay` sends `true`; the hide path sends `false`.

### Daemon state

Two `PluginMu`-protected fields on `Pane`:

- `OverlayHiddenAt time.Time` — zero means currently visible.
- `OverlayShownAt time.Time` — last time it was shown; drives LRU.

**No client attached ⇒ every overlay is hidden.** On last-client disconnect the
daemon stamps `OverlayHiddenAt` on every overlay that lacks one. Nothing can be
displaying an overlay when nothing is attached, and this is what makes the
timer fire while the user is away.

### Idle eviction

Swept from the existing 1 s `idleChecker` goroutine rather than a new ticker.
An overlay whose `OverlayHiddenAt` is non-zero and older than
`idle_timeout_minutes` is destroyed through the ordinary pane-destroy path
(`DestroyPane` → `releasePanes` off-lock, `cleanupPaneArtifacts`), so it emits
the same broadcast and snapshot as any other destroy and the TUI reconciles it
with machinery that already exists.

A **visible** overlay is never evicted, however long it sits idle: lazygit
emits nothing while you read it, so an activity-based rule would kill the pane
you are looking at. That is the whole reason visibility is reported rather than
inferred.

### LRU cap

Enforced at **creation**, in `handleCreatePane`'s overlay branch: if admitting
the new overlay would exceed `max_live`, evict the least-recently-shown overlay
(tie-break: oldest created) until there is room.

At creation rather than in the sweep so the 6th open is instant and
deterministic instead of "eventually". Least-recently-*shown* rather than
oldest-created because strict FIFO evicts the overlay you use constantly simply
because you opened it first.

Eviction crosses tabs — the cap is global, since it exists to bound total
process count rather than per-tab tidiness.

### Configuration

```toml
[overlay]
idle_timeout_minutes = 5
max_live = 5
```

A new `[overlay]` section, matching the per-feature precedent of
`[ghost_buffer]` and `[notification]`. `0` disables either policy
independently. The daemon reads this at startup, so a daemon with no TUI still
evicts.

F1 → Settings gets two numeric rows, edited like the existing
`sidebar_width` / `mouse_scroll_lines` rows.

**Settings edits must also reach the running daemon.** Every Settings setter
only flips `m.configChanged`, and `cmd/quil/main.go` writes the file on TUI
exit — so a daemon-side policy read from disk would not change until the next
daemon start. The TUI therefore pushes the values with a small
`MsgOverlayPolicy{IdleTimeoutMinutes, MaxLive}` once after attach and on every
change. Immediate effect, identical behaviour against a remote daemon, and
last-writer-wins if several clients disagree (documented, not defended
against).

## Testing

**Daemon**

- An overlay hidden past the timeout is destroyed.
- A **visible** overlay is never destroyed, however long it idles. (The case an
  activity-based implementation would get wrong.)
- Last-client disconnect stamps hidden on overlays that lack it.
- The cap evicts the least-recently-**shown**, not the oldest created — the
  case where LRU and FIFO disagree, so the test fails against a FIFO
  implementation.
- `idle_timeout_minutes = 0` and `max_live = 0` each disable their own policy
  without disabling the other.
- A non-overlay pane is never touched by either policy.

**TUI**

- Show and hide each send `OverlayVisible` with the right value.
- The two Settings rows edit, flag `configChanged`, and push the policy.

**Config**

- Defaults are 5 and 5; TOML round-trips; absent section yields the defaults
  rather than zeros (which would silently disable both policies).

## Risks

- **A shared 1 s sweep touching pane state** must take `PluginMu` per pane and
  must not hold `sm.mu` across `DestroyPane`, per the daemon's existing lock
  discipline (a parked writer starves every reader).
- **Evicting an overlay the TUI still believes in** — handled by the existing
  reconcile path (`overlay_reconcile_test.go`), which the destroy broadcast
  already drives; worth an explicit test rather than an assumption.
