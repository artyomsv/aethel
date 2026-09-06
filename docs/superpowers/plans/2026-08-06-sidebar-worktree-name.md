# Sidebar Worktree Name Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Name the worktree a pane lives in on its sidebar row, in place of today's bare `wt` marker, without adding a single git invocation.

**Architecture:** `gitinfo.Probe` already computes `LinkedWorktree` as `gitDir != commonDir`. For a linked worktree git's per-checkout dir *is* `<common>/worktrees/<name>`, so `filepath.Base(gitDir)` is the worktree's name — free, at the line that already makes the decision, and computed on the machine that owns the disk. The name rides the existing workspace broadcast as one more `git_*` key, exactly like `git_branch`. `gitRow` renders it instead of `wt`, suppressed when it is merely the branch with separators swapped, since the row is 22 cells and that case adds nothing.

**Tech Stack:** Go 1.25, Lipgloss v2.

## Global Constraints

- Go files use tabs. `gofmt` is mandatory. The working copy is CRLF on Windows — judge formatting from `git show`'d blobs, never from `gofmt -l` on the checkout.
- Build and test through Docker only: `./scripts/dev.sh test`, `./scripts/dev.sh vet`, `./scripts/dev.sh test-race`.
- Never touch `~/.quil/` or the production daemon. Dev mode only.
- **No new git invocation and no new syscall.** `internal/gitinfo`'s three plumbing calls per checkout are a fixed budget; `git status --porcelain` is deliberately excluded because it is the one call that takes seconds on a large repository without fsmonitor. The name comes from a path already in hand.
- **The name is derived on the daemon, never on the client.** Path separators belong to the machine holding the disk — a Windows daemon's `C:\repo\.git\worktrees\feat-x` split by a Linux client's `filepath.Base` returns the whole string. This is the same rule `remote-dialogs.md` states for the browse `Child` join.
- Every remote-sourced string is passed through `sanitizeRemoteText` **at render**, before any width measurement. `lipgloss.Width` measures an escape as zero cells, so a truncation is not a sanitiser.
- `gitRow` must return a string measuring **exactly** `w` cells. `renderSidebar`'s closing `.Width(w)` wraps rather than cuts, and `sidebarRowAt` maps screen row `y` to `rows[y-1]` — a row one cell too wide shifts every row below it and breaks the hit test.
- Git state is runtime-only, never persisted: a name from a previous daemon run describes a checkout nobody has re-probed.

---

### Task 1: Carry the worktree name through `gitinfo`

**Files:**
- Modify: `internal/gitinfo/gitinfo.go` (the `Info` struct ~line 28, `Probe` ~line 123)
- Test: `internal/gitinfo/gitinfo_test.go` (extend)

**Interfaces:**
- Consumes: `Dirs(ctx, dir) (gitDir, commonDir string, ok bool)`.
- Produces: `Info.WorktreeName string` — Task 2 reads it in the daemon, Task 3 renders it.

**Context an implementer cannot infer:** the name must be set **inside `Probe`**, not only at the cache's placeholder site. `gitcache.go:209` assigns `e.info, e.repo, e.at, e.headMtime, e.stale = info, true, now, head, false` — a wholesale struct replacement — so anything the cache seeds at `gitcache.go:163` and `Probe` does not reproduce is silently dropped on the first successful probe. That is precisely why `Probe` already recomputes `LinkedWorktree` from its own `Dirs` call rather than trusting the caller. Task 2 sets the placeholder too, so the name is right during the window before the first probe lands; both sites are required and neither is redundant.

- [ ] **Step 1: Write the failing test**

Append to `internal/gitinfo/gitinfo_test.go`:

