package tui

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sort"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/config"
)

// Reproduction harness for the production TUI stall.
//
// The symptom, measured from ~71 000 perf-log windows across two production
// sessions: a frame normally costs 2-5 ms, but in episodes the SAME frame costs
// 100-966 ms, and one 5 s perf ticker fired 8.1 s late — so the whole process
// waits, not just the renderer. The episodes are not a leak (a session sat at
// 75% slow frames in hour 31 and 0.0% in hour 34) and they carry LESS pane
// output than a healthy window, not more. The one workload signal that tracks
// them is that agents are mid-turn: workSpinnerTickMsg appears in 100% of slow
// windows against 71% of healthy ones.
//
// The hypothesis under test is that the live VT heap is the cause: 48 panes
// each holding the adaptive 2000-line scrollback keep ~1 GB live, and a
// renderer that allocates per frame is then charged GC mark assists, which show
// up as a frame that waits.
//
// The experiment is a CONTROL, not a stress test. Every arm builds the same
// workspace, drives the same frames, and does byte-for-byte the same per-frame
// work — View renders the visible screen, whose size does not depend on how
// deep the scrollback behind it is. What varies is how many scrollback lines
// are retained (the live heap) and, in the last arm, GOGC.
//
// Gated behind QUIL_GC_REPRO because the large arms hold ~1 GB and the run
// takes minutes: it must never join the ordinary `dev.sh test` loop or CI.
// Run it with:
//
//	docker run --rm -v "$PWD:/src" -v quil-gomod:/go/pkg/mod \
//	  -v quil-gocache:/root/.cache/go-build -w //src -e QUIL_GC_REPRO=1 \
//	  golang:1.25-alpine go test ./internal/tui/ -run TestFrameLatencyVsLiveHeap -v -timeout 40m
const gcReproEnv = "QUIL_GC_REPRO"

// reproPanes matches the production workspace the stall was measured on: 48
// tabs, one pane each. Pane COUNT is what drives the adaptive scrollback depth
// in production, so it is not a free parameter here.
const reproPanes = 48

// reproTermWidth/Height are the terminal the frames are rendered at. Scrollback
// memory is per CELL, so the emulator width is as load-bearing as the depth —
// filling a 2000-line scrollback on an 80-column grid would under-report the
// live heap by more than half.
const (
	reproTermWidth  = 200
	reproTermHeight = 50
)

// reproFrames is how many frames each arm drives.
//
// Sized from the first run of this harness rather than guessed: a frame
// allocates ~350 KB, so at GOGC=100 over a ~1 GB live heap the collector does
// not run until ~1 GB has been allocated — about 3000 frames. 12 000 frames is
// several natural cycles, which is the minimum that can answer what a cycle
// costs. The first version used 1200 and observed ZERO cycles; it measured the
// absence of GC and would have read as "GC is innocent".
const reproFrames = 12000

// gcSnapshot is the subset of runtime/metrics that decides this question.
//
// ReadMemStats is deliberately NOT the primary source: it stops the world, and
// a harness that perturbs the very pauses it measures cannot answer whether the
// pauses are real. runtime/metrics reads without a stop-the-world. MemStats is
// still sampled once per arm, for the live-heap number that is comparable with
// the private-bytes figure taken from production.
type gcSnapshot struct {
	cycles     uint64        // /gc/cycles/total:gc-cycles
	liveBytes  uint64        // /gc/heap/live:bytes
	assistCPU  float64       // /cpu/classes/gc/mark/assist:cpu-seconds
	gcTotalCPU float64       // /cpu/classes/gc/total:cpu-seconds
	pauseTotal time.Duration // sum over /gc/pauses:seconds
	allocBytes uint64        // /gc/heap/allocs:bytes

	// Raw histogram state, kept so the PEAK can be computed over the delta
	// between two snapshots.
	//
	// Reducing to a peak at read time was wrong and produced a number that read
	// as this arm's worst pause while actually being the worst pause since the
	// process started — including workspace construction, the forced settling
	// collections, and every previous arm. The symptom was three arms printing
	// an identical "worst pause bucket 1.835ms" and nobody noticing, because a
	// cumulative maximum looks exactly like a real one.
	//
	// Counts are COPIED: metrics.Read documents that it may reuse the histogram
	// it returns, so holding the slice would make the "before" snapshot mutate
	// into the "after" one.
	pauseBuckets, schedBuckets []float64
	pauseCounts, schedCounts   []uint64
}

