# Worktree-Owned Panes, Stage A — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a pane be created inside an existing git worktree, chosen from a list the daemon supplies, so an agent, a terminal and lazygit can sit on one branch together and the sidebar reports each pane's real checkout.

**Architecture:** One new daemon-side RPC (`MsgWorktreeListReq`/`Resp`) answered on a worker goroutine behind its own single-flight slot, a TUI client-state struct that mirrors `repoScanState`, and a new field in the create-pane setup dialog. Attaching needs no create-path change at all: `CWD` is already an ordinary field on `CreatePanePayload`, so "attach" is a normal create whose CWD happens to be a worktree.

**Tech Stack:** Go 1.25, Bubble Tea v2 (`charm.land/bubbletea/v2`), Lipgloss v2, length-prefixed JSON IPC.

**Spec:** `docs/superpowers/specs/2026-08-05-worktree-owned-panes-design.md`

## Global Constraints

- **Stage A never creates a branch or a worktree.** The field has exactly three states: inert (not a repository), `off`, and one row per existing worktree. `gitworktree.Add` exists and stays unused by this plan.
- **Build and test through Docker only** — Go is not installed on the host: `./scripts/dev.sh test <pkg>`, `./scripts/dev.sh vet`, `./scripts/dev.sh build`.
- **Never touch `~/.quil/`** or the production daemon. Dev mode only (`.quil/` in the project root).
- **Response payloads echo their request key VERBATIM** on every path including error and single-flight rejection. It is the client's staleness key, not a statement about what was read.
- **Every daemon-side git invocation takes a `claimBlockingFSCall` permit** and runs under a context deadline.
- **Remote-sourced strings are sanitized at RENDER only**, via `sanitizeRemoteText` — never in state, because a chosen worktree path becomes the pane's spawn CWD.
- **`gofmt` is mandatory.** Tabs in Go, 2 spaces in TOML/JSON/YAML.
- **Commit messages:** Conventional Commits, imperative, first line ≤ 72 chars. No AI/agent attribution of any kind.
- Work on branch `feature/worktree-panes-stage-a`, branched from `master`.

## Already Done (do not redo)

`internal/gitworktree` is implemented and tested (`gitworktree.go`, `gitworktree_test.go`, `proc_windows.go`, `proc_other.go`). It exports:

```go
type Worktree struct {
    Path     string // absolute, as git reports it
    Branch   string // "" when Detached or Bare
    Detached bool
    Main     bool   // the FIRST porcelain entry
    Locked   bool
    Prunable bool
    Bare     bool   // only ever on the main entry
}

func List(ctx context.Context, dir string) ([]Worktree, error) // outside a repo: nil, nil
func Add(ctx context.Context, repo, path, branch string) error // STAGE B — unused here
```

## File Structure

| File | Responsibility |
|---|---|
| `internal/ipc/protocol.go` (modify) | Two message-type constants, two payload structs, one wire struct for a worktree entry |
| `internal/daemon/worktree.go` (create) | The handler, its single-flight claim, and the pure `worktreeListResponse` |
| `internal/daemon/daemon.go` (modify) | One `atomic.Bool` field, one dispatch case |
| `internal/tui/worktree_client.go` (create) | `worktreeState`, request builder, response/timeout appliers |
| `internal/tui/model.go` (modify) | One field, two `Update` cases |
| `internal/tui/dialog.go` (modify) | Field presence, render, key handling, submit, CWD-change reset |
| `docs/features.md`, `docs/configuration.md`, `CHANGELOG.md` (modify) | User-facing docs |

---

### Task 1: IPC contract

**Files:**
- Modify: `internal/ipc/protocol.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ipc.MsgWorktreeListReq`, `ipc.MsgWorktreeListResp`, `ipc.WorktreeListReqPayload{Path string}`, `ipc.WorktreeListRespPayload{Path, Repo, Root, WorktreeRoot, Worktrees, Error}`, `ipc.WorktreeInfo{Path, Branch, Detached, Main, Locked, Prunable, Bare}`.

- [ ] **Step 1: Add the message-type constants**

In `internal/ipc/protocol.go`, beside `MsgGitReposReq`/`MsgGitReposResp` (line ~125):

```go
	MsgWorktreeListReq  = "worktree_list_req"
	MsgWorktreeListResp = "worktree_list_resp"
```

- [ ] **Step 2: Add the payload structs**

Beside `GitReposRespPayload` (line ~755):

```go
// WorktreeListReqPayload asks which git worktrees belong to the repository
// containing Path. An empty Path means the daemon's default directory.
type WorktreeListReqPayload struct {
	Path string `json:"path"`
}

// WorktreeInfo is one entry of the repository's worktree list, as the daemon
// sees it. A mirror of gitworktree.Worktree rather than a reuse of it: this is
// a wire type, and the internal one is free to change shape.
type WorktreeInfo struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	Main     bool   `json:"main,omitempty"`
	Locked   bool   `json:"locked,omitempty"`
	Prunable bool   `json:"prunable,omitempty"`
	Bare     bool   `json:"bare,omitempty"`
}

// WorktreeListRespPayload carries the repository's worktrees, main checkout
// first.
//
// CONTRACT: Path echoes the request VERBATIM on every path, including the
// error and single-flight-rejection ones. It is the client's staleness key,
// not a statement about what was read — normalising it daemon-side would make
// a live request look permanently stale.
//
// Repo false with an empty Error is a real answer ("this is not a repository")
// and must stay distinguishable from a failure: only one of the two justifies
// telling the user there is no repository here.
//
// WorktreeRoot is the directory NEW worktrees would go in, already joined by
// the daemon with the daemon's own separators. The client must never compute
// it: doing so means running filepath.Dir/Join with the CLIENT's separators
// over a path that lives on the daemon's machine. Unused by stage A beyond
// display, and present now so the contract does not change under stage B.
type WorktreeListRespPayload struct {
	Path         string         `json:"path"`
	Repo         bool           `json:"repo,omitempty"`
	Root         string         `json:"root,omitempty"`
	WorktreeRoot string         `json:"worktree_root,omitempty"`
	Worktrees    []WorktreeInfo `json:"worktrees,omitempty"`
	Error        string         `json:"error,omitempty"`
}
```

