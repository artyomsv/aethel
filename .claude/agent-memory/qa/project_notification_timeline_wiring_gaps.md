---
name: notification-timeline-wiring-gaps
description: PR #204 (feat/notification-events) — 15 mutation-verified gaps where emitters/filters are tested directly but their call sites are unpinned; the whole sidebar filter can be disconnected in NewModel with a green suite
metadata:
  type: project
---

PR #204 reworked the notification sidebar into a filtered timeline. Round-1 QA
(ref `e70e7da`) found the same shape of gap as
[[project_notify_wiring_gap]] and [[project_ipc_write_window_wiring_gap]]:
every new unit is tested by DIRECT CALL, and every CALL SITE is unpinned.

**Why:** 15 mutations applied to a clean `git archive HEAD` export left
`internal/config`, `internal/tui` and `internal/daemon` fully green. The
highest-value one: deleting
`m.notifications.SetGroups(groupFilterFrom(cfg.Notification.Events))` from
`NewModel` (internal/tui/model.go:1066) makes the entire feature inert — a nil
filter shows everything — and nothing goes red, because every filter test
builds a `NotificationCenter` or a `Model` struct literal and calls
`SetGroups` itself. The four daemon emitters (`notifyMCPControl`,
`notifyPaneMark`, `notifyPaneDestroyed`, `notifyWorktreeReady`) can each be
severed from their handlers, and the `changed :=` change-detection in
`handleUpdatePane` can be replaced with `true`, all green.

**How to apply:** on any future round of this branch — and on any daemon event
emitter added later — check the HANDLER, not the emitter. The daemon package
has no test that drives `handlePaneInput` / `handleUpdatePane` /
`handleDestroyPaneReq` / `worktreeAddAndCreate` and asserts on
`d.events.Count()`. One such test per call site closes the whole class.

Also note the tree went DIRTY mid-review (the author was still committing;
three `fix(...)` commits landed while round 1 ran). Pin to a
`git archive <sha> | tar -x -C <scratchpad>` export before running or mutating,
or the baseline moves under the probe.