// copyHistogram snapshots a histogram's counts and bucket bounds.
func copyHistogram(h *metrics.Float64Histogram) (buckets []float64, counts []uint64) {
	if h == nil {
		return nil, nil
	}
	return append([]float64(nil), h.Buckets...), append([]uint64(nil), h.Counts...)
}

// deltaPeak returns the largest bucket that gained a sample between two
// readings of the same histogram — the worst value observed in that interval,
// rather than the worst ever observed.
func deltaPeak(buckets []float64, before, after []uint64) time.Duration {
	var peak time.Duration
	for i := range after {
		gained := after[i]
		if i < len(before) {
			if after[i] <= before[i] {
				continue
			}
			gained = after[i] - before[i]
		}
		if gained == 0 || i+1 >= len(buckets) {
			continue
		}
		upper := buckets[i+1]
		if upper > 1e18 || upper != upper { // +Inf or NaN
			upper = buckets[i]
		}
		if d := time.Duration(upper * float64(time.Second)); d > peak {
			peak = d
		}
	}
	return peak
}

// gcMetricNames are read as one batch. Any name the running Go version does not
// know comes back KindBad and is reported as absent rather than as zero — a
// silent zero would read as "no assist time" and answer the question wrongly.
var gcMetricNames = []string{
	"/gc/cycles/total:gc-cycles",
	"/gc/heap/live:bytes",
	"/cpu/classes/gc/mark/assist:cpu-seconds",
	"/cpu/classes/gc/total:cpu-seconds",
	"/gc/pauses:seconds",
	"/sched/latencies:seconds",
	"/gc/heap/allocs:bytes",
}

// cycleMetric is read on its own once per frame. The full batch above includes
// two histograms, and reading those 12 000 times would put the harness's own
// cost inside the frame timings it exists to measure.
//
// Completed cycles are all Go offers. `metrics.All()` on Go 1.25 has
// /gc/cycles/automatic, /forced and /total — every one a COMPLETION count — and
// runtime.MemStats carries no in-progress flag either. See gcProximityFrames
// for what this harness does about that.
const cycleMetric = "/gc/cycles/total:gc-cycles"

// readCycles samples the completed-GC-cycle counter.
//
// Returns ok=false rather than zero when the metric is unavailable. Zero here
// would be the fabricated-zero failure the rest of this file is written to
// avoid: run.cycles and gcFrames would both read 0 and the headline table would
// report "no GC ran" for a run nobody measured. That guard is not theoretical —
// it is what caught a first attempt at this classification reading a metric
// name (/gc/cycles/started) that does not exist on any Go version.
func readCycles() (uint64, bool) {
	s := []metrics.Sample{{Name: cycleMetric}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0, false
	}
	return s[0].Value.Uint64(), true
}

// readGC samples the metrics above. missing names the metrics this Go build did
// not supply, so a caller can say so instead of reporting a fabricated zero.
func readGC() (gcSnapshot, []string) {
	samples := make([]metrics.Sample, len(gcMetricNames))
	for i, n := range gcMetricNames {
		samples[i].Name = n
	}
	metrics.Read(samples)

	var s gcSnapshot
	var missing []string
	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindBad {
			missing = append(missing, sample.Name)
			continue
		}
		switch sample.Name {
		case "/gc/cycles/total:gc-cycles":
			s.cycles = sample.Value.Uint64()
		case "/gc/heap/live:bytes":
			s.liveBytes = sample.Value.Uint64()
		case "/gc/heap/allocs:bytes":
			s.allocBytes = sample.Value.Uint64()
		case "/cpu/classes/gc/mark/assist:cpu-seconds":
			s.assistCPU = sample.Value.Float64()
		case "/cpu/classes/gc/total:cpu-seconds":
			s.gcTotalCPU = sample.Value.Float64()
		case "/gc/pauses:seconds":
			h := sample.Value.Float64Histogram()
			s.pauseTotal = histogramTotal(h)
			s.pauseBuckets, s.pauseCounts = copyHistogram(h)
		case "/sched/latencies:seconds":
			s.schedBuckets, s.schedCounts = copyHistogram(sample.Value.Float64Histogram())
		}
	}
	return s, missing
}

