---
name: notify-claude-md-quil-activate-gap
description: cmd/quil-activate (new binary added by system-notifications) is missing from .claude/CLAUDE.md's Architecture binary list and Build Variants table; the "(6 binaries)" build-count comment in both CLAUDE.md and scripts/dev.sh's own help text is stale.
metadata:
  type: project
---

Round-2 finding (2026-08-14) on branch `feat/system-notifications`.

`.claude/CLAUDE.md` was updated in this branch for `internal/notify` (Architecture
bullet) and the M17 milestone row — both accurate. But the new **binary**
`cmd/quil-activate` (a `-H windowsgui` helper, added in commit `853ad0b`, wired
into `scripts/dev.sh build`/`clean`/`cross`, `.goreleaser.yml`, and
`.github/workflows/ci.yml`) was never added to:

- The Architecture section's binary list (only `cmd/quil/` and `cmd/quild/` are
  listed as top-level entry points).
- The Build Variants table (still describes only the prod/dev/debug
  `quil.exe`+`quild.exe` pairs).
- The comment `# Build all variants: prod, dev, debug (6 binaries)` — this
  exact line is duplicated verbatim in `.claude/CLAUDE.md`'s Building section
  AND in `scripts/dev.sh`'s own `help` case (line ~199). `dev.sh build` now
  produces 7 Windows binaries (added `quil-activate.exe`), so both copies of
  the count are wrong.

**Why:** `scripts/check-claude-md-size.sh` truncates from the tail on overflow
and the project's own rule (`.claude/CLAUDE.md`, "Adding to this file?") asks
"does this apply when I open a file in a different package?" — a whole new
binary/package is exactly that case, and it was only half-applied here.

**How to apply:** On the next review round, check whether `cmd/quil-activate`
now appears in the Architecture list and whether the Build Variants table /
binary-count comments were corrected in both `.claude/CLAUDE.md` and
`scripts/dev.sh`. If still absent, keep flagging as MEDIUM (structural change
not fully reflected in CLAUDE.md, per the rules-compliance Step 4 category).

## PARTIALLY RESOLVED (2026-08-14, same session, commit b843d9c)

`.claude/CLAUDE.md`'s Build Variants section now has a full paragraph on
`quil-activate.exe` (why it's a 7th, variant-agnostic binary, why `-H
windowsgui`), the `(6 binaries)` comment was amended to `(6 binaries) +
quil-activate.exe`, and the M17 row now names it and records the window-raise
removal with the techniques already tried. Good enough that CLAUDE.md's
Architecture bullet list still not naming `cmd/quil-activate/` explicitly is a
non-issue at this point — do not re-flag that.

**Still NOT fixed by b843d9c**: `scripts/dev.sh`'s own `help` case (line
~199) still prints the literal stale `"Build all variants: prod, dev, debug (6
binaries)"` with no mention of the 7th. Keep flagging that one line only.