```go
// git's per-checkout git dir for a linked worktree IS <common>/worktrees/<name>,
// so the name needs no command of its own. This is the entire reason the
// feature costs nothing.
func TestProbe_NamesTheLinkedWorktree(t *testing.T) {
	repo := initRepo(t)
	wt := addWorktree(t, repo, "feat-x")

	got, ok := Probe(context.Background(), wt)
	if !ok {
		t.Fatal("Probe failed on a linked worktree")
	}
	if !got.LinkedWorktree {
		t.Fatal("LinkedWorktree false for a linked worktree")
	}
	if got.WorktreeName != "feat-x" {
		t.Errorf("WorktreeName = %q, want \"feat-x\"", got.WorktreeName)
	}
}

// The MAIN checkout has no worktree name. Reporting one would put a
// directory name on every ordinary pane's row, which is noise on the rows
// that are not the point of the feature.
func TestProbe_MainCheckoutHasNoWorktreeName(t *testing.T) {
	repo := initRepo(t)

	got, ok := Probe(context.Background(), repo)
	if !ok {
		t.Fatal("Probe failed on the main checkout")
	}
	if got.WorktreeName != "" {
		t.Errorf("WorktreeName = %q for the main checkout, want empty", got.WorktreeName)
	}
}
```

Reuse the package's existing repo fixture helpers. If `addWorktree` does not exist beside `initRepo`, add it there:

```go
// addWorktree creates a linked worktree named n off repo's HEAD and returns
// its path. Uses the real git binary, like the rest of this file's fixtures —
// the whole claim under test is about the layout git itself creates.
func addWorktree(t *testing.T, repo, n string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), n)
	cmd := exec.Command("git", "worktree", "add", "-b", n, path)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return path
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/gitinfo/ -run TestProbe_`
Expected: FAIL to compile — `got.WorktreeName undefined`.

- [ ] **Step 3: Add the field and populate it**

In `internal/gitinfo/gitinfo.go`, inside `Info` beside `LinkedWorktree`:

```go
	// WorktreeName names the linked worktree, empty on the main checkout.
	//
	// Taken from the per-checkout git dir's basename, which git spells
	// <common>/worktrees/<name> — so this costs no command of its own. It is
	// git's own identifier for the worktree rather than the checkout
	// directory's basename, and the two can differ when git disambiguates a
	// name collision. The identifier is the more useful of the two: it is
	// what `git worktree list` and `git worktree remove` accept.
	WorktreeName string
```

In `Probe`, replace line 123:

```go
	info := Info{LinkedWorktree: gitDir != commonDir}
	if info.LinkedWorktree {
		// Set HERE and not only at the cache's placeholder site: gitcache
		// replaces the whole Info struct with the probe result, so a field
		// the cache seeds and Probe does not reproduce is dropped on the
		// first successful probe. Same reason LinkedWorktree is recomputed
		// from this function's own Dirs call.
		info.WorktreeName = filepath.Base(gitDir)
	}
```

Add `"path/filepath"` to the imports.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/gitinfo/`
Expected: PASS, including the pre-existing `TestProbe_LinkedWorktree`.

- [ ] **Step 5: Commit**

```bash
git add internal/gitinfo/gitinfo.go internal/gitinfo/gitinfo_test.go
git commit -F - <<'EOF'
feat(gitinfo): report the linked worktree's name

git spells a linked checkout's git dir <common>/worktrees/<name>, so the
name is already in hand at the line that decides LinkedWorktree — no
extra command, no extra syscall.

