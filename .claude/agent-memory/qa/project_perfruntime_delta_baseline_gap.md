---
name: project-perfruntime-delta-baseline-gap
description: PR #205 (fix/profile-slowness) — flush()'s `s.lastRuntime = rt` baseline advance and histogramTotal's +Inf bucket fallback both survive mutation; the wiring test only ever flushes once
metadata:
  type: project
---

On branch `fix/profile-slowness` (commit db43093), two lines in the new runtime-accounting
code are mutation-removable with a fully green `internal/tui` suite:

1. `internal/tui/perf.go:231` — `s.lastRuntime = rt`. Replacing it with `_ = rt` passes every
   test. `TestFlushEmitsRuntimeSection` flushes exactly ONCE, so it pins "flush uses lastRuntime
   as this window's baseline" (mutating the call to `formatRuntimeDelta(rt, rt, window)` IS
   killed) but never "flush advances the baseline for the next window".
2. `internal/tui/perfruntime.go:131` — the `upper > 1e18 || upper != upper` +Inf/NaN last-bucket
   fallback in `histogramTotal`. Replacing the condition with `false` passes every test. The
   branch is unreachable from the constructed-sample tests and the real runtime histogram's
   overflow bucket is normally empty.

**Why:** both are "the log reports a cumulative total / an overflowed garbage duration instead of
this window's delta" failures — exactly the class of wrongness the file's own doc comment says the
design exists to prevent, and invisible in production because the number still looks like a number.

**How to apply:** a second `s.flush()` in the wiring test, and a direct `histogramTotal` table test
with a `+Inf` final bucket, close both. Same shape as [[project-ipc-write-window-wiring-gap]] —
a one-shot test proves the read of a piece of state but never its write-back.
