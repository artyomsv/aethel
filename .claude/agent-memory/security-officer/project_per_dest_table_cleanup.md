---
name: per-dest-table-cleanup
description: Any new per-destination map on tui.Model must be deleted in disconnectDest (internal/tui/dialdest.go); PR #198's destAvail missed it
metadata:
  type: project
---

When a change adds a `map[string]...` keyed by destination to `tui.Model`, check that `disconnectDest` (`internal/tui/dialdest.go`) deletes it beside `redialFns`, `attached`, `links`, `updateInfos`, `installedDests`, `offlineWoken`, `cachedRemote`.

**Why:** that function states the contract in its own comment ("every other per-destination table goes with them"), yet `destAvail` (PR #198, 2026-09-03, per-destination plugin availability) was added without a delete — a host the user told the client to forget kept answering for itself, and nothing else in the repo enforces the list.

**How to apply:** grep `map\[string\]` additions in `internal/tui/model.go` on any remote/router PR, then grep the new field in `dialdest.go`. Also check the ipc `Origin` stamping story: `Router.pump` (`router.go`) overwrites `msg.Origin` on every received frame and `Origin` is `json:"-"`, so a per-dest bucket keyed by Origin cannot be chosen by the wire — that part needs no re-review. See [[remote-daemon-string-taint]] for the render-side rule (remote strings used only as map keys never reach `sanitizeRemoteText` and need not).
