package tui

import (
	"fmt"
	"runtime"
	"runtime/metrics"
	"time"
)

// Runtime accounting for the perf summary.
//
// The perf log could already say how long a frame took. It could not say WHY a
// frame that did the same work took fourteen times longer, and that gap cost a
// whole investigation: three separate hypotheses (GC pressure, a large live VT
// heap, an expensive renderer state) each had to be reproduced from scratch in
// a benchmark before they could be ruled out, because the log held no evidence
// that could distinguish them.
//
// The number that separates the remaining explanations is CPU TIME USED per
// window, read against the time the same window spent inside View and Update.
//
// **The comparison is ASYMMETRIC, and saying so is load-bearing.** `cpu` is
// PROCESS-wide — every goroutine, including the IPC reader, Bubble Tea's own
// renderer and input goroutines, and any `tea.Cmd` in flight — while View+Update
// is wall time on the event-loop goroutine alone. Go exposes no per-goroutine
// CPU counter, and the event-loop goroutine is not pinned to an OS thread, so
// GetThreadTimes/RUSAGE_THREAD cannot stand in for one without a
// runtime.LockOSThread this diagnostic has no business adding. What survives
// that mismatch is one direction of the inference, not both:
//
//   - cpu ≪ time in View+Update → CONCLUSIVE. Process CPU is an upper bound on
//     any single goroutine's CPU, so if the WHOLE process used far less CPU than
//     the event loop spent inside View and Update, the event loop cannot have
//     been executing for most of that wall time. Look outside quil: descheduled,
//     throttled, or blocked.
//   - cpu ≥ time in View+Update → NOT conclusive on its own. Busy background
//     goroutines lift the figure whether or not the event loop was running. Read
//     it beside gor= and the per-message table rather than as proof that the
//     work itself got more expensive.
//
// The useful direction is the conclusive one, which is why the field is worth
// having: the open question about the 2026-09 stall is precisely whether the
// process was running.
//
// PER WINDOW, not per frame, and not by choice. Windows accrues process times
// against the scheduler tick (~15.6 ms by default), so a 3 ms frame measures as
// 0 or as one whole tick. Over five seconds the quantisation averages out; over
// one frame it is pure noise, and the two extra syscalls would sit in the render
// hot path to produce it.
//
// Everything else here is context for that number and costs one sampling call
// per five seconds: goroutine count (a leak slows every scheduling decision
// uniformly, which looks exactly like the case above), live heap, and the three
// GC figures whose absence made the GC hypothesis take a benchmark to falsify.
//
// **CPU comes from the OS, not from runtime/metrics, and that is the whole
// point of this file working at all.** The obvious implementation reads
// `/cpu/classes/total` minus `/cpu/classes/idle`. Those are NOT live counters:
// `cpuStatsAggregate.compute` copies a snapshot that `work.cpuStats.accumulate`
// only ever writes from `gcMarkTermination`, so the values advance when a GC
// CYCLE COMPLETES and at no other time. Measured on Go 1.25, four goroutines
// burning CPU for three seconds:
//
//	GC OFF : wall 3s  cpu-used delta 0.000s   gc cycles 0
//	GC ON  : wall 3s  cpu-used delta 40.793s  gc cycles 2207
//
// A TUI holding a ~1 GB live heap and allocating ~350 KB per frame at ~5 fps
// completes a cycle roughly every nine minutes, so about ninety-nine windows in
// a hundred would have printed `cpu=0s/5s` — which the reading rule above calls
// "the process was OFF the CPU", the exact opposite of the truth — and the
// hundredth would print the accumulated CPU of all the others. The instrument
// would have aimed the next investigation away from the answer, confidently.
// getrusage(2) and GetProcessTimes both advance continuously; see perfcpu_unix.go
// and perfcpu_windows.go.
//
// runtime/metrics rather than runtime.ReadMemStats for the rest, deliberately:
// ReadMemStats stops the world, and instrumentation that pauses the program is
// a poor way to investigate pauses. The one exception is NumGoroutine, a plain
// counter read.

// runtimeMetricNames are the metrics sampled at each flush.
//
// Only the two live counters and the two GC-published levels are here. The
// /cpu/classes/* family is deliberately ABSENT — see the CPU note above for the
// measurement showing why reading it per window would be worse than reading
// nothing.
var runtimeMetricNames = []string{
	"/gc/cycles/total:gc-cycles",
	"/gc/pauses:seconds",
	"/cpu/classes/gc/mark/assist:cpu-seconds",
	"/gc/heap/live:bytes",
}

// runtimeSample is one reading. The have* flags exist because a metric this Go
// build does not supply comes back as KindBad, and rendering that as 0 would
// print a confident "no assist time" for a figure that was never read — the
// exact failure this whole file exists to prevent.
type runtimeSample struct {
	// cpuUsed is user+system CPU consumed by the whole process since it
	// started, read from the OS.
	cpuUsed time.Duration

	gcCycles   uint64
	gcAssist   float64
	gcPause    time.Duration
	heapLive   uint64
	goroutines int

	haveCPU    bool
	haveCycles bool
	havePause  bool
	haveAssist bool
	haveLive   bool
}