- [ ] **Step 3: Verify it compiles**

Run: `./scripts/dev.sh vet internal/ipc`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add internal/ipc/protocol.go
git commit -m "feat(ipc): add the worktree-list request/response pair"
```

---

### Task 2: Daemon handler

**Files:**
- Create: `internal/daemon/worktree.go`
- Create: `internal/daemon/worktree_test.go`
- Modify: `internal/daemon/daemon.go` (add `worktreeScanning atomic.Bool` field; add dispatch case)

**Interfaces:**
- Consumes: `ipc.MsgWorktreeListReq`, `ipc.WorktreeListReqPayload`, `ipc.WorktreeListRespPayload`, `ipc.WorktreeInfo` (Task 1); `gitworktree.List`.
- Produces: `(*Daemon).handleWorktreeListReq(conn *ipc.Conn, msg *ipc.Message)`, `worktreeListResponse(req ipc.WorktreeListReqPayload, fallback string) ipc.WorktreeListRespPayload`, package var `worktreeListFn = gitworktree.List`.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/worktree_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

func stubWorktreeList(t *testing.T, out []gitworktree.Worktree, err error) {
	t.Helper()
	prev := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) {
		return out, err
	}
	t.Cleanup(func() { worktreeListFn = prev })
}

// The echo contract: Path comes back byte-identical on EVERY path, because it
// is the client's staleness key. A daemon-side normalisation makes a live
// request look permanently stale.
func TestWorktreeListResponse_EchoesPathVerbatim(t *testing.T) {
	stubWorktreeList(t, nil, errors.New("boom"))
	const raw = "  /repo/./sub/  "
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: raw}, "/fallback")
	if got.Path != raw {
		t.Errorf("Path = %q, want the request's %q verbatim", got.Path, raw)
	}
}

// "Not a repository" is a real answer and must be distinguishable from a
// failure: only one of the two justifies telling the user there is no
// repository here.
func TestWorktreeListResponse_NotARepoIsNotAnError(t *testing.T) {
	stubWorktreeList(t, nil, nil)
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/tmp"}, "/fallback")
	if got.Repo {
		t.Error("Repo should be false outside a repository")
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty — not-a-repo is an answer", got.Error)
	}
}

// Root is the MAIN checkout and WorktreeRoot is its sibling worktrees
// directory, both computed daemon-side because they describe the daemon's disk.
func TestWorktreeListResponse_DerivesRootAndWorktreeRoot(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil", Branch: "master", Main: true},
		{Path: "/projects/quil-worktrees/feat-x", Branch: "feat/x"},
	}, nil)

	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil/internal"}, "")
	if !got.Repo {
		t.Fatal("Repo should be true")
	}
	if got.Root != "/projects/quil" {
		t.Errorf("Root = %q, want the main checkout", got.Root)
	}
	if got.WorktreeRoot != "/projects/quil-worktrees" {
		t.Errorf("WorktreeRoot = %q, want the sibling worktrees dir", got.WorktreeRoot)
	}
	if len(got.Worktrees) != 2 {
		t.Fatalf("got %d worktrees, want 2", len(got.Worktrees))
	}
	if !got.Worktrees[0].Main || got.Worktrees[1].Main {
		t.Error("only the first entry is the main checkout")
	}
	if got.Worktrees[1].Branch != "feat/x" {
		t.Errorf("branch = %q, want feat/x", got.Worktrees[1].Branch)
	}
}

// A bare main checkout has no working tree, so there is nothing for a sibling
// path to be a sibling OF. Reported as a repository with no derived root
// rather than a failure — the field renders the reason.
func TestWorktreeListResponse_BareRepoHasNoWorktreeRoot(t *testing.T) {
	stubWorktreeList(t, []gitworktree.Worktree{
		{Path: "/projects/quil.git", Main: true, Bare: true},
	}, nil)
	got := worktreeListResponse(ipc.WorktreeListReqPayload{Path: "/projects/quil.git"}, "")
	if !got.Repo {
		t.Fatal("a bare repository is still a repository")
	}
	if got.WorktreeRoot != "" {
		t.Errorf("WorktreeRoot = %q, want empty for a bare repo", got.WorktreeRoot)
	}
	if !got.Worktrees[0].Bare {
		t.Error("the bare flag must survive onto the wire")
	}
}

// An empty Path means the daemon's default directory.
func TestWorktreeListResponse_EmptyPathUsesFallback(t *testing.T) {
	var sawDir string
	prev := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) {
		sawDir = dir
		return nil, nil
	}
	t.Cleanup(func() { worktreeListFn = prev })

	worktreeListResponse(ipc.WorktreeListReqPayload{Path: ""}, "/daemon/cwd")
	if sawDir != "/daemon/cwd" {
		t.Errorf("scanned %q, want the fallback", sawDir)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `undefined: worktreeListFn`, `undefined: worktreeListResponse`.

- [ ] **Step 3: Write the implementation**

Create `internal/daemon/worktree.go`:

```go
package daemon

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeListTimeout bounds one listing. `git worktree list` reads the
// repository's admin directory, so it is a readdir-class call like browse —
// not the checkout-class budget an Add would need (stage B).
var worktreeListTimeout = 10 * time.Second

// worktreeListFn is the seam tests drive, so no test needs a repository or a
// git binary. Same pattern as gitProbeFn/gitDirsFn in gitcache.go.
var worktreeListFn = gitworktree.List

