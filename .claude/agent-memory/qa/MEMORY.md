# QA Agent Memory Index

## Project
- [project_ipc_write_window_wiring_gap.md](project_ipc_write_window_wiring_gap.md) — newConn → writeDeadline wiring is pinned by TestNewConn_UsesTheProductionWriteWindow; found by mutation, was uncovered
- [project_subscribe_broadcast_wiring_gap.md](project_subscribe_broadcast_wiring_gap.md) — MsgSubscribe opt-out: predicate tests proved nothing about reachability; RESOLVED in-branch by 3 mutation-verified wiring tests
- [project_notify_wiring_gap.md](project_notify_wiring_gap.md) — raiseAttentionToast/sweepOutstandingToasts call sites were mutation-removable with a green suite; now pinned by Update-driven tests
- [project_test_tooling.md](project_test_tooling.md) — Docker test runner, no local Go, worktree isolation recipe, mutation-testing gate booleans/dispose calls, concurrent agents' stray zz_probe test files
- [project_mutation_probe_on_a_copy.md](project_mutation_probe_on_a_copy.md) — probing on a scratchpad copy: sources are CRLF (\n regexes no-op) and restores must come from `git show HEAD:`, not the live worktree
- [project_notification_timeline_wiring_gaps.md](project_notification_timeline_wiring_gaps.md) — PR #204: 15 mutations green; NewModel's SetGroups line can be deleted and the whole sidebar filter goes inert unnoticed
- [project_procreport_selfstat_gaps.md](project_procreport_selfstat_gaps.md) — PR #195: stat-ticker and daemon-own-row gaps both RESOLVED in-branch (mutation-verified); procCollector goroutine still not tied to Daemon.Stop() → techdebt/4-2
- [project_perfruntime_delta_baseline_gap.md](project_perfruntime_delta_baseline_gap.md) — PR #205: `s.lastRuntime = rt` and histogramTotal's +Inf fallback both survive mutation; the flush test only flushes once
- [project_ingest_arrival_order_gaps.md](project_ingest_arrival_order_gaps.md) — PR #199: both gaps (Ingester.Cancel's order-list removal, the subagent branch keying on the CLASSIFIED kind) RESOLVED in-branch and pinned; kept for the method — mutate a removal path to a no-op, and enumerate every type a kind covers
- [project_notify_activate_run_untested.md](project_notify_activate_run_untested.md) — notify.RunActivation, the handler both quil-activate.exe and `quil activate` call, has zero tests on any platform, though two of its three branches are platform-neutral and Linux-testable
- [project_plugin_availability_marker_vacuity.md](project_plugin_availability_marker_vacuity.md) — "DetectAvailability re-detected locally" assertions are vacuous with a terminal/terminal-wide marker: both are Available:true at construction, so they pass with the call deleted

## Feedback
- [feedback_tier_seam_and_hardcoded_dispatch_gaps.md](feedback_tier_seam_and_hardcoded_dispatch_gaps.md) — dispatch-registry refactors: hardcoded key switches outside the table, case arms moved across a tier seam, and nil-safe fallbacks all evade naive test-presence checks; mutate and re-run to confirm
