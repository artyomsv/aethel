---
name: project_ingest_arrival_order_gaps
description: hookevents Ingester arrival-order queue (PR #199) — BOTH gaps RESOLVED in-branch and pinned by tests; keep the method: mutate a removal path to a no-op, and enumerate every type a CLASSIFIED kind covers
type: project
---

`internal/hookevents/ingest.go` gained an `order []string` arrival queue plus a
per-entry `due` flag so coalesced events emit in first-arrival order across
keys (fixing per-key `time.AfterFunc` emitting LIFO). Two gaps found by
mutation on a scratchpad copy, 2026-09-04, branch `fix/progress-indicator-2`.

**STATUS: both gaps are CLOSED — do not go hunting for either defect.** Both
were fixed and pinned before PR #199 merged. The findings are kept below
because the METHOD that found them is the reusable part; the defects are not
present on master. Verified 2026-09-06.

**1. RESOLVED — `Cancel` removing keys from `order` was uncovered.** Now pinned
by `TestIngester_Cancel_ResubmittedKeyTakesAFreshSlot`
(`internal/hookevents/ingest_order_test.go`), which runs the exact probe below
20 times. Original finding: rewriting `Cancel` to
delete from `pending` and stop timers but leave `order` untouched left the
whole `internal/hookevents` suite green. It is a real bug, not tidiness: a key
resubmitted for the same pane after a Cancel finds its STALE slot still at the
front of the queue and jumps ahead of keys that arrived in between. A probe
(`Submit A; Cancel(pane); Submit B; Submit A`, expecting emit order `B,A`)
fails on the mutant and passes on HEAD.

**2. RESOLVED — the TUI's subagent branch keyed on the kind, not the event
type.** `internal/tui/workstate.go:389` now reads
`if eventType == subagentFailureEvent && data["agent_type"] != ""`, matching the
RAW type (`hook.claude.StopFailure`) instead of the classified kind, with the
reasoning in the comment above it and in the `subagentFailureEvent` doc comment.
Pinned by `internal/tui/workstate_subagent_failure_test.go`. Original finding:
`if kind == workStop && data["agent_type"] != ""` fired for EVERY event type
`ClassifyWorkEvent` maps to `WorkEventStop`, not just the one the PR was about.
As of 2026-09-06 that is SIX types — re-read `ClassifyWorkEvent` rather than
trusting this list, because it has grown twice already:

| Type | Note |
|---|---|
| `hook.claude.Stop` | the realistic one; measured below |
| `hook.claude.StopFailure` | the only type the branch is meant to catch |
| `hook.codex.Stop` | added later, by the codex plugin (PR #201) |
| `hook.opencode.session.idle` | no producer sets `agent_type` |
| `hook.opencode.session.error` | no producer sets `agent_type` |
| `internal.user_interrupt` | TUI-synthesised from ESC; never crosses IPC |

The note first said FOUR, which was the count on the pre-fix branch tip; review
caught it at five and the sixth turned up on re-reading. That miscount is itself
the lesson — a kind is a moving target and a hand-copied enumeration rots.

Measured against the pre-fix branch tip: a `hook.claude.Stop` carrying
`agent_type` drained the agent and left `turnActive` TRUE forever — the exact
wrong-on wedge the change exists to remove. It was latent only because the sole
`Ingester.Submit` caller is the spool watcher and no producer sets `agent_type`
on the others, and nothing pinned that.

**Why:** both are "the guard is set correctly, the thing it guards is never
driven" — the same shape as the gate-boolean and requestSnapshot gaps in
[[project_test_tooling]].
**How to apply:** when a change adds an ordering queue with a separate removal
path (Cancel/prune/evict), mutate the removal to a no-op and re-run — a green
suite means only the happy path is pinned. When a consumer branches on a
CLASSIFIED kind rather than a raw event type, enumerate every type that maps to
that kind and check the branch is correct for all of them, not just the one the
PR is about — and read the classifier to build that list every time. A copied
enumeration rots the moment a plugin adds a type, and it rots SILENTLY: the
branch keeps compiling and the suite stays green.