// handleWorktreeListReq answers "which worktrees does this repository have".
//
// Worker goroutine + single-flight, matching handleGitReposReq. Its OWN slot
// rather than sharing browseScanning: the setup dialog resolves a directory
// and then lists its worktrees, so one shared guard would make each step fail
// exactly when it followed the other — the same reasoning that gave
// gitDiscovering, kubeDiscovering and dirsChecking slots of their own.
func (d *Daemon) handleWorktreeListReq(conn *ipc.Conn, msg *ipc.Message) {
	rejection, ok := d.beginWorktreeList(msg)
	if !ok {
		respondTo(conn, msg.ID, ipc.MsgWorktreeListResp, rejection)
		return
	}
	fallback := d.defaultCWD()
	go func() {
		defer d.worktreeScanning.Store(false)
		respondTo(conn, msg.ID, ipc.MsgWorktreeListResp,
			worktreeListResponse(worktreeListReq(msg), fallback))
	}()
}

// beginWorktreeList claims the single-flight slot, returning the rejection to
// send when it is already taken. Split from the handler because ipc.Conn
// cannot be built outside its package — same reason as beginGitDiscover, and
// the reason the slot-independence test can exercise the handler's OWN claim
// path rather than poking the atomic directly.
func (d *Daemon) beginWorktreeList(msg *ipc.Message) (ipc.WorktreeListRespPayload, bool) {
	if d.worktreeScanning.CompareAndSwap(false, true) {
		return ipc.WorktreeListRespPayload{}, true
	}
	return ipc.WorktreeListRespPayload{
		Path:  worktreeListReq(msg).Path,
		Error: "another worktree scan is already running",
	}, false
}

func worktreeListReq(msg *ipc.Message) ipc.WorktreeListReqPayload {
	var req ipc.WorktreeListReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		log.Printf("handleWorktreeListReq: decode: %v", err)
	}
	return req
}

// worktreeListResponse is the pure half.
//
// CONTRACT: Path echoes req.Path VERBATIM on every path, including the error
// ones — it is the client's staleness key.
func worktreeListResponse(req ipc.WorktreeListReqPayload, fallback string) ipc.WorktreeListRespPayload {
	out := ipc.WorktreeListRespPayload{Path: req.Path}

	dir := req.Path
	if dir == "" {
		dir = fallback
	}
	if dir == "" {
		out.Error = "no directory to scan and no default available"
		return out
	}

	if !claimBlockingFSCall() {
		out.Error = "too many filesystem calls in flight"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeListTimeout)
	list, err := worktreeListFn(ctx, dir)
	cancel()
	releaseBlockingFSCall()

	if err != nil {
		out.Error = err.Error()
		return out
	}
	if len(list) == 0 {
		// Not a repository. A real answer, deliberately not an Error — the two
		// produce different UI and only one may say "there is no repository".
		return out
	}

	out.Repo = true
	out.Worktrees = make([]ipc.WorktreeInfo, 0, len(list))
	for _, w := range list {
		out.Worktrees = append(out.Worktrees, ipc.WorktreeInfo{
			Path:     w.Path,
			Branch:   w.Branch,
			Detached: w.Detached,
			Main:     w.Main,
			Locked:   w.Locked,
			Prunable: w.Prunable,
			Bare:     w.Bare,
		})
	}

	main := list[0]
	out.Root = main.Path
	// A bare main checkout has no working tree, so nothing can be its sibling.
	// Left empty rather than guessed; the field renders the reason.
	if !main.Bare {
		out.WorktreeRoot = filepath.Join(filepath.Dir(main.Path),
			filepath.Base(main.Path)+"-worktrees")
	}
	return out
}
```

- [ ] **Step 4: Add the daemon field and dispatch case**

In `internal/daemon/daemon.go`, beside the existing `gitDiscovering`/`kubeDiscovering`/`dirsChecking` fields on the `Daemon` struct:

```go
	// worktreeScanning single-flights MsgWorktreeListReq. Its own slot — see
	// handleWorktreeListReq for why it is not browseScanning's.
	worktreeScanning atomic.Bool
```

In the `handleMessage` switch, beside `case ipc.MsgGitReposReq:`:

```go
	case ipc.MsgWorktreeListReq:
		d.handleWorktreeListReq(conn, msg)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS.

- [ ] **Step 6: Add the slot-independence test**

Append to `internal/daemon/worktree_test.go`. It claims through the handler's own path, not by setting the atomic — a test that pokes `d.worktreeScanning` directly stays green when the handler is wired to the wrong slot:

```go
// The worktree slot is independent of the browse and git-discovery slots: the
// setup dialog resolves a directory and then lists its worktrees, so a shared
// guard would fail each step exactly when it followed the other.
func TestBeginWorktreeList_SlotIsIndependent(t *testing.T) {
	d := &Daemon{}
	d.browseScanning.Store(true)
	d.gitDiscovering.Store(true)

	msg, err := ipc.NewMessage(ipc.MsgWorktreeListReq, ipc.WorktreeListReqPayload{Path: "/x"})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if _, ok := d.beginWorktreeList(msg); !ok {
		t.Fatal("claim refused while only the OTHER slots are held")
	}
	// Second claim must be refused, and the rejection must still echo Path.
	rejection, ok := d.beginWorktreeList(msg)
	if ok {
		t.Fatal("second claim should be refused")
	}
	if rejection.Path != "/x" {
		t.Errorf("rejection Path = %q, want /x — a rejection that drops the key is discarded as stale", rejection.Path)
	}
}
```

- [ ] **Step 7: Run tests and vet**

Run: `./scripts/dev.sh test internal/daemon && ./scripts/dev.sh vet internal/daemon`
Expected: PASS, no vet output.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/worktree.go internal/daemon/worktree_test.go internal/daemon/daemon.go
git commit -m "feat(daemon): answer worktree listings over IPC"
```

---

### Task 3: TUI client state

**Files:**
- Create: `internal/tui/worktree_client.go`
- Create: `internal/tui/worktree_client_test.go`
- Modify: `internal/tui/model.go` (one field; two `Update` cases)

**Interfaces:**
- Consumes: Task 1's payloads; `(*Model).nextReqGen()`, `m.client.Send`.
- Produces: `worktreeState` struct, `(*Model).requestWorktrees(path string) tea.Cmd`, `(*Model).applyWorktreeList(msg worktreeListMsg)`, `(*Model).applyWorktreeTimeout(msg worktreeTimeoutMsg)`, message types `worktreeListMsg{Resp, Gen}` and `worktreeTimeoutMsg{path, gen}`, and `var worktreeScanTimeout = 8 * time.Second`.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/worktree_client_test.go`:

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