// reproArm is one leg of the control: same frames, same per-frame work.
type reproArm struct {
	name       string
	scrollback int
	// gogc is the GC percentage this arm runs at. 100 is what production uses
	// (quil sets none). A lower value is a SENSITIVITY arm — it does not
	// reproduce production's configuration, it forces cycles to happen often
	// enough to measure what one cycle costs at this heap size.
	gogc int
}

// buildReproModel builds a workspace shaped like the production one and fills
// every pane's scrollback to the given depth. The panes are returned so the
// caller can Dispose them.
//
// Order matters twice. The emulator is resized to the real terminal geometry
// BEFORE any output is written, because x/vt lays out each scrollback line at
// the width in force when the line scrolled off — filling first and resizing
// after would retain 80-column lines and understate the heap. And the depth is
// published through SetScrollbackLines before the panes are constructed,
// because SetScrollbackSize is applied at emulator CREATION: x/vt reslices its
// backing array rather than reallocating, so a depth set afterwards frees
// nothing (see adaptiveScrollbackLines).
func buildReproModel(tb testing.TB, scrollback, paneCount int) (Model, []*PaneModel) {
	tb.Helper()

	prev := explicitScrollback.Load()
	SetScrollbackLines(scrollback)
	tb.Cleanup(func() { explicitScrollback.Store(prev) })

	projects := 3
	ps := make([]*ProjectModel, projects)
	for i := range ps {
		ps[i] = &ProjectModel{ID: fmt.Sprintf("proj-%d", i), Name: fmt.Sprintf("project-%d", i)}
	}

	// Each pane is filled with 25% MORE lines than the scrollback holds, so
	// every arm ends with a scrollback that is genuinely full and evicting.
	// Filling exactly to the depth would leave the large arm one line short of
	// the steady state the production panes have been in for hours.
	fillLines := scrollback + scrollback/4

	panes := make([]*PaneModel, 0, paneCount)
	for i := 0; i < paneCount; i++ {
		pane := NewPaneModel(fmt.Sprintf("pane-%d", i), 500*512)
		// The pane area, not the terminal: one pane per tab, minus the project
		// sidebar and the pane's own border.
		pane.ResizeVT(reproTermWidth-defaultSidebarWidth-2, reproTermHeight-chromeHeight-2)
		pane.Type = "claude-code"
		pane.WideCanvas = true
		for l := 0; l < fillLines; l++ {
			pane.AppendOutput([]byte(realisticPaneLines[l%len(realisticPaneLines)]))
		}
		// Mid-turn, like the ~25 working panes the stall episodes coincide
		// with. This is what keeps the work spinner running, and the spinner is
		// what makes production rebuild a frame five times a second.
		pane.working = true
		pane.turnActive = true

		tab := tabWith(pane)
		tab.Name = fmt.Sprintf("tab-%d", i)
		ps[i%projects].tabs = append(ps[i%projects].tabs, tab)
		panes = append(panes, pane)
	}

	m := Model{
		cfg:           config.Default(),
		projects:      ps,
		activeProject: 0,
		sidebarOpen:   true,
		sidebarWidth:  defaultSidebarWidth,
		width:         reproTermWidth,
		height:        reproTermHeight,
		termFocused:   true,
		notifications: NewNotificationCenter(30, 50),
		mcpHighlights: map[string]bool{},
		perfStats:     newEventLoopStats(),
	}
	m.viewCache = &viewCacheBox{}
	return m, panes
}

