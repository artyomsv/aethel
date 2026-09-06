---
name: go-cpu-class-metrics-stale
description: Go runtime/metrics /cpu/classes/* only advance at GC mark termination — a window with no GC cycle reads a 0 delta, and the next GC window reports every preceding window's CPU at once
metadata:
  type: reference
---

`runtime/metrics` `/cpu/classes/*` (total, idle, gc/mark/assist, and every sibling)
are NOT live counters. They are a snapshot taken at GC mark termination.

Verified against `golang:1.25-alpine` source and by probe:

- `runtime/metrics.go` `cpuStatsAggregate.compute()` does only `a.cpuStats = work.cpuStats`.
  The line that would refresh it is commented out with a `TODO(mknyszek)` saying it
  "will cause non-monotonicity in the user CPU time metric".
- `work.cpuStats.accumulate(...)` has exactly ONE call site: `mgc.go:1128`, inside
  `gcMarkTermination`.
- Probe (28 GOMAXPROCS, 4 goroutines burning 3 s wall):
  - GC off  → `cpu-used delta 0.000s`, 0 cycles
  - GC on   → `cpu-used delta 24.042s`, 1 cycle (both arms' CPU attributed at once)

Same class of staleness: `/gc/heap/live:bytes` is `gcController.heapMarked` —
"marked by the PREVIOUS GC", so it is flat between cycles.

Live and safe to delta per-window: `/gc/cycles/total:gc-cycles` (sysStatsDep) and
`/gc/pauses:seconds` (`sched.stwTotalTimeGC`, written at every STW).

**Why:** any per-interval "CPU used" built on `total - idle` reads 0 for most
intervals on a low-GC process, and 0 reads as "the process was off the CPU" —
the opposite of the truth. Found in quil PR #205 (`internal/tui/perfruntime.go`).

**How to apply:** for real per-window process CPU use the OS instead —
`syscall.GetProcessTimes` + `syscall.GetCurrentProcess` on Windows,
`syscall.Getrusage(RUSAGE_SELF)` elsewhere. Both are Go stdlib, no CGo, no new
dependency. Probe method: standalone `go run` in the golang container, no shared
build cache. See [[verify-claims-in-container]].
