# Worktree-Owned Panes — Stage B Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the setup dialog *create* a git worktree on a new branch and spawn the pane inside it, so an agent's work is isolated by construction rather than by asking it nicely.

**Architecture:** Stage A shipped the read half — `internal/gitworktree.List`, the `MsgWorktreeListReq/Resp` pair, and a setup-dialog field that attaches a pane to an existing worktree. Stage B adds the write half. `gitworktree.Add` already exists and is tested; what stage B builds is everything around it: branch validation, an asynchronous daemon create that cannot block a client's dispatch goroutine, a request-response failure path that unwinds the layout placeholder the TUI armed seconds earlier, persisted worktree ownership so restore can tell a missing worktree from a missing browsed directory, and an error pane for when it is gone.

**Spec:** `docs/superpowers/specs/2026-08-05-worktree-owned-panes-design.md`. This plan resolves the three decisions that spec left open — the rulings are recorded in "Decisions this plan closes" below.

**Tech Stack:** Go 1.25, Bubble Tea v2, Lipgloss v2, `internal/gitworktree` (stdlib-only).

## Decisions this plan closes

| Open question | Ruling | Why |
|---|---|---|
| Replace mode | **Refuse** — the worktree field is not rendered on the replace path at all | The spec's own honest default. The client-side replace sets `leaf.Pane = nil` and calls `old.Dispose()` *before* the send, so a failed add costs a live pane and `PrunePlaceholders` cannot undo a `Dispose()` |
| Restore with a missing worktree | **The pane comes up visibly broken**: no PTY, `SpawnError` broadcast, message rendered in the pane rect, Alt+R retries | The alternative silently continues a claude session against the main checkout. A pane whose *worktree* is gone loses its isolation; a pane whose browsed CWD is gone loses only a convenience, so today's fallback stays for that case |
| Add's timeout and permit budget | One `claimBlockingFSCall` permit, `worktreeAddTimeout = 120s`, serialised by the `worktreeAdding` single-flight | The single-flight **is** the budget: at most one add runs daemon-wide, so at most one permit is ever held long. 120 s because a large checkout legitimately takes minutes and killing it mid-write leaves a half-built worktree git must then prune |

## Global Constraints

- Go files use tabs. `gofmt` is mandatory. The working copy is CRLF on Windows — judge formatting from `git show`'d blobs, never from `gofmt -l` on the checkout.
- Build and test through Docker only: `./scripts/dev.sh test`, `./scripts/dev.sh vet`, `./scripts/dev.sh test-race`. Go is not installed on the host.
- Never touch `~/.quil/` or the production daemon. Dev mode only. Test every destructive path against a throwaway repo under `t.TempDir()`.
- `internal/gitworktree` stays **stdlib-only** and must never import `internal/gitinfo` — the separation is what keeps repository writes out of the package a ticker runs.
- `gitworktree.Add` must never pass `--force`. Every refusal git raises is a fact the user needs, and forcing past one lands a pane on top of another pane's checkout. Pinned by `TestAdd_NeverForces`.
- **A failed add creates no pane, and never falls back to the repository root.** `handleCreatePane` (`daemon.go:1600-1610`) silently substitutes `d.defaultCWD()` for a CWD that fails its stat — sound for a browsed directory, wrong for a worktree, where the directory *is* the isolation. The exemption must be explicit in the code path.
- Responses echo the request key **verbatim** on every path including errors and rejections. It is the client's staleness key, not a statement about what was read.
- The worktree RPC uses its **own** single-flight atomic (`worktreeAdding`), not `worktreeScanning`. A create runs while a listing may be in flight, and a shared slot would fail each exactly when it followed the other — the same reason `dirsChecking` is not `browseScanning`.
- Any handler doing filesystem work runs on a **worker goroutine**, never the requesting connection's dispatch goroutine.
- `pane.PTY.Close()` must never run under `sm.mu`.
- Every remote-sourced string is `sanitizeRemoteText`'d **at render**, never in state — the worktree path becomes a spawn CWD.

---

### Task 1: Validate the branch name and derive the worktree path

**Files:**
- Create: `internal/gitworktree/validate.go`
- Test: `internal/gitworktree/validate_test.go`

**Interfaces:**
- Produces:
  - `func ValidateBranch(name string) error`
  - `func DerivePath(repoRoot, branch string) string`

