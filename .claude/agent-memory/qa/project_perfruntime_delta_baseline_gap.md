---
name: project-perfruntime-delta-baseline-gap
description: A one-shot wiring test proves flush() READS its delta baseline but never that it ADVANCES it — found by mutation on PR #205, both gaps now closed; reuse the shape, do not reopen the gaps
metadata:
  type: project
---

**Status: both gaps are CLOSED** (PR #205, merged as `c432a65`, v1.68.2). Kept for the test
SHAPE, which generalises; do not re-report these two as open.

Mutation testing on `fix/profile-slowness` found two lines in the runtime-accounting code that
could be removed with a fully green `internal/tui` suite:

1. `internal/tui/perf.go` — `s.lastRuntime = rt`. Replacing it with `_ = rt` passed every test.
   `TestFlushEmitsRuntimeSection` flushed exactly ONCE, so it pinned "flush uses lastRuntime as
   this window's baseline" (mutating the call to `formatRuntimeDelta(rt, rt, window)` WAS
   killed) but never "flush advances the baseline for the next window".
   **Closed by** `TestEventLoopStats_Flush_EmitsRuntimeSection`, which now flushes twice and
   asserts the second line reads `gc=1` and `cpu=1s/` rather than the running totals.
2. `internal/tui/perfruntime.go` — the `+Inf`/NaN last-bucket fallback in `histogramTotal`.
   Replacing the condition with `false` passed every test: the branch is unreachable from
   constructed-sample tests and the real runtime's overflow bucket is normally empty.
   **Closed by** `TestHistogramTotal_InfiniteLastBucket_UsesFiniteBound`, plus
   `TestDeltaPeak_...` for the harness copy of the same clamp.

Both mutations were re-run after the fixes and both now FAIL, which is the only thing that
makes "closed" mean anything.

**Why it matters:** both were "the log reports a cumulative total, or an overflowed garbage
duration, instead of this window's delta" — exactly the wrongness the design exists to prevent,
and invisible in production because the number still looks like a number.

**How to apply — the reusable shape:** a one-shot test proves the READ of a piece of state but
never its WRITE-BACK. Any test that exercises a stateful step exactly once has this hole. Drive
it twice and assert the second result differs. Same shape as
[[project-ipc-write-window-wiring-gap]].