// gcProximityFrames is how many frames before a completed cycle count as
// GC-adjacent.
//
// A PROXIMITY WINDOW, not phase detection, and the distinction is forced rather
// than chosen: Go exposes no "GC is running" signal. `metrics.All()` on Go 1.25
// offers only completed-cycle counters (/gc/cycles/automatic, /forced, /total)
// and runtime.MemStats has no in-progress flag either.
//
// That matters because the narrow question — "did a cycle finish during THIS
// frame?" — marks exactly one frame per cycle and files every other assisted
// frame under "clean". Mark assists are charged across the whole concurrent
// mark phase, which spans many frames, so the narrow test biases the comparison
// toward acquitting GC. Since the mark phase cannot be observed, the honest
// substitute is a generous window ending at the completion: at these frame
// rates a mark phase over a ~1 GB heap is far shorter than 100 frames, so the
// window contains it, at the cost of also containing innocent frames. That
// error direction is the safe one — it can only make GC look WORSE.
const gcProximityFrames = 100

// frameRun is what one arm's animation produced.
type frameRun struct {
	sorted []time.Duration
	// order holds the same durations in the order they were measured, which is
	// what the proximity pass needs and what sorting destroys.
	order []time.Duration
	// cycleAt holds the frame index each completed cycle was observed at.
	cycleAt []int

	gcFrames int
	// gcMax is the worst frame within gcProximityFrames of a completed cycle;
	// cleanMax the worst of the rest. If GC is the mechanism, the two are far
	// apart. If they are the same, the tail is not made of GC.
	//
	// NEITHER is what the falsification rests on. That rests on the OVERALL max
	// in the percentile line, which needs no classification to be believed.
	gcMax, cleanMax time.Duration
	cycles          uint64
}

// classify fills gcFrames/gcMax/cleanMax from order and cycleAt.
func (r *frameRun) classify() {
	near := make([]bool, len(r.order))
	for _, at := range r.cycleAt {
		lo := at - gcProximityFrames
		if lo < 0 {
			lo = 0
		}
		for i := lo; i <= at && i < len(near); i++ {
			near[i] = true
		}
	}
	for i, d := range r.order {
		if near[i] {
			r.gcFrames++
			if d > r.gcMax {
				r.gcMax = d
			}
		} else if d > r.cleanMax {
			r.cleanMax = d
		}
	}
}

// driveFrames runs the animation production runs.
//
// The per-frame work is deliberately identical in every arm: the work spinner
// advances (which is what invalidates the render caches in production), and
// every eighth frame the active pane receives one line of output. Neither
// depends on scrollback depth, so a latency difference between arms is
// attributable to the heap and to GOGC, and to nothing else.
func driveFrames(tb testing.TB, m *Model, frames int) frameRun {
	tb.Helper()
	durations := make([]time.Duration, 0, frames)
	active := m.activeTabModel().Leaves()[0]

	_ = m.View() // prime every pane cache; the first frame is not a sample

	first, ok := readCycles()
	if !ok {
		tb.Fatal("the completed-GC-cycle counter is unavailable — this harness cannot classify " +
			"frames without it, and reporting zero cycles would read as 'no GC ran'")
	}
	lastFinished := first
	var run frameRun

	for i := 0; i < frames; i++ {
		m.workSpinnerFrame++
		for _, tab := range m.allTabs() {
			if tab.Root == nil {
				continue
			}
			for _, p := range tab.Leaves() {
				if p != nil && p.working {
					p.workFrame = m.workSpinnerFrame
				}
			}
		}
		if i%8 == 0 {
			active.AppendOutput([]byte(realisticPaneLines[i%len(realisticPaneLines)]))
		}

		start := time.Now()
		_ = m.View()
		d := time.Since(start)
		durations = append(durations, d)

		// Read AFTER the frame is timed, so the metrics call is never inside a
		// sample. It perturbs the FOLLOWING frame, identically in every arm, so
		// the control holds.
		if finished, ok := readCycles(); ok && finished != lastFinished {
			lastFinished = finished
			run.cycleAt = append(run.cycleAt, i)
		}
	}
	run.cycles = lastFinished - first
	run.order = durations
	run.classify()

	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	run.sorted = sorted
	return run
}