// A response for a directory the user has left is dropped. The path is the
// content key; gen identifies WHICH request, because two requests for the same
// path are otherwise wire-indistinguishable.
func TestApplyWorktreeList_DropsStaleResponses(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/b", gen: "2", pending: true}

	m.applyWorktreeList(worktreeListMsg{
		Gen:  "1",
		Resp: ipc.WorktreeListRespPayload{Path: "/a", Repo: true},
	})
	if m.worktrees.loaded {
		t.Error("a response for another request must not land")
	}

	m.applyWorktreeList(worktreeListMsg{
		Gen:  "2",
		Resp: ipc.WorktreeListRespPayload{Path: "/b", Repo: true, Worktrees: []ipc.WorktreeInfo{{Path: "/b", Main: true}}},
	})
	if !m.worktrees.loaded || !m.worktrees.repo {
		t.Error("the matching response must land")
	}
	if m.worktrees.pending {
		t.Error("pending must clear on a matching response")
	}
}

// A timeout may only clear the request it belongs to. Without the gen check a
// previous request's late tick wipes the listing the current one just
// delivered.
func TestApplyWorktreeTimeout_OnlyFiresForItsOwnRequest(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/b", gen: "2", pending: true}

	m.applyWorktreeTimeout(worktreeTimeoutMsg{path: "/b", gen: "1"})
	if !m.worktrees.pending {
		t.Error("a stale timeout must not clear a live request")
	}

	m.applyWorktreeTimeout(worktreeTimeoutMsg{path: "/b", gen: "2"})
	if m.worktrees.pending {
		t.Error("the matching timeout must clear pending")
	}
	if m.worktrees.err == "" {
		t.Error("a timeout must record a reason, or the field says 'no worktrees' for a scan that never answered")
	}
}

