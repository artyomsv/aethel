---
name: project_ingest_arrival_order_gaps
description: hookevents Ingester arrival-order queue (PR #199) — Cancel's order-list removal is mutation-survivable; the TUI's subagent-StopFailure branch keys on the CLASSIFIED KIND, not the event type
type: project
---

`internal/hookevents/ingest.go` gained an `order []string` arrival queue plus a
per-entry `due` flag so coalesced events emit in first-arrival order across
keys (fixing per-key `time.AfterFunc` emitting LIFO). Two gaps found by
mutation on a scratchpad copy, 2026-09-04, branch `fix/progress-indicator-2`:

**1. `Cancel` removing keys from `order` is uncovered.** Rewriting `Cancel` to
delete from `pending` and stop timers but leave `order` untouched left the
whole `internal/hookevents` suite green. It is a real bug, not tidiness: a key
resubmitted for the same pane after a Cancel finds its STALE slot still at the
front of the queue and jumps ahead of keys that arrived in between. A probe
(`Submit A; Cancel(pane); Submit B; Submit A`, expecting emit order `B,A`)
fails on the mutant and passes on HEAD.

**2. The TUI's subagent branch keys on the kind, not the event type.**
`internal/tui/workstate.go`'s `if kind == workStop && data["agent_type"] != ""`
fires for ALL FOUR event types `ClassifyWorkEvent` maps to `WorkEventStop`:
`hook.claude.Stop`, `hook.claude.StopFailure`, `hook.opencode.session.idle`,
`hook.opencode.session.error`. Measured on HEAD: a `hook.claude.Stop` carrying
`agent_type` drains the agent and leaves `turnActive` TRUE forever — the exact
wrong-on wedge the change exists to remove. Latent only because the sole
`Ingester.Submit` caller is the spool watcher and no producer sets `agent_type`
on those three; nothing pins that.

**Why:** both are "the guard is set correctly, the thing it guards is never
driven" — the same shape as the gate-boolean and requestSnapshot gaps in
[[project_test_tooling]].
**How to apply:** when a change adds an ordering queue with a separate removal
path (Cancel/prune/evict), mutate the removal to a no-op and re-run — a green
suite means only the happy path is pinned. When a consumer branches on a
CLASSIFIED kind rather than a raw event type, enumerate every type that maps to
that kind and check the branch is correct for all of them, not just the one the
PR is about.