**Context an implementer cannot infer:** the branch name is validated **twice over**, against two different grammars, and both are needed. It reaches `git worktree add -b <branch>` as argv, so it must be a legal git ref (no leading `-`, no `..`, no space, no `~^:?*[\`, no `.lock` suffix, no trailing `/`). It *also* becomes a path segment via `DerivePath`, so it must not be able to escape its parent directory (`..`, absolute paths, drive letters, or a `/` producing nested directories). A name legal as a ref can still be dangerous as a path — `feat/x` is a perfectly good branch and a two-segment path — which is why the derivation swaps separators rather than rejecting them.

`DerivePath` implements the spec's sibling-directory decision: `<parent-of-repo>/<repo-name>-worktrees/<branch-with-separators-swapped>`.

- [ ] **Step 1: Write the failing tests**

```go
package gitworktree

import (
	"path/filepath"
	"strings"
	"testing"
)

// The name reaches `git worktree add -b <branch>` as argv AND becomes a path
// segment. Both grammars are checked, because a name legal as one can be
// dangerous as the other.
func TestValidateBranch(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"ordinary name", "feat-x", false},
		{"slashes are legal in a ref", "feat/x", false},
		{"digits and dots inside", "release-1.2.3", false},
		{"empty", "", true},
		{"leading dash reads as a flag", "-b", true},
		{"double dot", "feat/../x", true},
		{"space", "feat x", true},
		{"tilde", "feat~1", true},
		{"caret", "feat^", true},
		{"colon", "feat:x", true},
		{"question mark", "feat?", true},
		{"asterisk", "feat*", true},
		{"open bracket", "feat[x", true},
		{"backslash", "feat\\x", true},
		{"lock suffix", "feat.lock", true},
		{"trailing slash", "feat/", true},
		{"leading slash", "/feat", true},
		{"double slash", "feat//x", true},
		{"lone dot", ".", true},
		{"parent", "..", true},
		{"control character", "feat\x01x", true},
		{"newline", "feat\nx", true},
		{"absolute windows path", "C:/evil", true},
		{"leading dot segment", ".hidden", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranch(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranch(%q) err = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

// The sibling location keeps the repo tree clean: no .gitignore entry, and no
// tool that walks the tree finds a second full checkout nested in the first.
func TestDerivePath_SiblingDirectory(t *testing.T) {
	got := DerivePath(filepath.Join("E:", "Projects", "quil"), "feat/x")
	want := filepath.Join("E:", "Projects", "quil-worktrees", "feat-x")
	if got != want {
		t.Errorf("DerivePath = %q, want %q", got, want)
	}
}

// Separators are SWAPPED, not rejected: feat/x is an ordinary branch name and
// must not produce a nested directory under the worktrees parent.
func TestDerivePath_FlattensSeparators(t *testing.T) {
	got := DerivePath(filepath.Join("/home", "u", "repo"), "feat/deep/nested")
	if strings.Count(strings.TrimPrefix(got, filepath.Join("/home", "u", "repo-worktrees")), string(filepath.Separator)) != 1 {
		t.Errorf("DerivePath = %q, want a single flat segment under the worktrees parent", got)
	}
	if !strings.HasSuffix(got, "feat-deep-nested") {
		t.Errorf("DerivePath = %q, want it to end in the flattened branch name", got)
	}
}

// Every name ValidateBranch accepts must derive a path that stays inside the
// worktrees parent. This is the property, and the table above is only a
// sample of it — an escape here puts a checkout somewhere the user never named.
func TestDerivePath_CannotEscapeTheParent(t *testing.T) {
	repo := filepath.Join("/home", "u", "repo")
	parent := filepath.Join("/home", "u", "repo-worktrees")
	for _, branch := range []string{"feat-x", "feat/x", "release-1.2.3", "a", "x/y/z"} {
		if err := ValidateBranch(branch); err != nil {
			t.Fatalf("fixture %q is not accepted: %v", branch, err)
		}
		got := DerivePath(repo, branch)
		rel, err := filepath.Rel(parent, got)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			t.Errorf("DerivePath(%q) = %q escapes %q (rel=%q, err=%v)", branch, got, parent, rel, err)
		}
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/gitworktree/ -run "TestValidateBranch|TestDerivePath"`
Expected: FAIL to compile — `ValidateBranch` and `DerivePath` undefined.

- [ ] **Step 3: Implement**

```go
package gitworktree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// branchRejected lists the bytes git refuses in a ref name, plus the ones that
// are legal in a ref and dangerous in a path.
const branchRejected = " ~^:?*[\\\x7f"

// ValidateBranch rejects a name that cannot safely be BOTH a git ref and a
// path segment.
//
// Two grammars, deliberately. The name reaches `git worktree add -b <branch>`
// as argv — so a leading dash reads as a flag, and git's own ref rules bar the
// rest — and it also becomes a path segment via DerivePath, so it must not be
// able to reach outside its parent directory. A name legal as one can be
// dangerous as the other, which is why neither check subsumes this one.
//
// Slashes are ACCEPTED: feat/x is an ordinary branch name. DerivePath flattens
// them rather than this function rejecting them.
func ValidateBranch(name string) error {
	if name == "" {
		return fmt.Errorf("branch name is empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch name may not start with %q — it would read as a flag", "-")
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("branch name may not start or end with %q", "/")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("branch name may not start with %q", ".")
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("branch name may not contain %q", "//")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name may not contain %q", "..")
	}
	if strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("branch name may not end in %q", ".lock")
	}
	if filepath.IsAbs(name) || strings.Contains(name, ":") {
		return fmt.Errorf("branch name may not be a path")
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(branchRejected, r) {
			return fmt.Errorf("branch name may not contain %q", r)
		}
	}
	return nil
}

// DerivePath returns where a worktree for branch belongs: a SIBLING of the
// repository, at <parent>/<repo>-worktrees/<flattened-branch>.
//
// Sibling rather than nested so the repo tree stays clean — no .gitignore
// entry, and no tool that walks the tree (ripgrep, file watchers, the agent
// itself) finds a second full checkout inside the first.
//
// Separators are flattened rather than preserved: feat/x is an ordinary branch
// name, and honouring it as a path would nest directories under the worktrees
// parent, so two branches could collide on an intermediate segment. Callers
// must have run ValidateBranch first — this function assumes the name cannot
// escape.
func DerivePath(repoRoot, branch string) string {
	parent := filepath.Dir(repoRoot)
	name := filepath.Base(repoRoot) + "-worktrees"
	seg := strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(parent, name, seg)
}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/gitworktree/`
Expected: PASS, including stage A's existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/gitworktree/validate.go internal/gitworktree/validate_test.go
git commit -F - <<'EOF'
feat(gitworktree): validate branch names and derive worktree paths

The name is checked against two grammars because it is used as two things:
argv for `git worktree add -b` and a path segment. A name legal as a ref
can still escape its directory, so neither check subsumes the other.

Paths land in a sibling <repo>-worktrees directory, keeping a second full
checkout out of the tree that ripgrep, watchers and the agent all walk.
EOF
```

---

### Task 2: Extend the wire protocol

**Files:**
- Modify: `internal/ipc/protocol.go`
- Test: `internal/ipc/protocol_test.go` (extend)

**Interfaces:**
- Produces:
  - `type WorktreeSpec struct { RepoRoot, Branch string }`
  - `CreatePanePayload.Worktree *WorktreeSpec` (`omitempty`)
  - `MsgCreatePaneResp` + `CreatePaneRespPayload{ PaneID, Error string, Worktree *WorktreeSpec }`

**Context an implementer cannot infer:** `Worktree` is a **pointer** and `omitempty`. A nil pointer is what makes every existing create — MCP `create_pane`, the plugin dialog, restore — take the unchanged synchronous path with no branch anywhere in the daemon. A value type with a zero-value check would work too but would put an empty struct on every create's wire frame; the pointer says "this create is different" structurally.

The response echoes the `Worktree` spec **verbatim**, on the error path as well as on success. The TUI matches its pending create on it.

- [ ] **Step 1: Write the failing test**

```go
// A create with no worktree spec must be wire-identical to today's, or every
// existing client — MCP, the plugin dialog, restore — changes behaviour.
func TestCreatePanePayload_NoWorktreeIsWireIdentical(t *testing.T) {
	got, err := json.Marshal(CreatePanePayload{TabID: "t1", Type: "terminal", CWD: "/x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(got, []byte("worktree")) {
		t.Errorf("payload carries a worktree key with no spec set: %s", got)
	}
}

// The response echoes the spec VERBATIM on the error path too: it is the
// client's staleness key, and the client has a layout placeholder armed
// against it that must be unwound.
func TestCreatePaneResp_EchoesTheSpecOnFailure(t *testing.T) {
	spec := &WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"}
	raw, err := json.Marshal(CreatePaneRespPayload{Error: "already exists", Worktree: spec})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CreatePaneRespPayload
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Worktree == nil || back.Worktree.Branch != "feat/x" || back.Worktree.RepoRoot != "/repo" {
		t.Errorf("spec not echoed verbatim: %+v", back.Worktree)
	}
	if back.PaneID != "" {
		t.Error("a failed create must carry no pane id")
	}
}
```

- [ ] **Step 2: Run the test and watch it fail**

Run: `./scripts/dev.sh test ./internal/ipc/ -run "TestCreatePane"`
Expected: FAIL to compile — the types do not exist.

- [ ] **Step 3: Add the types**

```go
// WorktreeSpec asks the daemon to CREATE a linked worktree and spawn the pane
// inside it. Create-time only — it is an instruction, not stored pane state;
// the resulting CWD is what persists.
type WorktreeSpec struct {
	// RepoRoot is the repository the worktree branches from, as the daemon's
	// own filesystem spells it.
	RepoRoot string `json:"repo_root"`
	// Branch is the NEW branch, off the repository's current HEAD. Existing
	// branches are deliberately not offered: one already checked out in
	// another worktree fails at the git level and needs its own error path,
	// which attaching to that worktree covers instead.
	Branch string `json:"branch"`
}

// CreatePaneRespPayload answers a create that carried a WorktreeSpec.
//
// Only such creates get a response: an ordinary create is synchronous and its
// result arrives in the next workspace broadcast, as it always has. This pair
// exists because a worktree add takes seconds, during which the TUI is holding
// a layout placeholder it must unwind if the add fails.
type CreatePaneRespPayload struct {
	// PaneID is set on success, empty on failure.
	PaneID string `json:"pane_id,omitempty"`
	// Error carries git's own stderr where there is any — "already used by
	// worktree '/x/feat-y'" names the pane to go look at, and no message
	// Quil could invent would.
	Error string `json:"error,omitempty"`
	// Worktree echoes the request's spec VERBATIM on every path, including
	// this one. It is the client's staleness key, not a statement about what
	// was created.
	Worktree *WorktreeSpec `json:"worktree,omitempty"`
}
```

Add `Worktree *WorktreeSpec \`json:"worktree,omitempty"\`` to `CreatePanePayload`, and `MsgCreatePaneResp = "create_pane_resp"` to the message-type constants.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/ipc/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -F - <<'EOF'
feat(ipc): carry a worktree spec on create_pane

The spec is a pointer so a create without one stays wire-identical, and
every existing client keeps the synchronous path.

Adds create_pane_resp, needed only by worktree creates: the add takes
seconds, and the client holds a layout placeholder it must unwind if the
add fails.
EOF
```

---

### Task 3: Create the worktree on a worker, and never relocate on failure

**Files:**
- Create: `internal/daemon/worktree_add.go`, `internal/daemon/worktree_add_test.go`
- Modify: `internal/daemon/daemon.go` (`worktreeAdding atomic.Bool`; the `handleCreatePane` branch)

**Interfaces:**
- Consumes: `gitworktree.ValidateBranch`, `gitworktree.DerivePath`, `gitworktree.Add`, `claimBlockingFSCall`/`releaseBlockingFSCall`, `respondTo`.
- Produces: `func (d *Daemon) beginWorktreeAdd() bool`, `func (d *Daemon) worktreeAddAndCreate(req ipc.Message) ipc.CreatePaneRespPayload`.

**Context an implementer cannot infer — three things, each of which is a shipped-bug class in this codebase:**

1. `handleCreatePane` runs on the requesting connection's **dispatch goroutine**. A `git worktree add` there blocks every message from that client, input included — the hazard that moved `browse`, `discover` and `claudesessions` onto workers.
2. `handleCreatePane` (`daemon.go:1600-1610`) silently substitutes `d.defaultCWD()` for a CWD that fails its stat. Correct for a browsed directory, catastrophic for a worktree, where the directory *is* the isolation: the pane would come up on `master` while the user believes it is isolated. The exemption must be **explicit**, because the fallback is what the surrounding code does by default.
3. The target tab can be destroyed during the seconds the add runs. Re-validate it before creating the pane, or the pane lands in a tab nobody is looking at.

The single-flight is the permit budget: one add at a time daemon-wide means at most one permit held for up to `worktreeAddTimeout`.

- [ ] **Step 1: Write the failing tests**

```go
package daemon

// The whole point of the feature: an add that fails must produce NO pane and
// must never relocate to the repository root. A pane on master that the user
// believes is isolated is the confidently-wrong answer this design exists to
// remove.
func TestWorktreeAdd_FailureCreatesNoPaneAndDoesNotRelocate(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	before := len(d.session.Panes(tab.ID))

	addFn = func(ctx context.Context, repo, path, branch string) error {
		return errors.New("fatal: '/x/feat-y' already exists")
	}
	t.Cleanup(func() { addFn = gitworktree.Add })

	resp := d.worktreeAddAndCreate(createReq(tab.ID, "/repo", "feat/x"))

	if resp.Error == "" {
		t.Error("a failed add reported no error")
	}
	if resp.PaneID != "" {
		t.Errorf("a failed add created pane %q", resp.PaneID)
	}
	if got := len(d.session.Panes(tab.ID)); got != before {
		t.Errorf("pane count %d, want %d — a failed add created a pane anyway", got, before)
	}
}

// The spec is echoed verbatim on the failure path: the client matches its
// armed layout placeholder on it, and a dropped echo leaves the placeholder
// armed forever.
func TestWorktreeAdd_EchoesTheSpecOnEveryPath(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	addFn = func(ctx context.Context, repo, path, branch string) error { return errors.New("nope") }
	t.Cleanup(func() { addFn = gitworktree.Add })

	resp := d.worktreeAddAndCreate(createReq(tab.ID, "/repo", "feat/x"))
	if resp.Worktree == nil || resp.Worktree.Branch != "feat/x" || resp.Worktree.RepoRoot != "/repo" {
		t.Errorf("spec not echoed: %+v", resp.Worktree)
	}
}

// Validation runs BEFORE any repository write, so a bad name costs no git
// invocation and cannot reach argv.
func TestWorktreeAdd_RejectsABadBranchWithoutRunningGit(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	called := false
	addFn = func(ctx context.Context, repo, path, branch string) error { called = true; return nil }
	t.Cleanup(func() { addFn = gitworktree.Add })

	resp := d.worktreeAddAndCreate(createReq(tab.ID, "/repo", "-b"))
	if resp.Error == "" {
		t.Error("a flag-shaped branch name was accepted")
	}
	if called {
		t.Error("git ran for a name that failed validation")
	}
}

// A tab destroyed while the add ran must not receive a pane. The window is
// seconds wide here, not the milliseconds an ordinary create has.
func TestWorktreeAdd_RefusesAVanishedTab(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	addFn = func(ctx context.Context, repo, path, branch string) error {
		d.session.DestroyTab(tab.ID) // the tab goes while the add runs
		return nil
	}
	t.Cleanup(func() { addFn = gitworktree.Add })

	resp := d.worktreeAddAndCreate(createReq(tab.ID, "/repo", "feat/x"))
	if resp.Error == "" {
		t.Error("a create into a destroyed tab was accepted")
	}
	if resp.PaneID != "" {
		t.Errorf("pane %q created in a destroyed tab", resp.PaneID)
	}
}

// worktreeAdding is its OWN slot. A create runs while a listing may be in
// flight — the dialog lists, then creates — and a shared guard would fail each
// exactly when it followed the other.
func TestWorktreeAdd_SlotIsIndependentOfTheListingSlot(t *testing.T) {
	d := newTestDaemon(t)
	d.worktreeScanning.Store(true) // a listing is in flight
	if !d.beginWorktreeAdd() {
		t.Error("an add was refused while only a LISTING held its slot")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/daemon/ -run TestWorktreeAdd`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the worker**

`internal/daemon/worktree_add.go`:

```go
package daemon

// worktreeAddTimeout bounds one `git worktree add`.
//
// Far longer than gitProbeTimeout, deliberately: an add CHECKS OUT A TREE, and
// on a large repository that legitimately takes minutes. Killing it partway
// leaves a half-written worktree git must then prune, so a tight budget trades
// a slow success for a mess.
//
// Holding a blocking-FS permit that long would normally be the pool-drain
// hazard sweepRoots documents — but the worktreeAdding single-flight IS the
// budget here: at most one add runs daemon-wide, so at most one permit is ever
// held long, however many clients ask.
const worktreeAddTimeout = 120 * time.Second

// addFn is the seam tests replace. Package-var rather than a field so the
// handler's own claim path is exercised.
var addFn = gitworktree.Add

// beginWorktreeAdd claims the add slot. Its OWN atomic, not worktreeScanning:
// the dialog LISTS a directory's worktrees and then CREATES one, so a shared
// guard would reject each step exactly when it followed the other — the same
// reason dirsChecking is not browseScanning.
func (d *Daemon) beginWorktreeAdd() bool {
	return d.worktreeAdding.CompareAndSwap(false, true)
}

func (d *Daemon) endWorktreeAdd() { d.worktreeAdding.Store(false) }

// worktreeAddAndCreate validates, creates the worktree, and spawns the pane
// inside it. Runs on a WORKER goroutine — handleCreatePane runs on the
// requesting conn's dispatch goroutine, where an add would block every message
// from that client, input included.
//
// Every failure path returns an error and creates NO pane. There is
// deliberately no fallback to the repository root: that spawns an agent on
// master while the user believes it is isolated, which is the exact
// confidently-wrong answer this design exists to remove.
func (d *Daemon) worktreeAddAndCreate(req ipc.Message) ipc.CreatePaneRespPayload {
	var p ipc.CreatePanePayload
	if err := ipc.DecodePayload(req.Payload, &p); err != nil {
		return ipc.CreatePaneRespPayload{Error: "malformed create request"}
	}
	// Echoed on EVERY return below, including the error ones: the client
	// matches its armed layout placeholder on this, so a dropped echo leaves
	// the placeholder armed forever.
	spec := p.Worktree
	fail := func(format string, args ...any) ipc.CreatePaneRespPayload {
		return ipc.CreatePaneRespPayload{Error: fmt.Sprintf(format, args...), Worktree: spec}
	}
	if spec == nil {
		return fail("no worktree spec")
	}
	// Validated BEFORE any repository write, so a bad name costs no git
	// invocation and never reaches argv.
	if err := gitworktree.ValidateBranch(spec.Branch); err != nil {
		return fail("%v", err)
	}
	if spec.RepoRoot == "" {
		return fail("no repository given")
	}
	if !d.beginWorktreeAdd() {
		return fail("another worktree is being created — try again in a moment")
	}
	defer d.endWorktreeAdd()

	if !claimBlockingFSCall() {
		return fail("the daemon is busy with filesystem work — try again in a moment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeAddTimeout)
	path := gitworktree.DerivePath(spec.RepoRoot, spec.Branch)
	err := addFn(ctx, spec.RepoRoot, path, spec.Branch)
	cancel()
	releaseBlockingFSCall()
	if err != nil {
		// git's own stderr is attached by gitworktree.Add — "already used by
		// worktree '/x/feat-y'" names the pane to go look at.
		return fail("%v", err)
	}

	// Re-validated AFTER the add: the window is seconds wide here, not the
	// milliseconds an ordinary create has, and a tab can be destroyed inside
	// it. Without this the pane lands in a tab nobody is looking at.
	if !d.session.TabExists(p.TabID) {
		return fail("the tab was closed while the worktree was being created")
	}

	pane, err := d.createPaneInWorktree(p, path)
	if err != nil {
		return fail("%v", err)
	}
	return ipc.CreatePaneRespPayload{PaneID: pane.ID, Worktree: spec}
}
```

`createPaneInWorktree` reuses `handleCreatePane`'s pane-construction body with **two** differences, and the plan is explicit that both are required:

```go
// createPaneInWorktree builds the pane with the worktree as its CWD.
//
// It does NOT reuse handleCreatePane's cwd sanity block. That block substitutes
// d.defaultCWD() whenever the stat fails — a sound convention for a browsed
// directory, where a stale path should not cost the user a pane, and exactly
// the wrong one here, where the directory IS the isolation. A worktree path
// has just been created by this daemon, so its absence is a real failure, not
// stale user input.
//
// It also marks the pane WorktreeOwned, which is what lets restore tell a
// missing worktree from a missing browsed directory (see spawnRestoredPane).
func (d *Daemon) createPaneInWorktree(p ipc.CreatePanePayload, path string) (*Pane, error) {
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("worktree %s is not there after creating it: %w", path, err)
	}
	p.CWD = path
	pane, err := d.createPaneCommon(p)
	if err != nil {
		return nil, err
	}
	pane.PluginMu.Lock()
	pane.WorktreeOwned = true
	pane.PluginMu.Unlock()
	return pane, nil
}
```

Extract `createPaneCommon` from `handleCreatePane`'s existing body (everything after the CWD block through the pane spawn and snapshot request) so both paths share one construction. Do not copy it.

- [ ] **Step 4: Add the state and the dispatch branch**

In `Daemon`, beside `worktreeScanning`:

```go
	// worktreeAdding serialises worktree CREATION. Its own slot — see
	// beginWorktreeAdd — and it doubles as the permit budget: one add at a
	// time means one long-held blocking-FS permit at most.
	worktreeAdding atomic.Bool
```

In `handleCreatePane`, at the very top, after the replace-mode dispatch:

```go
	// A create carrying a worktree spec goes to a WORKER: git worktree add
	// checks out a tree, and this function runs on the requesting conn's
	// dispatch goroutine, where seconds of that block the client's input.
	if p.Worktree != nil {
		go func() {
			resp := d.worktreeAddAndCreate(msg)
			d.respondTo(conn, msg.ID, ipc.MsgCreatePaneResp, resp)
		}()
		return
	}
```

- [ ] **Step 5: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/daemon/ -run TestWorktreeAdd`
Expected: PASS.

- [ ] **Step 6: Mutation check the no-relocation guarantee**

Replace `createPaneInWorktree`'s stat guard with `handleCreatePane`'s fallback block (`if stat fails { path = "" }`) and re-run. Expected: `TestWorktreeAdd_FailureCreatesNoPaneAndDoesNotRelocate` fails. If it passes, the test is not reaching the guard — fix the test before restoring the code.

Restore the guard.

- [ ] **Step 7: Full package, race, vet**

Run: `./scripts/dev.sh test ./internal/daemon/`, `./scripts/dev.sh test-race ./internal/daemon/`, `./scripts/dev.sh vet`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/worktree_add.go internal/daemon/worktree_add_test.go internal/daemon/daemon.go
git commit -F - <<'EOF'
feat(daemon): create a git worktree and spawn the pane inside it

The add runs on a worker: handleCreatePane runs on the requesting conn's
dispatch goroutine, and a checkout there blocks that client's input for
seconds.

A failed add creates no pane and never relocates. The surrounding create
path substitutes the daemon's default directory for a CWD that fails its
stat, which is right for a browsed path and wrong for a worktree, where
the directory is the isolation — so the worktree path is exempt from it
explicitly.

One add at a time daemon-wide, which is also the permit budget: at most
one blocking-FS permit is held for the add's long timeout.
EOF
```

---

### Task 4: Persist ownership; bring a pane with a missing worktree up visibly broken

**Files:**
- Modify: `internal/daemon/session.go` (`Pane.WorktreeOwned`, `Pane.SpawnError`), `internal/daemon/daemon.go` (`spawnRestoredPane`, the broadcast keys, the snapshot), `internal/persist/snapshot.go` if the pane record is typed there
- Modify: `internal/tui/pane.go`, `internal/tui/workstate.go`, `internal/tui/pane_render.go` (or wherever pane content is composed)
- Test: `internal/daemon/worktree_restore_test.go`, `internal/tui/pane_spawnerror_test.go`

**Interfaces:**
- Consumes: `Pane.WorktreeOwned` (Task 3).
- Produces: `Pane.SpawnError string` (runtime-only), `PaneModel.SpawnError string`.

**Context an implementer cannot infer:** `WorktreeOwned` is **persisted**; `SpawnError` is **not**. Ownership is a durable fact about how the pane was made and is the only thing that lets restore distinguish a missing worktree from a missing browsed directory — the spec notes the snapshot stores only CWD today, which is exactly why this case is undetectable. The error is a runtime observation a fresh daemon re-derives by stat'ing again, and persisting it would resurrect a stale complaint about a worktree the user has since restored.

Today's blank-and-fall-back behaviour **stays** for non-worktree panes. A pane whose browsed CWD vanished loses a convenience; a pane whose worktree vanished loses its isolation, and for a claude pane it is worse than a wrong directory — it still resumes its recorded session, so the conversation continues against the wrong tree.

`SpawnError` is a broadcast pane field like `git_branch`, and the TUI copies it unconditionally so a successful retry clears it.

- [ ] **Step 1: Write the failing tests**

```go
// A worktree-owned pane whose directory is gone must come up VISIBLY BROKEN,
// not quietly relocated. Blanking the CWD reaches d.defaultCWD(), so the pane
// would return in the main checkout on whatever branch that is — and a claude
// pane resumes its recorded session there, continuing the conversation against
// the wrong tree.
func TestSpawnRestoredPane_WorktreeGoneComesUpBroken(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "p1", CWD: filepath.Join(t.TempDir(), "vanished"), WorktreeOwned: true, Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.CWD == "" {
		t.Error("the worktree CWD was blanked — the pane would relocate to the daemon default")
	}
	if pane.SpawnError == "" {
		t.Error("no SpawnError set — the failure would be in a log nobody reads")
	}
	if pane.PTY != nil {
		t.Error("a PTY was spawned for a pane whose worktree is gone")
	}
}

// A pane whose ORDINARY browsed CWD is gone keeps today's behaviour. The two
// cases are not the same loss: a stale browsed path costs a convenience, a
// missing worktree costs the isolation the pane exists for.
func TestSpawnRestoredPane_OrdinaryMissingCWDStillFallsBack(t *testing.T) {
	d := newTestDaemon(t)
	pane := &Pane{ID: "p2", CWD: filepath.Join(t.TempDir(), "vanished"), Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.CWD != "" {
		t.Errorf("CWD = %q, want it blanked so defaultCWD applies", pane.CWD)
	}
	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q — an ordinary stale CWD is not an error state", pane.SpawnError)
	}
}

// A worktree-owned pane whose directory is STILL THERE restores normally.
func TestSpawnRestoredPane_WorktreePresentRestoresNormally(t *testing.T) {
	d := newTestDaemon(t)
	dir := t.TempDir()
	pane := &Pane{ID: "p3", CWD: dir, WorktreeOwned: true, Type: "terminal"}

	d.spawnRestoredPane(pane)

	if pane.SpawnError != "" {
		t.Errorf("SpawnError = %q for a worktree that is present", pane.SpawnError)
	}
	if pane.CWD != dir {
		t.Errorf("CWD = %q, want %q", pane.CWD, dir)
	}
}

// Ownership is PERSISTED — it is the only thing that lets restore tell the two
// cases apart, and the snapshot stores only CWD without it.
func TestSnapshot_PersistsWorktreeOwnership(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane := d.session.CreatePane(tab.ID, "terminal", "/wt")
	pane.WorktreeOwned = true

	snap := d.snapshot()
	restored := reloadSnapshot(t, snap)

	if !restored.pane(pane.ID).WorktreeOwned {
		t.Error("WorktreeOwned did not survive the snapshot round-trip")
	}
}

// The ERROR is not persisted: a fresh daemon re-stats and re-derives it, and a
// stored one would resurrect a complaint about a worktree since restored.
func TestSnapshot_DoesNotPersistSpawnError(t *testing.T) {
	d := newTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane := d.session.CreatePane(tab.ID, "terminal", "/wt")
	pane.SpawnError = "worktree is gone"

	if bytes.Contains(mustMarshal(t, d.snapshot()), []byte("worktree is gone")) {
		t.Error("SpawnError was persisted")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/daemon/ -run "TestSpawnRestoredPane|TestSnapshot_"`
Expected: FAIL to compile — the fields do not exist.

- [ ] **Step 3: Add the fields**

In `Pane` (`session.go`), inside the `PluginMu`-protected group:

```go
	// WorktreeOwned marks a pane created into a worktree Quil made for it.
	// PERSISTED, and it is the only thing that lets restore tell a missing
	// worktree from a missing browsed directory — the snapshot stores just
	// CWD otherwise, so the two are indistinguishable and the wrong recovery
	// applies to one of them.
	WorktreeOwned bool
	// SpawnError explains why this pane has no process. Runtime-only and
	// deliberately NOT persisted: a fresh daemon re-stats and re-derives it,
	// while a stored one resurrects a complaint about a worktree the user has
	// since restored.
	SpawnError string
```

- [ ] **Step 4: Branch the restore path**

Replace the CWD sanity block in `spawnRestoredPane` (`daemon.go:838-849`):

```go
	if pane.CWD != "" {
		if info, err := os.Stat(pane.CWD); err != nil || !info.IsDir() {
			if pane.WorktreeOwned {
				// A worktree-owned pane does NOT relocate. Blanking the CWD
				// reaches d.defaultCWD(), so the pane would return in the main
				// checkout on whatever branch that is — and a claude pane
				// resumes its recorded session there, continuing the
				// conversation against the wrong tree. Come up visibly broken
				// instead: the pane exists, its CWD stands, and the failure is
				// on screen rather than in a log.
				log.Printf("pane %s: worktree %q gone, leaving the pane unspawned", pane.ID, pane.CWD)
				pane.PluginMu.Lock()
				pane.SpawnError = fmt.Sprintf("worktree is gone: %s", pane.CWD)
				pane.PluginMu.Unlock()
				return
			}
			log.Printf("pane %s: saved cwd %q gone, using default", pane.ID, pane.CWD)
			pane.PluginMu.Lock()
			pane.CWD = ""
			pane.PluginMu.Unlock()
		}
	}
	// A retry (Alt+R) must clear a previous failure, or a pane that recovers
	// keeps its complaint.
	pane.PluginMu.Lock()
	pane.SpawnError = ""
	pane.PluginMu.Unlock()
```

Place the clear so it runs on every path that proceeds to spawn, and **not** on the early return above.

- [ ] **Step 5: Persist ownership, broadcast the error**

Add `worktree_owned` to the snapshot's pane record and to `workspaceStateFromSnapshot`. Add `spawn_error` to the broadcast pane data (`daemon.go`, beside the `git_*` keys) — conditional on non-empty, matching `git_branch`.

Add `SpawnError string` to `PaneInfo` and `PaneModel`, decode it, and copy it **unconditionally** in `workstate.go` beside the git fields, so a successful retry clears it.

- [ ] **Step 6: Render the error in the pane**

Where pane content is composed, a pane with a non-empty `SpawnError` renders the message instead of VT content:

```go
	// A pane with no process says why, in the pane's own rectangle. Not a
	// modal: this fires during restore, potentially for several panes at
	// once, and a modal per pane would be unusable. Not a log line either —
	// that is the quiet relocation this replaces.
	if pane.SpawnError != "" {
		return renderPaneMessage(rect, sanitizeRemoteText(pane.SpawnError), "Alt+R to retry")
	}
```

`sanitizeRemoteText` at render, as with every other daemon-sourced string: the message interpolates a path from a host the user may not control.

- [ ] **Step 7: Write the TUI test**

```go
// The message is rendered in the pane's own rect, not as a modal: restore can
// produce several of these at once and a modal each would be unusable.
func TestPaneRender_SpawnErrorIsShownInThePane(t *testing.T) {
	pane := &PaneModel{ID: "p1", SpawnError: "worktree is gone: /wt/feat-x"}
	out := stripANSI(renderPaneForTest(t, pane, 60, 12))

	if !strings.Contains(out, "worktree is gone") {
		t.Errorf("pane render does not carry the reason:\n%s", out)
	}
	if !strings.Contains(out, "Alt+R") {
		t.Errorf("pane render does not offer the retry:\n%s", out)
	}
}

// The message interpolates a path from a daemon the user may not control.
func TestPaneRender_SanitizesTheSpawnError(t *testing.T) {
	pane := &PaneModel{ID: "p1", SpawnError: "gone: \x1b]52;c;cGF5bG9hZA==\x07"}
	if strings.Contains(renderPaneForTest(t, pane, 60, 12), "\x1b]52") {
		t.Error("an OSC 52 in the spawn error survived to the rendered pane")
	}
}
```

- [ ] **Step 8: Run everything and watch it pass**

Run: `./scripts/dev.sh test ./internal/daemon/ ./internal/tui/ ./internal/persist/`
Expected: PASS.

- [ ] **Step 9: Mutation check**

Delete the `pane.WorktreeOwned` branch added in Step 4 and re-run. Expected: `TestSpawnRestoredPane_WorktreeGoneComesUpBroken` fails and `TestSpawnRestoredPane_OrdinaryMissingCWDStillFallsBack` still passes — proving the two cases are genuinely separated rather than the branch being unreachable.

Restore the branch.

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/ internal/tui/ internal/persist/
git commit -F - <<'EOF'
feat(daemon): a pane whose worktree is gone comes up visibly broken

Restore blanked a missing CWD and fell back to the daemon's default, so
removing a worktree brought the pane back in the main checkout on whatever
branch that was — and a claude pane resumed its recorded session there,
continuing against the wrong tree.

Worktree ownership is persisted because that is the only thing that tells
the two cases apart: the snapshot stored just CWD, so a missing worktree
and a stale browsed path were indistinguishable. Ordinary panes keep the
fallback; their loss is a convenience, not the isolation.

The error itself is runtime-only — a fresh daemon re-stats — so a restored
worktree does not carry a stale complaint.
EOF
```

---

### Task 5: The `new branch` mode in the setup dialog

**Files:**
- Modify: `internal/tui/dialog.go` (`worktreeRows`, `renderSetupWorktreeField`, `handleSetupWorktreeKey`, `submitSetupDialog`), `internal/tui/worktree_client.go`
- Test: `internal/tui/worktree_newbranch_test.go`

**Interfaces:**
- Consumes: stage A's `worktreeState`, `selectedWorktree`, `worktreeCursor`, `worktreeScroll`, `setupSpawnDir()`, `onSetupCWDChanged(dir)`.
- Produces: `Model.worktreeNewBranch string`, `Model.worktreeMode` — consumed by Task 6's submit path.

**Context an implementer cannot infer:**

- Stage A's field has three states: inert (not a repository), `off`, and one row per existing worktree. Stage B adds a **fourth row**, `+ new branch…`, which expands into a text input. It is added at the **end** of the list so every existing row keeps its index and stage A's fixture tests shift by one row rather than being rewritten.
- **The field must not be rendered at all in replace mode.** That is this plan's ruling, and it is load-bearing: the client-side replace sets `leaf.Pane = nil` and calls `old.Dispose()` before the send, so a failed add costs a live pane and `PrunePlaceholders` cannot undo a `Dispose()`.
- **Every path that changes the browsed directory must route through `onSetupCWDChanged`.** This was a Critical finding in stage A and its third recurrence in the same class: a bare `m.cwdBrowseDir = …` leaves the worktree selection pointing at the previous repository, so the next pane spawns in repo A's worktree while the UI shows repo B. The new-branch text must be cleared there too. The invariant is: **no bare `m.cwdBrowseDir =` on any setup-dialog path.** Verify by grep, not by recalling call sites.
- `worktreeVisibleRows` must keep reserving `sessionListMinRows` for the session field below it. Stage A shipped a version that claimed greedily and violated the height budget for terminal heights **29–35** — a seven-integer band that four sampled heights straddled and missed. Sweep the contiguous range in the test; do not sample.

- [ ] **Step 1: Write the failing tests**

```go
// The new-branch row is LAST so every existing worktree keeps its index and
// stage A's row fixtures shift rather than needing a rewrite.
func TestWorktreeRows_NewBranchIsLast(t *testing.T) {
	m := setupModelInRepo(t, []gitworktree.Worktree{
		{Path: "/repo", Branch: "master", Main: true},
		{Path: "/repo-worktrees/feat-x", Branch: "feat/x"},
	})
	rows := m.worktreeRows()
	if len(rows) == 0 {
		t.Fatal("no worktree rows")
	}
	if !strings.Contains(rows[len(rows)-1].label, "new branch") {
		t.Errorf("last row is %q, want the new-branch row", rows[len(rows)-1].label)
	}
}

// Replace mode must not render the field at all: the client destroys its model
// of the old pane before the send, so a failed add costs a live pane and
// PrunePlaceholders cannot undo a Dispose().
func TestWorktreeField_AbsentInReplaceMode(t *testing.T) {
	m := setupModelInRepo(t, []gitworktree.Worktree{{Path: "/repo", Branch: "master", Main: true}})
	m.setupReplacing = true

	out := stripANSI(m.renderSetupDialog())
	if strings.Contains(out, "Worktree") {
		t.Errorf("the worktree field is rendered on the replace path:\n%s", out)
	}
}

// The third recurrence of this class in one feature. A bare m.cwdBrowseDir
// assignment leaves the worktree selection pointing at the PREVIOUS repository,
// so the next pane spawns in repo A while the UI shows repo B.
func TestOnSetupCWDChanged_ClearsTheNewBranchToo(t *testing.T) {
	m := setupModelInRepo(t, []gitworktree.Worktree{{Path: "/repo", Branch: "master", Main: true}})
	m.worktreeNewBranch = "feat/x"
	m.worktreeMode = worktreeModeNew

	m.onSetupCWDChanged("/other-repo")

	if m.worktreeNewBranch != "" {
		t.Errorf("worktreeNewBranch = %q after a directory change", m.worktreeNewBranch)
	}
	if m.worktreeMode != worktreeModeOff {
		t.Errorf("worktreeMode = %v after a directory change, want off", m.worktreeMode)
	}
	if m.selectedWorktree != "" {
		t.Errorf("selectedWorktree = %q after a directory change", m.selectedWorktree)
	}
}

// An invalid branch is refused in the DIALOG, before any IPC — the daemon
// validates too, but a round-trip to learn about a typo is a bad dialog.
func TestSubmitSetup_RefusesAnInvalidBranchLocally(t *testing.T) {
	m := setupModelInRepo(t, []gitworktree.Worktree{{Path: "/repo", Branch: "master", Main: true}})
	m.worktreeMode = worktreeModeNew
	m.worktreeNewBranch = "-b"

	sender := &fakeSender{}
	m.client = sender
	m.submitSetupDialog()

	if len(sender.sent) != 0 {
		t.Errorf("an invalid branch was sent to the daemon: %+v", sender.sent)
	}
	if m.setupErr == "" {
		t.Error("no error shown for an invalid branch")
	}
}

// lipgloss.Place does not clip, so a field that claims rows greedily pushes
// [Continue] off screen. Stage A shipped a version that violated this for
// heights 29-35 — a band four sampled heights straddled. SWEEP the range.
func TestWorktreeVisibleRows_RespectsTheSessionFloorAtEveryHeight(t *testing.T) {
	for h := 20; h <= 60; h++ {
		m := setupModelInRepo(t, manyWorktrees(20))
		m.height = h
		wt, sess := m.worktreeVisibleRows(), m.sessionVisibleRows()
		if sess < sessionListMinRows && h >= minDialogHeightForLists {
			t.Errorf("h=%d: session got %d rows, below the floor %d (worktree took %d)", h, sess, sessionListMinRows, wt)
		}
		if total := setupFixedRows + wt + sess; total > h {
			t.Errorf("h=%d: dialog wants %d rows, more than the terminal has", h, total)
		}
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run "TestWorktree|TestOnSetupCWDChanged|TestSubmitSetup"`
Expected: FAIL to compile.

- [ ] **Step 3: Add the mode state**

```go
// worktreeMode is which of the field's three committing states is chosen.
// Stage A shipped off + existing; new is stage B's addition.
type worktreeMode int

const (
	worktreeModeOff worktreeMode = iota
	worktreeModeExisting
	worktreeModeNew
)
```

Add `worktreeMode worktreeMode` and `worktreeNewBranch string` to `Model`.

- [ ] **Step 4: Extend `onSetupCWDChanged`**

```go
func (m *Model) onSetupCWDChanged(dir string) tea.Cmd {
	m.cwdBrowseDir = dir
	m.selectedWorktree = ""
	m.worktreeMode = worktreeModeOff
	m.worktreeNewBranch = ""
	m.worktreeCursor = 0
	m.worktreeScroll = 0
	m.worktrees = worktreeState{}
	return nil
}
```

- [ ] **Step 5: Add the row, the input, and the replace-mode gate**

Append the `+ new branch…` row in `worktreeRows()`; expand it into a text input in `renderSetupWorktreeField` when `worktreeMode == worktreeModeNew`; route typing through `handleSetupWorktreeKey`. Gate the whole field on `!m.setupReplacing` at the one place the field's rows are computed, so the gate cannot be half-applied between the renderer and the key handler.

- [ ] **Step 6: Validate at submit**

In `submitSetupDialog`, before the send:

```go
	if m.worktreeMode == worktreeModeNew {
		// Validated locally as well as daemon-side. The daemon is the
		// authority — an IPC client can send anything — but a round-trip to
		// learn about a typo is a bad dialog.
		if err := gitworktree.ValidateBranch(m.worktreeNewBranch); err != nil {
			m.setupErr = err.Error()
			return
		}
		payload.Worktree = &ipc.WorktreeSpec{
			RepoRoot: m.cwdBrowseDir,
			Branch:   m.worktreeNewBranch,
		}
	}
```

- [ ] **Step 7: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS. Stage A's worktree fixtures shift by one row — update them.

- [ ] **Step 8: Grep the invariant**

Run: `grep -rn "m\.cwdBrowseDir = " internal/tui/ | grep -v onSetupCWDChanged`
Expected: **no matches** outside `onSetupCWDChanged` itself. Any hit is the same Critical defect recurring; route it through the helper.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/
git commit -F - <<'EOF'
feat(tui): create a worktree on a new branch from the setup dialog

Adds a "+ new branch…" row at the END of the worktree field, so existing
rows keep their indices. The branch is validated in the dialog as well as
daemon-side — the daemon is the authority, but a round-trip to learn about
a typo is a bad dialog.

The field is absent in replace mode: that path destroys its model of the
old pane before the send, so a failed add would cost a live pane and
PrunePlaceholders cannot undo a Dispose().
EOF
```

---

### Task 6: Unwind the layout placeholder when the add fails

**Files:**
- Modify: `internal/tui/dialog.go` (`handleCreatePaneSplit`), `internal/tui/model.go` (the `MsgCreatePaneResp` arm), `internal/tui/worktree_client.go`
- Test: `internal/tui/worktree_create_resp_test.go`

**Interfaces:**
- Consumes: `ipc.CreatePaneRespPayload` (Task 2), `LayoutNode.PrunePlaceholders` (`layout.go:230`), `m.pendingSplit`.

**Context an implementer cannot infer, and it is the reason this task exists:** `handleCreatePaneSplit` splits the layout tree and arms `m.pendingSplit[tab.ID]` **before** the IPC send (`dialog.go:1683`), and `applyWorkspaceState` fills that placeholder with the next pane arriving for the tab, whoever created it (`model.go:4072-4076`). Today that window is a millisecond. A worktree add stretches it to seconds, and two things then go wrong that cannot go wrong now:

1. A pane created by another client or by MCP `create_pane` fills the placeholder meant for this one.
2. A failed add leaves the placeholder armed **forever**, because nothing on the failure path unwinds it.

The unwind needs no new machinery: `PrunePlaceholders` already removes a placeholder leaf by promoting its sibling and `applyWorkspaceState` already calls it on two paths. What this task adds is *when*.

Every `MsgCreatePaneResp` Update arm **must** re-arm `m.listenForMessages()`, or the IPC listen loop dies for the session. The timeout tick must **not**.

- [ ] **Step 1: Write the failing tests**

```go
// A failed add must unwind the placeholder the client armed before the send.
// Nothing else will: applyWorkspaceState only fills placeholders, it never
// retires one whose pane never arrives.
func TestCreatePaneResp_FailureUnwindsThePlaceholder(t *testing.T) {
	m := modelWithPendingWorktreeSplit(t, "tab1")
	if _, ok := m.pendingSplit["tab1"]; !ok {
		t.Fatal("fixture did not arm a pending split")
	}

	m2, _ := m.Update(createPaneRespMsg(ipc.CreatePaneRespPayload{
		Error:    "fatal: '/wt/feat-x' already exists",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}))
	m = m2.(Model)

	if _, ok := m.pendingSplit["tab1"]; ok {
		t.Error("the pending split is still armed after a failed create")
	}
	if countPlaceholders(m.tabByID("tab1").Root) != 0 {
		t.Error("a placeholder leaf survived a failed create")
	}
	if m.flash == "" {
		t.Error("the failure was not reported to the user")
	}
}

// git's own stderr reaches the user: "already used by worktree '/x/feat-y'"
// names the pane to go look at, and no message Quil could invent would.
func TestCreatePaneResp_ShowsGitsOwnError(t *testing.T) {
	m := modelWithPendingWorktreeSplit(t, "tab1")
	m2, _ := m.Update(createPaneRespMsg(ipc.CreatePaneRespPayload{
		Error:    "already used by worktree '/x/feat-y'",
		Worktree: &ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"},
	}))
	if !strings.Contains(m2.(Model).flash, "/x/feat-y") {
		t.Errorf("flash %q drops git's own message", m2.(Model).flash)
	}
}

// Every response arm must re-arm the listener or the IPC loop dies for the
// session — the same rule the history and memory arms follow.
func TestCreatePaneResp_ReArmsTheListener(t *testing.T) {
	m := modelWithPendingWorktreeSplit(t, "tab1")
	_, cmd := m.Update(createPaneRespMsg(ipc.CreatePaneRespPayload{PaneID: "p9"}))
	if cmd == nil {
		t.Fatal("no command returned — the listen loop would die")
	}
	if !cmdContainsListen(t, cmd) {
		t.Error("the response arm does not re-arm listenForMessages")
	}
}

// A never-answered create must not leave the dialog and the layout wedged.
func TestCreatePane_TimeoutUnwindsThePlaceholder(t *testing.T) {
	m := modelWithPendingWorktreeSplit(t, "tab1")
	m2, _ := m.Update(createPaneTimeoutMsg{spec: ipc.WorktreeSpec{RepoRoot: "/repo", Branch: "feat/x"}})
	if _, ok := m2.(Model).pendingSplit["tab1"]; ok {
		t.Error("a timed-out create left the placeholder armed")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestCreatePane`
Expected: FAIL to compile.

- [ ] **Step 3: Implement the response arm**

```go
	case createPaneRespMsg:
		m.applyCreatePaneResp(msg.payload)
		// MUST re-arm: an arm that returns a bare nil ends the listen loop
		// for the session, and every later IPC message is lost.
		return m, m.listenForMessages()
```

```go
// applyCreatePaneResp settles a worktree-backed create.
//
// Success needs nothing: the pane arrives in the next workspace broadcast and
// fills the placeholder exactly as an ordinary create does.
//
// Failure has to unwind by hand. handleCreatePaneSplit splits the layout tree
// and arms pendingSplit BEFORE the send, and nothing on the failure path
// retires either — applyWorkspaceState only FILLS placeholders. Without this
// the tab keeps a dead placeholder leaf and the next pane created anywhere in
// that tab is swallowed by it.
func (m *Model) applyCreatePaneResp(p ipc.CreatePaneRespPayload) {
	if p.Error == "" {
		return
	}
	tabID := m.worktreeCreateTab
	if tab := m.tabByID(tabID); tab != nil && tab.Root != nil {
		tab.Root.PrunePlaceholders()
	}
	delete(m.pendingSplit, tabID)
	m.worktreeCreateTab = ""
	// git's own stderr, sanitized at render like every daemon-sourced string.
	m.setFlash("worktree not created: " + sanitizeRemoteText(p.Error))
	m.sendAllLayouts()
}
```

Arm `m.worktreeCreateTab` and a `createPaneTimeout` tick in `handleCreatePaneSplit` when the payload carries a spec. The timeout must be **longer than `worktreeAddTimeout`** (Task 3's 120 s) plus slack, or the client gives up on an add that is still running and prunes a placeholder the daemon is about to fill. Use `createPaneTimeout = worktreeAddTimeout + 30*time.Second` and state the relationship in the comment so the two cannot drift apart silently.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `./scripts/dev.sh test ./internal/tui/ -run TestCreatePane`
Expected: PASS.

- [ ] **Step 5: Full suite, race, vet**

Run: `./scripts/dev.sh test`, `./scripts/dev.sh test-race`, `./scripts/dev.sh vet`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/
git commit -F - <<'EOF'
fix(tui): unwind the layout placeholder when a worktree create fails

handleCreatePaneSplit arms a placeholder before the send and nothing
retires it — applyWorkspaceState only fills placeholders. That window was
a millisecond for an ordinary create; a worktree add stretches it to
seconds, so a failure left a dead placeholder that swallowed the next pane
created in the tab.

The client's give-up is deliberately longer than the daemon's add timeout,
so it can never prune a placeholder the daemon is about to fill.
EOF
```

---

### Task 7: End-to-end verification and documentation

**Files:**
- Create: `internal/daemon/worktree_e2e_test.go` (build tag `integration`)
- Modify: `docs/features.md`, `docs/configuration.md`, `.claude/rules/projects.md`, `CHANGELOG.md`

- [ ] **Step 1: Write the integration test**

Against a real repository under `t.TempDir()`, with the real git binary: create a worktree through `worktreeAddAndCreate`, assert the directory exists, the branch is checked out, the pane's CWD is the worktree, and `WorktreeOwned` is set. Then remove the worktree and assert `spawnRestoredPane` leaves the pane unspawned with a `SpawnError` and an unchanged CWD.

Guard with `//go:build integration`.

- [ ] **Step 2: Pin the claude-resume interaction**

The spec flags this as assumed-not-verified: a claude pane in a worktree records its transcript under the worktree's path, and `ReadPersistedSession` merges the recorded path with the session id and verifies the path names the id it was recorded with. Write the test that proves it, rather than inheriting the assumption.

- [ ] **Step 3: Run everything**

Run: `./scripts/dev.sh test`, `./scripts/dev.sh test-race`, `./scripts/dev.sh vet`, then `go test -tags=integration ./internal/daemon/` inside the container.
Expected: PASS.

- [ ] **Step 4: Manual verification in dev mode**

```bash
./scripts/dev.sh build
./scripts/quil-dev.ps1
```

Confirm `[dev]` in the status bar, then: create a claude pane on a new worktree in a real repo; confirm the sidebar shows the new branch and names the worktree (if the sidebar-worktree-name plan has shipped); create a second pane attached to the same worktree; try a branch that already exists and confirm the error names it and no pane appears; close the TUI, `git worktree remove` the directory, relaunch, and confirm the pane comes up with the message rather than in the main checkout.

- [ ] **Step 5: Document**

`docs/features.md`: the field's three modes. `docs/configuration.md`: the sibling-directory location and that worktrees are never auto-removed. `.claude/rules/projects.md`: the three invariants — a worktree CWD is exempt from the create-path fallback; a worktree-owned pane never relocates on restore; the worktree field is absent in replace mode, because that path disposes the old pane before the send. `CHANGELOG.md` under `[Unreleased]`.

- [ ] **Step 6: Check the docs-size gate**

Run: `./scripts/dev.sh docs-size`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/worktree_e2e_test.go docs/ .claude/rules/projects.md CHANGELOG.md
git commit -F - <<'EOF'
test(daemon): cover worktree creation end to end

Drives a real repository through create, attach, failure and restore.
Pins the claude-resume interaction rather than inheriting it as an
assumption: a pane in a worktree records its transcript under the
worktree's path, and that path is verified against the session id by
machinery written before worktrees were common.
EOF
```

---

## Self-Review

**Spec coverage.** The spec's stage B is the create path plus its three open decisions. Task 1 covers branch validation and the `[git]`-adjacent path derivation; Task 2 the wire; Task 3 the asynchronous daemon create, the no-fallback exemption and the permit budget (open decision 3); Task 4 persisted ownership and the restore error state (open decision 2); Task 5 the dialog, including refusing replace mode (open decision 1); Task 6 the placeholder unwind the spec called out as "what stage B has to design is *when* to run it"; Task 7 the resume interaction the spec flagged as assumed.

Deliberately **not** covered, and unchanged from the spec's out-of-scope list: moving an existing pane into a worktree (physically a respawn, tangled with claude session resume), worktree removal from Quil, and offering existing branches in create mode.

**Placeholder scan.** Every code step carries real code. Three bodies are specified by contract rather than written out, each because it is an extraction from code the implementer will have open: `createPaneCommon` (Task 3 — an extraction from `handleCreatePane`, explicitly "do not copy it"), the `worktreeRows`/`renderSetupWorktreeField` edits (Task 5 — stage A's code, whose shape the plan names precisely), and `renderPaneMessage` (Task 4 — the pane compositor's own helper conventions).

**Type consistency.** `WorktreeSpec{RepoRoot, Branch}` is introduced in Task 2 and used unchanged in Tasks 3, 5 and 6. `ValidateBranch`/`DerivePath` (Task 1) are called from the daemon (Task 3) and the dialog (Task 5). `Pane.WorktreeOwned` is written in Task 3 and read in Task 4. `Pane.SpawnError` is written in Task 4 and rendered in Task 4. `CreatePaneRespPayload` is produced in Task 3 and consumed in Task 6.

**Risk note for the executor.** Task 3 and Task 6 are the two that carry real hazard: the first because it touches `handleCreatePane`, which every pane in the product goes through, and the second because it manipulates the layout tree on a failure path that is hard to reach by hand. Both deserve a review at a higher tier than the rest, and Task 3's mutation check (Step 6) is not optional — it is the only step that proves the no-relocation guarantee is actually load-bearing rather than merely written down.
