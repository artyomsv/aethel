# QA Agent Memory Index

## Project
- [project_ipc_write_window_wiring_gap.md](project_ipc_write_window_wiring_gap.md) — newConn → writeDeadline wiring is pinned by TestNewConn_UsesTheProductionWriteWindow; found by mutation, was uncovered
- [project_subscribe_broadcast_wiring_gap.md](project_subscribe_broadcast_wiring_gap.md) — MsgSubscribe opt-out: predicate tests proved nothing about reachability; RESOLVED in-branch by 3 mutation-verified wiring tests
- [project_notify_wiring_gap.md](project_notify_wiring_gap.md) — raiseAttentionToast/sweepOutstandingToasts call sites were mutation-removable with a green suite; now pinned by Update-driven tests
- [project_test_tooling.md](project_test_tooling.md) — Docker test runner, no local Go, worktree isolation recipe, mutation-testing gate booleans/dispose calls, concurrent agents' stray zz_probe test files
- [project_mutation_probe_on_a_copy.md](project_mutation_probe_on_a_copy.md) — probing on a scratchpad copy: sources are CRLF (\n regexes no-op) and restores must come from `git show HEAD:`, not the live worktree
- [project_procreport_selfstat_gaps.md](project_procreport_selfstat_gaps.md) — PR #195: stat-ticker and daemon-own-row gaps both RESOLVED in-branch (mutation-verified); procCollector goroutine still not tied to Daemon.Stop() → techdebt/4-2
- [project_perfruntime_delta_baseline_gap.md](project_perfruntime_delta_baseline_gap.md) — PR #205: `s.lastRuntime = rt` and histogramTotal's +Inf fallback both survive mutation; the flush test only flushes once

## Feedback
- [feedback_tier_seam_and_hardcoded_dispatch_gaps.md](feedback_tier_seam_and_hardcoded_dispatch_gaps.md) — dispatch-registry refactors: hardcoded key switches outside the table, case arms moved across a tier seam, and nil-safe fallbacks all evade naive test-presence checks; mutate and re-run to confirm