// readRuntimeSample takes one reading.
//
// A package var so tests can drive formatRuntimeDelta with constructed samples
// instead of whatever the test binary's own runtime happens to be doing.
var readRuntimeSample = func() runtimeSample {
	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, n := range runtimeMetricNames {
		samples[i].Name = n
	}
	metrics.Read(samples)

	s := runtimeSample{goroutines: runtime.NumGoroutine()}
	s.cpuUsed, s.haveCPU = processCPU()

	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindBad {
			continue
		}
		switch sample.Name {
		case "/gc/cycles/total:gc-cycles":
			s.gcCycles, s.haveCycles = sample.Value.Uint64(), true
		case "/cpu/classes/gc/mark/assist:cpu-seconds":
			s.gcAssist, s.haveAssist = sample.Value.Float64(), true
		case "/gc/heap/live:bytes":
			s.heapLive, s.haveLive = sample.Value.Uint64(), true
		case "/gc/pauses:seconds":
			s.gcPause, s.havePause = histogramTotal(sample.Value.Float64Histogram()), true
		}
	}
	return s
}

// histogramTotal sums a runtime/metrics duration histogram.
//
// Each bucket contributes its UPPER bound, so the total over-estimates by at
// most one bucket width. Two reasons that is the right direction. First, this
// figure is read to decide whether GC pauses are large enough to matter, and an
// under-estimate would answer "no" for the wrong reason. Second, and more
// usefully: what the perf line prints is a DELTA of two of these totals, and
// both carry the same systematic over-estimate on the same historical samples,
// so the bias cancels and only the window's new samples are approximated at all.
//
// The last bucket's upper bound is +Inf and falls back to its lower bound, the
// only finite thing known about it. Without that, `time.Duration(+Inf * 1e9)`
// converts to a garbage value which then also poisons the NEXT window, because
// formatRuntimeDelta compares the two totals before subtracting.
func histogramTotal(h *metrics.Float64Histogram) time.Duration {
	if h == nil {
		return 0
	}
	var total time.Duration
	for i, count := range h.Counts {
		if count == 0 {
			continue
		}
		if i+1 >= len(h.Buckets) {
			// Unreachable under the documented contract (len(Buckets) is
			// len(Counts)+1). Guarded anyway because this runs on the Bubble
			// Tea program goroutine, where a panic takes the user's whole
			// session down — a truncated diagnostic is the better failure.
			break
		}
		upper := h.Buckets[i+1]
		if upper > 1e18 || upper != upper { // +Inf or NaN
			upper = h.Buckets[i]
		}
		total += time.Duration(count) * time.Duration(upper*float64(time.Second))
	}
	return total
}

// formatRuntimeDelta renders the runtime section of one perf line.
//
// Every figure except heap and goroutines is a DELTA over the window, because a
// cumulative counter on a process that has been up for days cannot show that
// this window was different from the last one — which is the only question the
// perf log is ever asked.
//
// Two distinct sentinels, because two distinct things go unreported:
//
//	?  the figure could not be measured (metric absent, or a counter that fell,
//	   meaning the samples did not come from one continuous run)
//	-  nothing was published to report: assist is accounted at mark termination,
//	   so a window in which no GC cycle completed has no assist figure to give.
//	   Printing 0s there would claim the collector charged the mutator nothing,
//	   which is a finding, and not one this reading supports.
//
// heap is the heap the PREVIOUS GC marked live, so it holds the same value for
// every window between cycles. That is a step function by design, not a stuck
// reading; `gc=` on the same line says when it last moved.
func formatRuntimeDelta(prev, cur runtimeSample, window time.Duration) string {
	cpu := "?"
	if prev.haveCPU && cur.haveCPU && cur.cpuUsed >= prev.cpuUsed {
		cpu = (cur.cpuUsed - prev.cpuUsed).Round(time.Millisecond).String()
	}

	cycles := "?"
	cycled := false
	if prev.haveCycles && cur.haveCycles && cur.gcCycles >= prev.gcCycles {
		n := cur.gcCycles - prev.gcCycles
		cycles = fmt.Sprintf("%d", n)
		cycled = n > 0
	}

	pause := "?"
	if prev.havePause && cur.havePause && cur.gcPause >= prev.gcPause {
		pause = (cur.gcPause - prev.gcPause).Round(time.Microsecond).String()
	}

	assist := "?"
	if prev.haveAssist && cur.haveAssist && cur.gcAssist >= prev.gcAssist {
		if cycled {
			d := time.Duration((cur.gcAssist - prev.gcAssist) * float64(time.Second))
			assist = d.Round(time.Microsecond).String()
		} else {
			assist = "-"
		}
	}

	heap := "?"
	if cur.haveLive {
		heap = formatHeapBytes(cur.heapLive)
	}

	return fmt.Sprintf("rt(cpu=%s/%s gor=%d heap=%s gc=%s pause=%s assist=%s)",
		cpu, window.Round(time.Millisecond), cur.goroutines, heap, cycles, pause, assist)
}

// formatHeapBytes renders a heap size without collapsing a small one to "0MB".
//
// A file whose whole argument is "never print a confident zero" must not print
// one itself: integer megabytes turn every heap under a megabyte into 0MB,
// which reads as a failed measurement rather than a small heap.
func formatHeapBytes(b uint64) string {
	const mb = 1 << 20
	if b < mb {
		return fmt.Sprintf("%dKB", b>>10)
	}
	return fmt.Sprintf("%dMB", b/mb)
}