// pct returns the p-th percentile of an already-sorted slice.
func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p / 100 * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// TestFrameLatencyVsLiveHeap answers one falsifiable question: does the live VT
// heap reproduce the production tail — occasional View calls of 100 ms and up
// while the work per frame is unchanged?
//
// It asserts nothing about absolute timings, because a container's frame cost
// is not the user's. What it reports is the SHAPE: each arm's tail, the live
// heap it holds, how many GC cycles ran, and whether the worst frames are the
// ones a cycle completed in. A confirmation looks like a large-heap arm whose
// worst frames are one to two orders above its own median AND land on GC
// frames. Anything else falsifies the hypothesis, which is equally a result.
func TestFrameLatencyVsLiveHeap(t *testing.T) {
	if os.Getenv(gcReproEnv) == "" {
		t.Skipf("set %s=1 to run the live-heap reproduction (holds ~1 GB, takes minutes)", gcReproEnv)
	}

	arms := []reproArm{
		// The floor of the adaptive policy (minAdaptiveScrollbackLines) is what
		// 48 production panes actually get, at the GOGC production runs.
		{name: "large-heap/gogc-100", scrollback: 2000, gogc: 100},
		// Same panes, same frames, a heap too small to matter.
		{name: "small-heap/gogc-100", scrollback: 100, gogc: 100},
		// Sensitivity, not a reproduction: GOGC low enough that cycles are
		// frequent, so the cost of ONE cycle over a ~1 GB live heap is measured
		// directly rather than inferred from how rarely it happened.
		{name: "large-heap/gogc-10", scrollback: 2000, gogc: 10},
	}

	type result struct {
		arm       reproArm
		run       frameRun
		live      uint64
		heapAlloc uint64
		before    gcSnapshot
		after     gcSnapshot
		buildTime time.Duration
	}
	var results []result

	for _, arm := range arms {
		buildStart := time.Now()
		m, panes := buildReproModel(t, arm.scrollback, reproPanes)
		buildTime := time.Since(buildStart)

		// Settle before measuring: the fill above is a huge allocation burst,
		// and a cycle still draining from it would be charged to the first
		// frames of the run rather than to the fill that caused it.
		//
		// GOGC is process-global, so the restore is registered with t.Cleanup
		// as well as run at the end of the loop body: a t.Fatal mid-arm would
		// otherwise leave the whole test binary at the sensitivity arm's
		// GOGC=10, and every test scheduled after it would run under a
		// collector nobody configured.
		prevGOGC := debug.SetGCPercent(arm.gogc)
		t.Cleanup(func() { debug.SetGCPercent(prevGOGC) })
		runtime.GC()
		runtime.GC()

		before, missing := readGC()
		if len(missing) > 0 {
			t.Logf("NOTE: this Go build does not supply %v — those columns are absent, not zero", missing)
		}

		run := driveFrames(t, &m, reproFrames)
		after, _ := readGC()

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		results = append(results, result{
			arm: arm, run: run, live: after.liveBytes, heapAlloc: ms.HeapAlloc,
			before: before, after: after, buildTime: buildTime,
		})

		// Dispose every pane before the next arm builds. Without this the arms
		// are not independent: each emulator is held live by its own
		// drainVTResponses goroutine, so dropping the Model frees nothing and
		// the "small heap" arm runs on top of the large arm's gigabyte. The
		// first version of this harness omitted it and reported the SMALL arm
		// holding MORE live heap than the large one, which is the shape of a
		// broken control rather than a finding.
		for _, p := range panes {
			p.Dispose()
		}
		debug.SetGCPercent(prevGOGC)
		runtime.GC()
		debug.FreeOSMemory()
	}

	t.Logf("workspace: %d panes, %dx%d terminal, %d frames per arm",
		reproPanes, reproTermWidth, reproTermHeight, reproFrames)
	for _, r := range results {
		s := r.run.sorted
		t.Logf("")
		t.Logf("=== %s ===", r.arm.name)
		t.Logf("  live heap        %.0f MB (MemStats HeapAlloc %.0f MB), fill took %v",
			float64(r.live)/(1<<20), float64(r.heapAlloc)/(1<<20), r.buildTime.Round(time.Millisecond))
		t.Logf("  frame p50 %v  p90 %v  p99 %v  p99.9 %v  max %v",
			pct(s, 50).Round(time.Microsecond), pct(s, 90).Round(time.Microsecond),
			pct(s, 99).Round(time.Microsecond), pct(s, 99.9).Round(time.Microsecond),
			s[len(s)-1].Round(time.Microsecond))
		t.Logf("  tail ratio max/p50 %.1fx", float64(s[len(s)-1])/float64(pct(s, 50)))
		t.Logf("  GC cycles during run %d, frames within %d of one %d/%d",
			r.run.cycles, gcProximityFrames, r.run.gcFrames, reproFrames)
		t.Logf("  worst frame within %d frames of a completed cycle %v, worst frame outside that window %v",
			gcProximityFrames, r.run.gcMax.Round(time.Microsecond), r.run.cleanMax.Round(time.Microsecond))
		t.Logf("  mark-assist CPU %.3fs, GC CPU total %.3fs, GC pause total %v, worst pause IN THIS ARM %v",
			r.after.assistCPU-r.before.assistCPU, r.after.gcTotalCPU-r.before.gcTotalCPU,
			(r.after.pauseTotal - r.before.pauseTotal).Round(time.Microsecond),
			deltaPeak(r.after.pauseBuckets, r.before.pauseCounts, r.after.pauseCounts).Round(time.Microsecond))
		t.Logf("  allocated during run %.0f MB, worst scheduler latency IN THIS ARM %v",
			float64(r.after.allocBytes-r.before.allocBytes)/(1<<20),
			deltaPeak(r.after.schedBuckets, r.before.schedCounts, r.after.schedCounts).Round(time.Microsecond))
	}
}