// "Not a repository" and "the scan failed" are different facts and only one of
// them may be rendered as an absence of worktrees.
func TestApplyWorktreeList_NotARepoIsNotAnError(t *testing.T) {
	m := &Model{}
	m.worktrees = worktreeState{path: "/tmp", gen: "1", pending: true}
	m.applyWorktreeList(worktreeListMsg{
		Gen:  "1",
		Resp: ipc.WorktreeListRespPayload{Path: "/tmp"},
	})
	if m.worktrees.err != "" {
		t.Errorf("err = %q, want empty", m.worktrees.err)
	}
	if m.worktrees.repo {
		t.Error("repo should be false")
	}
	if !m.worktrees.loaded {
		t.Error("the answer still counts as loaded — the field must stop saying 'scanning'")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `undefined: worktreeState`, `undefined: worktreeListMsg`.

- [ ] **Step 3: Write the implementation**

Create `internal/tui/worktree_client.go`:

```go
package tui

import (
	"log"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/artyomsv/quil/internal/ipc"
)

// worktreeListMsg carries one worktree-listing response. Gen echoes the
// requesting message's ID — see worktreeState.gen.
type worktreeListMsg struct {
	Resp ipc.WorktreeListRespPayload
	Gen  string
}

// worktreeTimeoutMsg fires when a listing request went unanswered.
type worktreeTimeoutMsg struct{ path, gen string }

// worktreeScanTimeout bounds one listing round trip from the client side.
// Matches gitScanTimeout for the same reason: this request can cross ssh,
// where a first round trip after an idle period pays a handshake. Still inside
// the daemon's own 10 s bound, so a slow-but-working scan reports rather than
// being pre-empted here. A var so the test binary can shorten it.
var worktreeScanTimeout = 8 * time.Second

// worktreeState tracks the worktree listing for one directory.
//
// pending is an explicit flag rather than a derived one because "" is a VALID
// path (the daemon's default directory), so the zero value cannot double as
// the idle sentinel — the same constraint browseState documents.
//
// loaded distinguishes "answered" from "never asked": a directory that is not
// a repository produces repo=false with no error, and the field must render
// that differently from a scan still in flight.
type worktreeState struct {
	path    string // echoed back by the daemon; identifies WHAT was asked
	gen     string // identifies WHICH request; see requestWorktrees
	pending bool
	loaded  bool

	repo         bool
	root         string
	worktreeRoot string
	list         []ipc.WorktreeInfo
	err          string
}

// requestWorktrees asks the daemon which worktrees the repository containing
// path has. Used in local mode too, deliberately: the answer is identical when
// the daemon is local, and a path exercised only by remote sessions is one
// that rots.
func (m *Model) requestWorktrees(path string) tea.Cmd {
	gen := m.nextReqGen()
	m.worktrees = worktreeState{path: path, gen: gen, pending: true}
	return tea.Batch(
		func() tea.Msg {
			msg, err := ipc.NewMessage(ipc.MsgWorktreeListReq, ipc.WorktreeListReqPayload{Path: path})
			if err != nil {
				log.Printf("worktree list: encode: %v", err)
				return nil
			}
			// respondTo echoes ID verbatim, which is what lets
			// applyWorktreeList tell two requests for the same path apart.
			msg.ID = gen
			m.client.Send(msg)
			return nil
		},
		worktreeTimeoutCmd(path, gen),
	)
}

func worktreeTimeoutCmd(path, gen string) tea.Cmd {
	return tea.Tick(worktreeScanTimeout, func(time.Time) tea.Msg {
		return worktreeTimeoutMsg{path: path, gen: gen}
	})
}

// applyWorktreeList lands a response, dropping any that does not match the
// request currently in flight on BOTH keys.
func (m *Model) applyWorktreeList(msg worktreeListMsg) {
	if !m.worktrees.pending || msg.Gen != m.worktrees.gen || msg.Resp.Path != m.worktrees.path {
		return
	}
	m.worktrees.pending = false
	m.worktrees.loaded = true
	m.worktrees.repo = msg.Resp.Repo
	m.worktrees.root = msg.Resp.Root
	m.worktrees.worktreeRoot = msg.Resp.WorktreeRoot
	m.worktrees.list = msg.Resp.Worktrees
	m.worktrees.err = msg.Resp.Error
}

// applyWorktreeTimeout gives up on a request. Gated on pending AND both keys:
// a previous request's late tick would otherwise wipe the listing the current
// one just delivered.
func (m *Model) applyWorktreeTimeout(msg worktreeTimeoutMsg) {
	if !m.worktrees.pending || msg.gen != m.worktrees.gen || msg.path != m.worktrees.path {
		return
	}
	m.worktrees.pending = false
	m.worktrees.loaded = true
	// A reason, not an empty list: "the scan never answered" and "this
	// repository has one worktree" must not render identically.
	m.worktrees.err = "worktree scan timed out"
}
```

- [ ] **Step 4: Wire the Model field and Update cases**

In `internal/tui/model.go`, beside the `repoScan` field:

```go
	worktrees worktreeState // create-pane dialog's worktree listing
```

In `Update`, beside the `gitReposMsg` case. **The response case MUST re-arm `m.listenForMessages()`; the timeout case MUST NOT** — the timeout is a local `tea.Tick`, and re-arming the listen loop from it double-arms the reader:

```go
	case worktreeListMsg:
		m.applyWorktreeList(msg)
		return m, m.listenForMessages()

	case worktreeTimeoutMsg:
		m.applyWorktreeTimeout(msg)
		return m, nil
```

In `listenForMessages`'s type switch, beside the `ipc.MsgGitReposResp` arm:

```go
	case ipc.MsgWorktreeListResp:
		var resp ipc.WorktreeListRespPayload
		if err := msg.DecodePayload(&resp); err != nil {
			log.Printf("worktree list: decode: %v", err)
			return listenContinueMsg{}
		}
		return worktreeListMsg{Resp: resp, Gen: msg.ID}
```

> A decode-error branch returns `listenContinueMsg{}`, never `nil`. `nil` yields no message, so `Update` never reaches a re-arm branch and the listen loop dies for the session on one malformed frame.

- [ ] **Step 5: Run tests and vet**

Run: `./scripts/dev.sh test internal/tui && ./scripts/dev.sh vet internal/tui`
Expected: PASS, no vet output.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/worktree_client.go internal/tui/worktree_client_test.go internal/tui/model.go
git commit -m "feat(tui): track worktree listings from the daemon"
```

---

### Task 4: The Worktree field — presence and render

**Files:**
- Modify: `internal/tui/dialog.go` (`setupFieldCount`, `setupFieldKind`, `renderCreatePaneSetupDialog`)
- Modify: `internal/tui/setup_dialog_test.go`

**Interfaces:**
- Consumes: `worktreeState` (Task 3).
- Produces: `"worktree"` as a `setupFieldKind` result; `(m Model) renderSetupWorktreeField(focused bool) string`; `(m Model) worktreeVisibleRows() int`.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/setup_dialog_test.go`:

```go
// The field is ALWAYS present for a prompts_cwd plugin, never conditionally
// inserted. setupFieldCount/setupFieldKind are pure functions of the plugin;
// making the row count depend on whether the browsed directory is a repository
// changes it WHILE the dialog is open, stranding the cursor on a field that no
// longer exists.
func TestSetupFieldCount_WorktreeRowAlwaysPresent(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{}
	p.Command.PromptsCWD = true

	// cwd + worktree + continue
	if got := m.setupFieldCount(p); got != 3 {
		t.Fatalf("setupFieldCount = %d, want 3", got)
	}
	if kind, _ := m.setupFieldKind(p, 1); kind != "worktree" {
		t.Errorf("field 1 = %q, want worktree", kind)
	}
	if kind, _ := m.setupFieldKind(p, 2); kind != "continue" {
		t.Errorf("field 2 = %q, want continue", kind)
	}
}

// Order is CWD -> kube -> toggles -> worktree -> session -> Continue. The
// worktree field is downstream of CWD because its contents are scoped to that
// directory, and upstream of session because the session listing is scoped to
// whichever directory the worktree choice settles on.
func TestSetupFieldKind_WorktreeSitsBetweenTogglesAndSession(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{}
	p.Command.PromptsCWD = true
	p.Command.Sessions = "claude"

	if kind, _ := m.setupFieldKind(p, 1); kind != "worktree" {
		t.Errorf("field 1 = %q, want worktree", kind)
	}
	if kind, _ := m.setupFieldKind(p, 2); kind != "session" {
		t.Errorf("field 2 = %q, want session", kind)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `setupFieldCount = 2, want 3`.

- [ ] **Step 3: Implement presence**

In `setupFieldCount`, after the `PromptsCWD` increment:

```go
	if p.Command.PromptsCWD {
		n++ // the worktree field, scoped to the CWD above it
	}
```

In `setupFieldKind`, insert AFTER the toggles block and BEFORE the session block:

```go
	if p.Command.PromptsCWD {
		if i == 0 {
			return "worktree", -1
		}
		i--
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 5: Implement the render**

Add to `internal/tui/dialog.go`, modelled on `renderSetupSessionField`:

```go
// renderSetupWorktreeField draws the worktree picker.
//
// Collapsed to one summary line while unfocused, for the reason the session
// field is: a dialog already carrying a directory browser must not grow a
// second full-height list for a field most panes never touch.
//
// Four states render DIFFERENTLY on purpose. A scan still in flight showing
// "no worktrees" is a confidently wrong answer, and "this is not a
// repository" is a different fact from "the scan failed" — only one of them
// justifies telling the user there is nothing here.
func (m Model) renderSetupWorktreeField(focused bool) string {
	var b strings.Builder
	label := "  Worktree"
	if focused {
		label = "> Worktree"
	}

	switch {
	case m.worktrees.pending:
		b.WriteString(dialogNormal.Render(label + "    scanning…"))
		return b.String()
	case !m.worktrees.loaded:
		b.WriteString(dialogSubtle.Render(label + "    —"))
		return b.String()
	case m.worktrees.err != "":
		b.WriteString(dialogNormal.Render(label + "    "))
		b.WriteString(dialogSubtle.Render(truncateToWidth(
			sanitizeRemoteText(m.worktrees.err), m.setupTextWidth()-12)))
		return b.String()
	case !m.worktrees.repo:
		b.WriteString(dialogSubtle.Render(label + "    not a git repository"))
		return b.String()
	}

	if !focused {
		summary := "off"
		if m.selectedWorktree != "" {
			summary = sanitizeRemoteText(worktreeLabel(m.worktrees.list, m.selectedWorktree))
		}
		b.WriteString(dialogNormal.Render(label + "    " + truncateToWidth(summary, m.setupTextWidth()-12)))
		return b.String()
	}

	b.WriteString(dialogNormal.Render(label))
	b.WriteString("\n")
	rows := m.worktreeRows()
	visible := m.worktreeVisibleRows()
	start := m.worktreeScroll
	end := start + visible
	if end > len(rows) {
		end = len(rows)
	}
	for i := start; i < end; i++ {
		mark := "    "
		if i == m.worktreeCursor {
			mark = "  > "
		} else if rows[i].path == m.selectedWorktree {
			mark = setupRowIdleMark
		}
		text := sanitizeRemoteText(rows[i].label)
		if rows[i].disabled {
			b.WriteString(dialogSubtle.Render(mark + truncateToWidth(text, m.setupTextWidth()-4)))
		} else {
			b.WriteString(dialogNormal.Render(mark + truncateToWidth(text, m.setupTextWidth()-4)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// worktreeRow is one selectable line: row 0 is always "off".
type worktreeRow struct {
	label    string
	path     string // "" for the off row
	disabled bool
}

// worktreeRows builds the field's rows. The MAIN checkout is excluded: it is
// not a worktree to attach to, it is the directory the CWD field already
// chose. Locked and prunable entries are shown but refused, so the user learns
// why a row is unavailable rather than wondering where it went.
func (m Model) worktreeRows() []worktreeRow {
	rows := []worktreeRow{{label: "off — use the directory above", path: ""}}
	for _, w := range m.worktrees.list {
		if w.Main {
			continue
		}
		name := w.Branch
		if name == "" {
			name = "(detached)"
		}
		row := worktreeRow{label: name + "  " + w.Path, path: w.Path}
		switch {
		case w.Prunable:
			row.label, row.disabled = name+"  (directory is gone)", true
		case w.Locked:
			row.label, row.disabled = name+"  (locked)", true
		}
		rows = append(rows, row)
	}
	return rows
}

// worktreeLabel resolves a stored path back to its display name for the
// collapsed summary.
func worktreeLabel(list []ipc.WorktreeInfo, path string) string {
	for _, w := range list {
		if w.Path == path {
			if w.Branch != "" {
				return w.Branch
			}
			return w.Path
		}
	}
	return path
}
```

Add the two cursor fields beside the session ones on `Model`:

```go
	selectedWorktree string // chosen worktree PATH; "" = off
	worktreeCursor   int
	worktreeScroll   int
```

Add the `"worktree"` case to `renderCreatePaneSetupDialog`'s field loop, calling `renderSetupWorktreeField(focused)`.

- [ ] **Step 6: Run tests and vet**

Run: `./scripts/dev.sh test internal/tui && ./scripts/dev.sh vet internal/tui`
Expected: PASS, no vet output.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/dialog.go internal/tui/model.go internal/tui/setup_dialog_test.go
git commit -m "feat(tui): show a worktree field in the pane setup dialog"
```

---

### Task 5: Shared row budget

**Files:**
- Modify: `internal/tui/dialog.go` (`sessionVisibleRows`, new `worktreeVisibleRows`)
- Modify: `internal/tui/setup_dialog_test.go`

**Interfaces:**
- Consumes: Task 4's render.
- Produces: `(m Model) worktreeVisibleRows() int`, with `sessionVisibleRows` unchanged in signature.

**Why this is its own task:** `lipgloss.Place` does not clip. Two independently-capped expanding lists can each fit while their sum does not, which pushes `[Continue]` off the bottom — the exact bug `sessionVisibleRows` exists to prevent, reintroduced by adding a second list beside it.

- [ ] **Step 1: Write the failing test**

```go
// Two expanding lists must not each fit while their SUM overflows. lipgloss
// does not clip, so the overflow silently pushes [Continue] off the terminal.
func TestSetupDialog_TwoListsShareOneHeightBudget(t *testing.T) {
	for _, h := range []int{12, 16, 24, 40} {
		m := Model{width: 100, height: h}
		total := m.worktreeVisibleRows() + m.sessionVisibleRows()
		if total > h-setupChromeRows {
			t.Errorf("height %d: worktree(%d) + session(%d) = %d rows, exceeds the %d available",
				h, m.worktreeVisibleRows(), m.sessionVisibleRows(), total, h-setupChromeRows)
		}
		if m.worktreeVisibleRows() < 1 {
			t.Errorf("height %d: worktree list must keep at least one row", h)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `./scripts/dev.sh test internal/tui -run TestSetupDialog_TwoListsShareOneHeightBudget`
Expected: FAIL — `undefined: worktreeVisibleRows` (and `setupChromeRows` if not yet present).

- [ ] **Step 3: Implement**

`sessionVisibleRows` (`internal/tui/sessions.go:37`) already exists and reads:

```go
func (m Model) sessionVisibleRows() int {
	const surroundingRows = 26
	avail := m.height - surroundingRows
	switch {
	case avail >= sessionListVisibleRows:
		return sessionListVisibleRows
	case avail < sessionListMinRows:
		return sessionListMinRows
	default:
		return avail
	}
}
```

`surroundingRows` is the fixed chrome measured against the shipped
claude-code layout — and the worktree field adds one more fixed row to that
chrome plus a variable list. So: add `worktreeVisibleRows`, and subtract it
inside `sessionVisibleRows` so the two share one budget.

In `internal/tui/dialog.go`:

```go
// worktreeListVisibleRows / worktreeListMinRows mirror the session field's
// pair. The max is deliberately small: worktree rows are one line each and a
// repository rarely has many, while a session list is the long one, so the
// session field keeps the larger share of a short terminal.
const (
	worktreeListVisibleRows = 6
	worktreeListMinRows     = 1
)

// worktreeVisibleRows caps the worktree list. The floor is 1, never a
// friendlier number: lipgloss.Place does not clip, so any floor above the
// height actually available manufactures the overflow it looks like it
// prevents — the same reasoning as historyMinRows.
func (m Model) worktreeVisibleRows() int {
	// One more than the session field's chrome: the collapsed worktree row
	// itself is always drawn, whether or not the list below it is.
	const surroundingRows = 27
	avail := m.height - surroundingRows
	switch {
	case avail >= worktreeListVisibleRows:
		return worktreeListVisibleRows
	case avail < worktreeListMinRows:
		return worktreeListMinRows
	default:
		return avail
	}
}
```

In `internal/tui/sessions.go`, subtract the worktree list from what the
session field may claim:

```go
func (m Model) sessionVisibleRows() int {
	const surroundingRows = 26
	// The worktree list is drawn in the same content area. Two lists that
	// each fit while their SUM does not is precisely the overflow this
	// function exists to prevent.
	avail := m.height - surroundingRows - m.worktreeVisibleRows()
	switch {
	case avail >= sessionListVisibleRows:
		return sessionListVisibleRows
	case avail < sessionListMinRows:
		return sessionListMinRows
	default:
		return avail
	}
}
```

Define `setupChromeRows = 26` in the test file (or reference the same value) so the test's budget assertion matches the implementation's.

- [ ] **Step 4: Run tests**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dialog.go internal/tui/setup_dialog_test.go
git commit -m "fix(tui): share one height budget between the setup dialog lists"
```

---

### Task 6: Key handling, submit, and CWD-change reset

**Files:**
- Modify: `internal/tui/dialog.go` (`handleCreatePaneSetupKey`, `onSetupFieldFocused`, `handleSetupCWDKey`, `submitSetupDialog`, `resetProjectBrowseState` equivalent for the setup dialog)
- Modify: `internal/tui/setup_dialog_test.go`

**Interfaces:**
- Consumes: Tasks 3–5.
- Produces: `(m Model) handleSetupWorktreeKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd)`.

- [ ] **Step 1: Write the failing tests**

```go
// The chosen worktree becomes the pane's spawn CWD. This is the whole of
// "attach" — CWD is an ordinary CreatePanePayload field, so no create-path
// change is needed.
func TestSubmitSetupDialog_WorktreeBecomesTheCWD(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{}
	p.Command.PromptsCWD = true
	m.cwdBrowseDir = "/repo"
	m.selectedWorktree = "/repo-worktrees/feat-x"

	updated, _ := m.submitSetupDialog(p)
	got := updated.(Model)
	if got.selectedCWD != "/repo-worktrees/feat-x" {
		t.Errorf("selectedCWD = %q, want the worktree path", got.selectedCWD)
	}
}

// Every value in the field is scoped to ONE repository: a listed worktree
// belongs to it. Carrying a choice across a directory change would submit a
// pane into a worktree of a repository the user navigated away from.
func TestSetupDialog_ChangingTheDirectoryClearsTheWorktree(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{}
	p.Command.PromptsCWD = true
	m.cwdBrowseDir = "/repo"
	m.selectedWorktree = "/repo-worktrees/feat-x"

	m.onSetupCWDChanged(p, "/other")
	if m.selectedWorktree != "" {
		t.Errorf("selectedWorktree = %q, want cleared", m.selectedWorktree)
	}
}

// The session listing is scoped to the directory the pane will actually spawn
// in. submit is the load-bearing check: the user can pick a session, change
// the worktree, and press Continue without ever re-focusing the session field.
func TestSubmitSetupDialog_WorktreeChangeDropsAStaleSession(t *testing.T) {
	m := Model{}
	p := &plugin.PanePlugin{}
	p.Command.PromptsCWD = true
	p.Command.Sessions = "claude"
	m.cwdBrowseDir = "/repo"
	m.sessionScanCWD = "/repo"
	m.selectedSessionID = "11111111-1111-1111-1111-111111111111"
	m.selectedWorktree = "/repo-worktrees/feat-x"

	updated, _ := m.submitSetupDialog(p)
	if got := updated.(Model); got.selectedSessionID != "" {
		t.Errorf("selectedSessionID = %q, want dropped — it was listed for a different directory", got.selectedSessionID)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `undefined: onSetupCWDChanged`, and `selectedCWD` still `/repo`.

- [ ] **Step 3: Implement**

Add the effective-directory helper and use it everywhere the spawn directory is needed:

```go
// setupSpawnDir is the directory the pane will actually start in: the chosen
// worktree when there is one, else the browsed directory. Every consumer of
// "where will this pane spawn" must read THIS, not cwdBrowseDir — the session
// listing scope and the submitted CWD are the two that matter.
func (m Model) setupSpawnDir() string {
	if m.selectedWorktree != "" {
		return m.selectedWorktree
	}
	return m.cwdBrowseDir
}
```

In `submitSetupDialog`, replace the CWD capture:

```go
	if p.Command.PromptsCWD {
		m.selectedCWD = m.setupSpawnDir()
	}
```

and widen the existing session guard to compare against the spawn dir:

```go
	if m.selectedSessionID != "" && m.sessionScanCWD != m.setupSpawnDir() {
		m.selectedSessionID = ""
	}
```

Add the reset, called from every site that commits a new browsed directory in `handleSetupCWDKey` and `handleSetupPickKey`:

```go
// onSetupCWDChanged reacts to the browsed directory moving: the worktree
// choice and its listing both belong to the repository just left.
func (m *Model) onSetupCWDChanged(p *plugin.PanePlugin, dir string) tea.Cmd {
	m.cwdBrowseDir = dir
	m.selectedWorktree = ""
	m.worktreeCursor = 0
	m.worktreeScroll = 0
	m.worktrees = worktreeState{}
	return nil
}
```

In `onSetupFieldFocused`, fire the scan on first focus per directory, and retry after a failure — only `pending` and a `loaded` success count as settled, so a timeout invites the retry it would otherwise refuse:

```go
	case "worktree":
		if m.worktrees.pending {
			return nil
		}
		if m.worktrees.loaded && m.worktrees.err == "" && m.worktrees.path == m.cwdBrowseDir {
			return nil
		}
		return m.requestWorktrees(m.cwdBrowseDir)
```

Add the key handler, dispatched from `handleCreatePaneSetupKey`'s `"worktree"` case:

```go
// handleSetupWorktreeKey moves the cursor within the worktree list and commits
// a choice.
//
// A disabled row still ACCEPTS the cursor and refuses Enter, rather than being
// skipped: the row exists to explain why that worktree is unavailable, and a
// row the cursor cannot reach explains nothing. Same choice the resume picker
// makes for an in-use session.
func (m Model) handleSetupWorktreeKey(p *plugin.PanePlugin, key string) (tea.Model, tea.Cmd) {
	rows := m.worktreeRows()
	if len(rows) == 0 {
		return m, nil
	}
	switch key {
	case "up", "k":
		if m.worktreeCursor > 0 {
			m.worktreeCursor--
		}
	case "down", "j":
		if m.worktreeCursor < len(rows)-1 {
			m.worktreeCursor++
		}
	case "enter":
		row := rows[m.worktreeCursor]
		if row.disabled {
			return m, nil
		}
		m.selectedWorktree = row.path
		// The session listing is scoped to the directory the pane will spawn
		// in, which this just changed. Clearing here is the responsive half;
		// submitSetupDialog's guard is the load-bearing one, since the user
		// can reach Continue without re-focusing the session field.
		m.selectedSessionID = ""
		return m, nil
	default:
		return m, nil
	}
	m.worktreeScroll = worktreeWindow(len(rows), m.worktreeCursor,
		m.worktreeScroll, m.worktreeVisibleRows())
	return m, nil
}

// worktreeWindow returns the scroll origin that keeps cursor visible.
//
// Pure, and called by BOTH the key handler and the render, because render must
// not depend on Update having run — a WindowSizeMsg can change the row budget
// between them. It re-derives the origin from the cursor and clamps to
// total-visible AFTER, in that order: a stored scroll can outlive the list it
// was computed for and would otherwise draw blank rows past the last entry.
// Identical in shape to historyWindow; if that helper is already generic
// enough to share, call it instead of adding this one.
func worktreeWindow(total, cursor, scroll, visible int) int {
	if visible <= 0 || total <= visible {
		return 0
	}
	if cursor < scroll {
		scroll = cursor
	}
	if cursor >= scroll+visible {
		scroll = cursor - visible + 1
	}
	if max := total - visible; scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 5: Full suite and vet**

Run: `./scripts/dev.sh test && ./scripts/dev.sh vet`
Expected: all packages ok, no vet output.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/dialog.go internal/tui/setup_dialog_test.go
git commit -m "feat(tui): spawn a pane in the worktree chosen in setup"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/features.md`, `CHANGELOG.md`

- [ ] **Step 1: Document the field in `docs/features.md`**

Under the pane-creation section, describing behaviour rather than implementation: the field appears for any plugin that asks for a directory; it lists the repository's existing worktrees; choosing one spawns the pane there; the main checkout is not listed because the directory field already selects it; locked and prunable worktrees are shown but not selectable; a non-repository directory renders the field inert. State explicitly that stage A does not create worktrees.

- [ ] **Step 2: Add the `CHANGELOG.md` entry**

Under `## [Unreleased]` → `### Added`, user-facing, saying what was not possible before:

```markdown
- **A new pane can now be opened directly in one of the repository's git
  worktrees.** The create-pane dialog lists them under the directory you pick,
  so an agent, a shell and lazygit can each sit in the same worktree and the
  sidebar shows each pane's real branch instead of repeating the main
  checkout's. Quil does not create worktrees yet — it offers the ones you
  already have.
```

- [ ] **Step 3: Commit**

```bash
git add docs/features.md CHANGELOG.md
git commit -m "docs: describe the setup dialog's worktree field"
```

---

## Manual verification

Docker tests cannot exercise a real repository through the dialog. After Task 7:

1. `./scripts/dev.sh build`
2. In the project, create a worktree by hand: `git worktree add -b test/wt-plan ../quil-worktrees/test-wt-plan`
3. Launch the dev TUI: `./scripts/quil-dev.ps1` — confirm `[dev]` appears in the status bar before doing anything else.
4. `Ctrl+N` → a `prompts_cwd` plugin → browse to the repo root → focus Worktree → confirm the new worktree is listed and the main checkout is not.
5. Select it, Continue. Confirm the pane's shell starts in the worktree and the sidebar's git row shows `test/wt-plan` with the ` wt` marker, while other panes still show the main branch.
6. Browse to a non-repository directory and confirm the field reads `not a git repository`.
7. Clean up: close the pane, then `git worktree remove ../quil-worktrees/test-wt-plan`.

## Self-review notes

- **Spec coverage:** stage A's scope in the spec is `internal/gitworktree` (done), the list RPC (Tasks 1–2), the client state (Task 3), the field (Tasks 4–6), and docs (Task 7). The spec's `new branch` mode, `[git]` config, branch validation, and the session field's whole-field inert state are all stage B and deliberately absent.
- **Not in this plan, by design:** `gitworktree.Add` stays unused; `WorktreeRoot` is carried on the wire but only displayed, so stage B does not change the contract.
- **Known follow-up:** Task 6's `handleSetupWorktreeKey` reuses the session field's scroll-window logic. If that logic is not already extracted into a shared helper, extract it rather than copying — a second copy drifts.