Set inside Probe rather than only at the cache's placeholder site: the
cache replaces the whole Info struct with the probe result, so a field
Probe does not reproduce is dropped on the first successful probe.
EOF
```

---

### Task 2: Broadcast the name to the client

**Files:**
- Modify: `internal/daemon/gitcache.go:163` (the placeholder entry), `internal/daemon/daemon.go:2553` (the broadcast keys)
- Modify: `internal/tui/model.go:~124` (`PaneInfo`), `internal/tui/model.go:~5370` (the decode), `internal/tui/pane.go:~81` (`PaneModel`), `internal/tui/workstate.go:~482` (the copy)
- Test: `internal/daemon/gitcache_test.go` (extend), `internal/tui/workstate_test.go` (extend)

**Interfaces:**
- Consumes: `gitinfo.Info.WorktreeName` (Task 1).
- Produces: `PaneModel.GitWorktreeName string` — Task 3 renders it.

**Context an implementer cannot infer:** the copy in `workstate.go` must be **unconditional**, matching the four `git_*` fields beside it. An absent key is meaningful: a pane that leaves a worktree, or a daemon restart that has not re-probed, must clear the name rather than keep showing the last one. The existing block's own comment states this; a `if info.GitWorktreeName != "" {` guard would break it.

The daemon-side emit **is** conditional (`if info.WorktreeName != ""`), matching `git_branch` beside it — an absent key on the wire decodes to the zero value, which is what the unconditional client-side copy then applies.

- [ ] **Step 1: Write the failing tests**

In `internal/daemon/gitcache_test.go`:

```go
// The placeholder entry — what the cache holds between resolving a CWD and
// the first probe answering — must name the worktree too. Without it the
// sidebar shows a nameless worktree for one tick on every new pane, which is
// the tick the user is actually looking at after creating one.
func TestGitCache_PlaceholderNamesTheWorktree(t *testing.T) {
	c := newGitCache()
	gitDirsFn = func(ctx context.Context, cwd string) (string, string, bool) {
		return "/repo/.git/worktrees/feat-x", "/repo/.git", true
	}
	t.Cleanup(func() { gitDirsFn = gitinfo.Dirs })
	gitProbeFn = func(ctx context.Context, dir string) (gitinfo.Info, bool) {
		return gitinfo.Info{}, false // probe never answers; placeholder stands
	}
	t.Cleanup(func() { gitProbeFn = gitinfo.Probe })

	c.refresh(context.Background(), []string{"/wt/feat-x"})

	info, ok, _ := c.lookup("/wt/feat-x")
	if !ok {
		t.Fatal("no cache entry for the worktree cwd")
	}
	if info.WorktreeName != "feat-x" {
		t.Errorf("WorktreeName = %q, want \"feat-x\"", info.WorktreeName)
	}
}
```

Adapt the seam names to whatever `gitcache_test.go` already uses — the point under test is the placeholder at `gitcache.go:163`, not the seam plumbing.

In `internal/tui/workstate_test.go`:

```go
// Copied unconditionally, like every other git_* field: an ABSENT key is
// meaningful. A pane that moves out of a worktree, or a daemon restart that
// has not re-probed, must CLEAR the name rather than keep showing the last
// one it had.
func TestApplyPaneInfo_ClearsTheWorktreeName(t *testing.T) {
	pane := &PaneModel{GitWorktree: true, GitWorktreeName: "feat-x"}
	applyPaneInfo(pane, PaneInfo{}) // no git keys at all

	if pane.GitWorktreeName != "" {
		t.Errorf("GitWorktreeName = %q after an empty update, want it cleared", pane.GitWorktreeName)
	}
}
```

Use whatever the real copy function is called at `workstate.go:482` — the test must drive that function, not a reimplementation.

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/daemon/ ./internal/tui/ -run "WorktreeName"`
Expected: FAIL to compile — the field does not exist yet.

- [ ] **Step 3: Seed the placeholder**

`internal/daemon/gitcache.go:163`:

```go
			c.byDir[gitDir] = &gitEntry{
				info: gitinfo.Info{
					LinkedWorktree: gitDir != commonDir,
					// Named here as well as in Probe: this entry is what
					// lookup() answers with between resolving the CWD and the
					// first probe landing, which is the tick right after a
					// pane is created in a worktree — exactly when the user
					// is looking at the row.
					WorktreeName: worktreeName(gitDir, commonDir),
				},
				repo:     true,
				probeDir: cwd,
			}
```

Add the helper beside it:

```go
// worktreeName is gitinfo's derivation, duplicated for the placeholder entry
// rather than shared through an exported function: it is one filepath.Base
// behind a comparison, and exporting it from gitinfo would invite a caller to
// derive the name from a path that did not come from Dirs.
func worktreeName(gitDir, commonDir string) string {
	if gitDir == commonDir {
		return ""
	}
	return filepath.Base(gitDir)
}
```

- [ ] **Step 4: Emit it on the wire**

`internal/daemon/daemon.go`, in the `git_worktree` branch at line 2553:

```go
					if info.LinkedWorktree {
						paneData["git_worktree"] = true
						// Conditional like git_branch: an absent key decodes
						// to the zero value on the client, where the copy is
						// unconditional and therefore clears it.
						if info.WorktreeName != "" {
							paneData["git_worktree_name"] = info.WorktreeName
						}
					}
```

- [ ] **Step 5: Decode and copy it on the client**

`internal/tui/model.go` — add `GitWorktreeName string` to `PaneInfo` beside `GitWorktree`, and decode it beside the `git_worktree` case (~line 5370):

```go
			case "git_worktree_name":
				if s, ok := v.(string); ok {
					pi.GitWorktreeName = s
				}
```

`internal/tui/pane.go` — add `GitWorktreeName string` to `PaneModel` beside `GitWorktree`.

`internal/tui/workstate.go:482` — beside `pane.GitWorktree = info.GitWorktree`:

```go
	pane.GitWorktreeName = info.GitWorktreeName
```

- [ ] **Step 6: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/daemon/ ./internal/tui/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/gitcache.go internal/daemon/daemon.go internal/tui/model.go internal/tui/pane.go internal/tui/workstate.go internal/daemon/gitcache_test.go internal/tui/workstate_test.go
git commit -F - <<'EOF'
feat(daemon): broadcast the worktree name with the pane's git state

Rides the existing workspace broadcast as one more git_* key, runtime-only
like the rest: a name from a previous daemon run describes a checkout
nobody has re-probed.

Derived daemon-side because path separators belong to the machine holding
the disk — a Windows daemon's path split by a Linux client's filepath.Base
returns the whole string.
EOF
```

---

### Task 3: Render the name on the sidebar row

**Files:**
- Modify: `internal/tui/sidebar.go` (`gitRow`)
- Test: `internal/tui/gitrow_test.go` (extend)

**Interfaces:**
- Consumes: `PaneModel.GitWorktreeName` (Task 2).
- Produces: nothing.

**Context an implementer cannot infer, and it is the design decision worth stating:** showing both the branch and the worktree name is usually redundant. The overwhelmingly common convention is a worktree named after its branch with `/` swapped for `-` (`feat/x` → `feat-x`), and at 22 cells spending eight on a restatement pushes the branch into middle-elision for nothing. The name is therefore **suppressed when it is the branch with separators swapped**, and the bare `wt` marker stands in — which is exactly the information that case has to convey. When they genuinely differ (an agent creating `wt-1` on branch `feat/refactor-sidebar`) the name is shown, because that is when it tells the user something the branch does not.

The suffix budget math is unchanged: `minGitBranchCells` still floors the branch, `truncateCells` still trims the suffix to what remains, and the row still measures exactly `w`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/gitrow_test.go`:

```go
func TestGitRow_WorktreeName(t *testing.T) {
	tests := []struct {
		name     string
		pane     PaneModel
		want     string // substring the row must contain
		wantNot  string // substring the row must NOT contain
	}{
		{
			name:    "a name that differs from the branch is shown",
			pane:    PaneModel{GitBranch: "feat/refactor", GitWorktree: true, GitWorktreeName: "wt-1"},
			want:    "wt-1",
			wantNot: " wt ",
		},
		{
			name:    "the branch with separators swapped is suppressed",
			pane:    PaneModel{GitBranch: "feat/x", GitWorktree: true, GitWorktreeName: "feat-x"},
			want:    "wt",
			wantNot: "feat-x",
		},
		{
			name:    "an identical name is suppressed",
			pane:    PaneModel{GitBranch: "hotfix", GitWorktree: true, GitWorktreeName: "hotfix"},
			want:    "wt",
			wantNot: "hotfix wt",
		},
		{
			name:    "the main checkout names nothing",
			pane:    PaneModel{GitBranch: "master"},
			want:    "master",
			wantNot: "wt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(gitRow(&tt.pane, 40))
			if !strings.Contains(got, tt.want) {
				t.Errorf("row %q does not contain %q", got, tt.want)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Errorf("row %q contains %q, which should be suppressed", got, tt.wantNot)
			}
		})
	}
}

// The row must measure EXACTLY its budget at every width. renderSidebar's
// closing .Width(w) WRAPS rather than cuts, and sidebarRowAt maps screen row
// y to rows[y-1] — so one cell of overflow shifts every row below it and the
// hit test starts returning the neighbour.
func TestGitRow_WithAWorktreeNameMeasuresExactly(t *testing.T) {
	pane := PaneModel{
		GitBranch:       "feat/some/deeply/nested/branch",
		GitWorktree:     true,
		GitWorktreeName: "a-very-long-worktree-directory-name",
		GitUpstream:     true,
		GitAhead:        12,
		GitBehind:       345,
		GitStale:        true,
	}
	for w := 10; w <= 80; w++ {
		if got := lipgloss.Width(gitRow(&pane, w)); got != w {
			t.Errorf("gitRow(w=%d) measured %d cells", w, got)
		}
	}
}

// The name arrives from a daemon the user may not control under --remote.
// sanitizeRemoteText must run BEFORE any width measurement: lipgloss measures
// an escape as zero cells, so truncation is not a sanitiser.
func TestGitRow_SanitizesTheWorktreeName(t *testing.T) {
	pane := PaneModel{
		GitBranch:       "master",
		GitWorktree:     true,
		GitWorktreeName: "ok\x1b]52;c;cGF5bG9hZA==\x07evil",
	}
	got := gitRow(&pane, 40)
	if strings.Contains(got, "\x1b]52") {
		t.Error("an OSC 52 in the worktree name survived to the rendered row")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestGitRow_`
Expected: FAIL — the name is never rendered; `TestGitRow_WorktreeName/a_name_that_differs...` fails on the missing `wt-1`.

- [ ] **Step 3: Implement the suppression rule and render**

In `internal/tui/sidebar.go`, add beside `gitRow`:

```go
// worktreeNameIsRedundant reports whether naming the worktree would restate
// the branch. The near-universal convention is a worktree named after its
// branch with the path separators swapped for hyphens (feat/x → feat-x), and
// the sidebar row is 22 cells by default: spending eight of them on a
// restatement pushes the branch into middle-elision for no information.
//
// When the two genuinely differ — an agent creating "wt-1" on branch
// "feat/refactor-sidebar" — the name is what the branch cannot tell the user,
// so it is shown.
func worktreeNameIsRedundant(name, branch string) bool {
	if name == "" || branch == "" {
		return name == ""
	}
	return name == strings.ReplaceAll(branch, "/", "-")
}
```

In `gitRow`, replace the `if pane.GitWorktree { suffix += " wt" }` block:

```go
	if pane.GitWorktree {
		// Sanitized here, before the width math below measures it: the name
		// comes from a daemon the user may not control under --remote, and
		// lipgloss measures an escape as zero cells, so a truncation is not
		// a sanitiser.
		wtName := sanitizeRemoteText(pane.GitWorktreeName)
		if worktreeNameIsRedundant(wtName, name) {
			suffix += " wt"
		} else {
			suffix += " " + wtName
		}
	}
```

Note `name` at that point is the already-sanitized branch, so the comparison is between two sanitized values.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestGitRow_`
Expected: PASS, all subtests, including the pre-existing `gitrow_test.go` cases.

- [ ] **Step 5: Mutation check**

Delete the `sanitizeRemoteText` call added in Step 3 and re-run. Expected: `TestGitRow_SanitizesTheWorktreeName` fails, and it is the only new failure. If it passes without the call, the test is not reaching the render path — fix the test before restoring the call.

Restore the call.

- [ ] **Step 6: Full package, race, vet**

Run: `./scripts/dev.sh test ./internal/tui/`, `./scripts/dev.sh test-race ./internal/tui/`, `./scripts/dev.sh vet`
Expected: PASS. The sidebar fixture tests (`sidebarRows`/`sidebarRowAt`) may shift — that is the expected cost recorded in `.claude/rules/projects.md` and they should be updated, not weakened.

- [ ] **Step 7: Manual check in dev mode**

```bash
./scripts/dev.sh build
./scripts/quil-dev.ps1
```

Create a worktree whose name differs from its branch, open a pane in it via Ctrl+N → worktree field, and confirm the sidebar names it. Create one named after its branch and confirm it shows `wt`.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/gitrow_test.go
git commit -F - <<'EOF'
feat(tui): name the worktree on the sidebar's git row

The row said "wt" — that a pane was in some worktree, not which one.

Suppressed when the name is the branch with separators swapped, which is
the near-universal convention and would spend eight of the row's 22 cells
restating the branch. Shown when the two genuinely differ, which is when
it tells the user something the branch does not.
EOF
```

---

### Task 4: Document the row

**Files:**
- Modify: `.claude/rules/projects.md` (git subsystem section), `docs/features.md` (sidebar description)

- [ ] **Step 1: Record the derivation in `.claude/rules/projects.md`**

Append to the git subsystem section:

> **The worktree's NAME costs nothing, and the reason is where it comes from.** git spells a linked checkout's per-checkout git dir `<common>/worktrees/<name>`, so `filepath.Base(gitDir)` is the name at the exact line that already computes `LinkedWorktree`. It is set in **both** `gitinfo.Probe` and the `gitcache` placeholder entry, and neither is redundant: the cache replaces the whole `Info` struct with the probe result, so a field only the placeholder sets is dropped on the first probe — while a field only `Probe` sets is missing for the tick right after a pane is created, which is the tick the user is looking at. Derived daemon-side because separators belong to the machine holding the disk. `gitRow` suppresses it when it is the branch with `/`→`-`, the near-universal convention, since restating the branch costs eight of a 22-cell row.

- [ ] **Step 2: Update `docs/features.md`**

One line in the sidebar description: a pane in a linked worktree names it beside the branch.

- [ ] **Step 3: Check the docs-size gate**

Run: `./scripts/dev.sh docs-size`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add .claude/rules/projects.md docs/features.md
git commit -F - <<'EOF'
docs: describe the sidebar's worktree name

Record why it costs no git call, why both the probe and the cache
placeholder set it, and when the row suppresses it.
EOF
```

---

## Self-Review

**Spec coverage.** The chosen scope was "worktree name + branch, no new git calls". Task 1 derives the name, Task 2 carries it, Task 3 renders it, Task 4 documents it. Zero new invocations — pinned by the fact that no task adds a call to `runGit`.

**Placeholder scan.** Every code step carries real code. Two adaptations are flagged rather than left vague: the `gitcache_test.go` seam names (Task 2 Step 1) and the `workstate.go` copy function's real name (Task 2 Step 1), both because the implementer will have those files open and the point under test is named explicitly in each.

**Type consistency.** `Info.WorktreeName string` (Task 1) → `worktreeName(gitDir, commonDir) string` and `paneData["git_worktree_name"]` (Task 2) → `PaneInfo.GitWorktreeName` → `PaneModel.GitWorktreeName` → read by `gitRow` (Task 3). One name, one type, end to end.

**Ordering note.** This plan is worth running **after** the sidebar width plan. The row gets longer, and the width control is what lets the user pay for it.
