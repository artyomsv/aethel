---
name: project_optional_binary_tier_gap
description: PR #193 (fix/windows-toast) — extractBinaries' split-pair rejection is correct but unpinned by any committed test
metadata:
  type: project
---

PR #193 (`internal/update/stage.go`) added `OptionalBinaryNames` (currently just
`quil-activate.exe` on Windows) and changed `extractBinaries`' completeness check
from a length compare (`len(files) != len(names)`) to a per-name loop over the
REQUIRED set only (`stage.go:334-338`). The PR's own comment says this exists
specifically so an archive carrying the optional helper but missing a required
binary (e.g. `quild.exe`) is still rejected — a naive length compare would pass
such an archive because the optional entry pads the count back up.

I verified by hand (temporary probe test, deleted after use — zip with
`quil.exe` + `quil-activate.exe`, no `quild.exe`) that the current code DOES
correctly reject this case (`archive missing required binary quild.exe`). But
no committed test exercises it — `internal/update/optional_test.go`'s three
tests only cover "helper present, both required present" and "no helper,
both required present." Nothing analogous to the existing
`TestVerifyStaged_ManifestOmittingABinary_Rejected` pattern exists for this
one at the `extractBinaries`/`Stage` level.

**Why:** this is the same shape as [[project_notify_wiring_gap]] and
[[feedback_tier_seam_and_hardcoded_dispatch_gaps]] — a refactor whose
correctness right now rests entirely on reading the code, with the specific
regression it guards against (a split-pair archive slipping through) fully
mutation-provable-away and nothing in the suite would catch it.
**How to apply:** if this file changes again, mutate `extractBinaries`' loop
back toward a count-based check (or make it check `wanted` instead of `names`)
and confirm the suite goes red. If it stays green, the split-pair gap has
reopened — recommend the missing test
(`TestStage_HelperPresentQuildMissing_Rejected` or similar) be added rather
than treating a clean mutation run as sufficient proof on its own.
