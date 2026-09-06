- **The TUI's 5-second perf summary now reports runtime accounting.** Each line
  carries an `rt(...)` section: CPU time actually used during the window, live
  heap, goroutine count, and the window's GC cycles, pause total and mark-assist
  time.

  The log could already say a frame took 900 ms. It could not say why a frame
  doing the same work cost fourteen times more, and that gap is expensive: three
  separate explanations for a production stall each had to be reproduced in a
  benchmark before they could be ruled out, because nothing in the log could
  distinguish them. CPU used, compared against the time the same window spent in
  `View` and `Update`, separates "the work genuinely cost more" from "the process
  was not running" — which are investigations in different places.

  CPU comes from the operating system (`getrusage`, or `GetProcessTimes` on
  Windows) rather than from Go's `/cpu/classes` metrics. Those advance only when
  a garbage-collection cycle completes: measured on Go 1.25, three seconds of
  saturated CPU with the collector off reports zero. A TUI collecting every few
  minutes would have printed a confident `cpu=0s` in almost every window.

  Two sentinels, never a fabricated zero: `?` means the figure could not be
  measured, `-` means nothing was published to report.

- **Two gated reproduction harnesses for frame cost** (`QUIL_GC_REPRO`,
  `QUIL_STATE_SWEEP`). One builds a production-shaped workspace — 48 panes at the
  adaptive scrollback depth, ~1 GB live — and measures frame latency against GC
  activity; the other sweeps the model states a TUI can sit in for minutes and
  reports each one's frame cost against a baseline. Both are skipped unless their
  environment variable is set, so neither joins the ordinary test loop or CI.