// TestDeltaPeak_IgnoresSamplesFromBeforeTheInterval is the guard for the bug
// this replaced.
//
// The previous code reduced each histogram to a peak AT READ TIME, so the
// "worst pause" printed beside an arm was the worst since the process started —
// workspace construction, the forced settling collections, and every earlier
// arm included. Three arms printed an identical 1.835 ms and it read as a real
// measurement, because a cumulative maximum looks exactly like one.
//
// Not gated: pure, instant, and it is the assertion that keeps the reporting
// honest, so it belongs in the ordinary suite rather than behind QUIL_GC_REPRO.
func TestDeltaPeak_IgnoresSamplesFromBeforeTheInterval(t *testing.T) {
	// Buckets: [0,1ms) [1ms,10ms) [10ms,100ms)
	buckets := []float64{0, 0.001, 0.01, 0.1}

	t.Run("a big sample already present is not attributed to this interval", func(t *testing.T) {
		before := []uint64{5, 0, 3} // three 100 ms-bucket pauses, all historical
		after := []uint64{9, 0, 3}  // only the 1 ms bucket gained
		if got := deltaPeak(buckets, before, after); got != time.Millisecond {
			t.Errorf("deltaPeak() = %v, want 1ms — a pre-existing sample leaked into the interval", got)
		}
	})

	t.Run("a big sample gained during the interval is reported", func(t *testing.T) {
		before := []uint64{5, 0, 3}
		after := []uint64{5, 0, 4}
		if got := deltaPeak(buckets, before, after); got != 100*time.Millisecond {
			t.Errorf("deltaPeak() = %v, want 100ms", got)
		}
	})

	t.Run("no change at all reports nothing", func(t *testing.T) {
		counts := []uint64{5, 2, 3}
		if got := deltaPeak(buckets, counts, counts); got != 0 {
			t.Errorf("deltaPeak() = %v, want 0 — an unchanged histogram has no peak for this interval", got)
		}
	})

	t.Run("an infinite final bound falls back to its lower bound", func(t *testing.T) {
		inf := []float64{0, 0.001, math.Inf(1)}
		if got := deltaPeak(inf, []uint64{0, 0}, []uint64{0, 1}); got != time.Millisecond {
			t.Errorf("deltaPeak() = %v, want 1ms — the +Inf bound leaked into the duration", got)
		}
	})
}
