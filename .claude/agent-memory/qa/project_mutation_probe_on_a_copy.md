---
name: project_mutation_probe_on_a_copy
description: Mutation probing on a scratchpad copy of this repo — line endings vary per file so ALWAYS assert the mutation applied, and the baseline must come from a pinned SHA not the live worktree
metadata:
  type: project
---

When mutation-testing quil on a COPY in the scratchpad (the read-only-worktree contract), three
things silently produce a false "mutation survived":

1. **Line endings are NOT uniform, so never assume either one.** The 2026-09-06 note said "the
   sources on disk are CRLF"; on 2026-09-07 (feat/docker-sandbox) every file I probed was LF, while
   `internal/tui/model.go` in the same tree was CRLF. `.gitattributes` says `eol=lf` but
   `core.autocrlf` and each agent's editor disagree per file, and `git show <sha>:<path>` can hand
   back a different flavour again than the worktree copy. So: never encode an assumption — write
   patterns with `\r?\n`, and **assert the mutation APPLIED** (`cmp -s file file.orig` → if identical,
   report `MUTATION-NOT-APPLIED` and skip the run). That assertion catches the line-ending mismatch
   whichever direction it points, and it is the only check that does. When comparing a copy to a ref,
   normalise BOTH sides (`tr -d '\r'`) before concluding the content differs.

2. **A compile error is not a kill.** Naive perl mutations routinely produce
   `declared and not used: want` or `undefined: filepath`, and a build failure reads as
   `--- FAIL`-adjacent in a grep. Grep the output for `^\s*--- FAIL` specifically, print `# <pkg>` /
   `undefined:` lines separately, and re-do any "kill" that was only a build break — I had four of
   those in one session and each one hid the real answer.

3. **Restoring the copy from the live worktree imports another agent's mid-edit state.** Observed
   2026-09-06 (PR #203): a probe of `overlayResizeCmd` produced nine unrelated failures identical to
   an "always refuse" mutation, because `cp` from the worktree happened while the team lead was
   rewriting `model.go`. Observed again 2026-09-07: the tree was clean at kickoff and eight files were
   modified by a peer agent mid-review.

   **Capture the SHA once and use that literal value — `HEAD` is not a pin.** `git show HEAD:<path>`
   re-resolves `HEAD` on every call, and the same edits that motivate rule 3 also COMMIT during a
   review (PR #203 moved HEAD twice while probes were running). Do:

   ```sh
   BASE=$(git rev-parse HEAD)          # once, before anything is copied
   git show "$BASE:$f" > "$copy/$f"    # every restore
   git show "$BASE:$f" | diff -q - "$copy/$f"   # every file, before each mutation
   ```

   In the Bash tool, `git show <rev>:<path>` needs `MSYS_NO_PATHCONV=1` or the arg becomes
   `<rev>;<path>` — see [[project_test_tooling]]. A loop that forgets it reports hundreds of bogus
   "DIFFERS" lines; `diff -rq copy/ worktree/` is the cheap sanity check that has no such trap.

**Why:** every one of these is silent and they all point the same wrong way — a surviving mutation
reads as a coverage gap, so the report accuses the PR of a hole that does not exist.

**How to apply:** run one UNMUTATED baseline on the copy first (it must be green), assert each
mutation applied, separate build breaks from test failures, and pin every restore to a SHA. Include a
CONTROL mutation you expect to be killed — it proves the harness can detect anything at all, and on
2026-09-07 a control was what showed that `TestMounts_ObjectStoreIsNeverWritable` had a live loop but
a fixture that never entered it. See [[project_test_tooling]] for the wider concurrent-edit problem.
