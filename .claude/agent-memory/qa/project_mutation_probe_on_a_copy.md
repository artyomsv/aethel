---
name: project_mutation_probe_on_a_copy
description: Mutation probing on a scratchpad copy of this repo — files are CRLF, so \n regexes silently no-op, and the baseline must come from `git show HEAD:` not the live worktree
metadata:
  type: project
---

When mutation-testing quil on a COPY in the scratchpad (the read-only-worktree contract), two things
silently produce a false "mutation survived":

1. **The Go sources on disk are CRLF.** A `perl -0pi -e 's/...\n...//'` pattern matches nothing, the
   copy stays pristine, the suite goes green, and the probe reads as "no test covers this". Every
   multi-line pattern needs `\r?\n`, and every mutation needs an assertion that it APPLIED
   (`grep -c <marker>` before/after) with the run SKIPPED when it did not.
2. **Restoring the copy from the live worktree imports another agent's mid-edit state.** Observed
   2026-09-06 (PR #203): a probe of `overlayResizeCmd` produced nine unrelated failures identical to
   an "always refuse" mutation, because `cp` from the worktree happened while the team lead was
   rewriting `model.go`. Restore each file with `git show HEAD:<path> > <copy>/<path>` instead, and
   assert `git show HEAD:<f> | diff -q - <copy>/<f>` for every file before each mutation.

**Why:** both failures are silent and both point the same wrong way — a surviving mutation reads as a
coverage gap, so the report accuses the PR of a hole that does not exist.
**How to apply:** always run one UNMUTATED baseline on the copy first (it must be green), assert the
mutation applied, and pin every restore to a SHA. See [[project_test_tooling]] for the wider
concurrent-edit problem this is the cheap form of.
