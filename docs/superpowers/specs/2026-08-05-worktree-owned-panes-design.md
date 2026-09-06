# Worktree-Owned Panes — Design

| Field | Value |
|---|---|
| Date | 2026-08-05 |
| Status | **Stage A shipped** (v1.51.0). Stage B's three open decisions closed 2026-08-06 — see the plan's "Decisions this plan closes" |
| Plan | Stage A: `docs/superpowers/plans/2026-08-05-worktree-panes-stage-a.md` · Stage B: `docs/superpowers/plans/2026-08-06-worktree-panes-stage-b.md` |
| Related | `.claude/rules/projects.md` (git subsystem), `.claude/rules/remote-dialogs.md` (daemon-side FS calls), `.claude/rules/hooks-and-sessions.md` (transcript paths) |

## Problem

The project sidebar shows a branch under every pane, and in a typical workspace
every one of them shows the **same** branch. That is not a rendering fault — it
is the truth about where those panes are.

Quil learns a pane's working directory from exactly one source: the OSC 7
sequence the daemon injects into **shells** via `internal/shellinit/`. The TUI's
VT emulator parses it (`internal/tui/pane.go:288`) and writes it back through
`MsgUpdatePane` (`internal/daemon/daemon.go:1946`). An AI pane runs `claude`,
not a shell, so it emits no OSC 7 and its `CWD` is frozen at spawn for the life
of the pane.

The gap this opens is the one that matters in practice. The normal way to run
several agents on one repository is to give each a linked worktree. But an agent
asked to "work in a worktree" does not `cd` — it creates the worktree and writes
to absolute paths inside it, while its own process stays exactly where it
started. There is no observable signal. The sidebar reports the spawn
directory's branch, which is correct about the pane and useless about the work.

So the git state Quil renders is accurate and uninformative at the same time,
and the isolation the user set up is invisible to the tool whose job is showing
them what their agents are doing.

## Solution overview

Make the pane **own** the worktree: create it at pane-creation time and spawn
the pane inside it. The pane's CWD then *is* the worktree, so every existing
mechanism reports the truth with no inference and no new signal.

```
 Ctrl+N → claude-code → setup dialog
┌────────────────────────────────────────┐
│ Directory   ▸ E:/Projects/Stukans/quil │
│ Worktree    ▸ new branch               │
│               quil/feat-sidebar█       │
│               → ../quil-worktrees/…    │
│ Session       (disabled — new worktree)│
│                                        │
│              [ Continue ]              │
└────────────────────────────────────────┘

 E:/Projects/Stukans/
   quil/                       ← main checkout, other panes
   quil-worktrees/
     quil-feat-sidebar/        ← this pane spawns here
```

The sidebar needs no change to benefit. `gitinfo.Dirs` already keys the git
cache on the **per-checkout** git dir rather than the repository's common dir,
specifically so linked worktrees on different branches do not collapse into one
entry, and `gitRow` already renders the ` wt` marker. That code was written for
a case that barely occurred; this feature is what makes it load-bearing.

## Staging

Review split this into two stages, and the split is a real seam rather than a
convenience: everything up to and including *choosing* a worktree is independent
of the machinery that *creates* one.

**Stage A — plannable now, and it SHIPS a working feature rather than an inert
field.** `internal/gitworktree`, the `MsgWorktreeListReq` pair, and the setup
dialog's Worktree field including its row budget, its branch-name validation,
and the session-picker scoping. Nothing here mutates a repository.

The reason this is a feature and not scaffolding: **attaching to an existing
worktree needs no create path at all.** `CWD` is an ordinary field on
`CreatePanePayload` (`dialog.go:1685-1692`), so "attach" is just a create whose
CWD happens to be a worktree — the daemon already resolves it, spawns there, and
the git cache already keys it per-checkout and renders the ` wt` marker. So
stage A delivers the case that motivated attach in the first place: an agent, a
terminal and lazygit on one branch together, each in the same worktree.

Only **creating** a branch needs the daemon-side worker, which is the entirety
of stage B's risk. That is also why the field's two modes are worth building in
this order rather than together: the cheaper mode is the one with no failure
path to design.

**Stage B — needs one more design pass.** The create path. Three decisions are
open and each changes the shape of the code:

1. Whether replace mode accepts a worktree or refuses one.
2. What a restored pane does when its worktree is gone — error pane, restore
   checklist failure, or prompt — and whether worktree ownership is persisted
   on the pane to make that case detectable at all. The snapshot stores only
   CWD today, so restore cannot currently tell a missing worktree from a
   missing browsed directory.
3. The add's timeout and permit budget, which is a question about the shared
   blocking-FS pool rather than about this feature.

**Stage A does not render the `new branch` mode at all.** Offering a mode that
cannot complete is worse than not offering it, and excluding it moves three
things out of stage A with it: branch-name validation, the `[git]` config
section, and the session field's whole-field inert state — that last one is
only reachable via `new branch`, since attaching to an existing worktree
re-scopes the session listing rather than disabling it. Stage A's field has
exactly three states: inert (not a repository), `off`, and one row per existing
worktree.

Stage A is not blocked on any of them.

## Decisions taken

| Decision | Choice | Rationale |
|---|---|---|
| Attribution mechanism | **The pane owns the worktree** | An agent that "moves into" a worktree does not move its process. Inference has no signal to read; ownership needs none |
| Worktree location | **Sibling** — `<parent>/<repo>-worktrees/<name>` | Keeps the repo tree clean, needs no `.gitignore` entry, and no tool that walks the tree (ripgrep, file watchers, the agent itself) finds a second full checkout nested in the first |
| Branch | **New branch off HEAD, name prompted** | The branch is the identity of the work. Auto-generated names produce branches the user did not choose and cannot recognise a week later |
| Existing branches | **Not offered in create mode** | Checking out a branch already live in another worktree fails at the git level and needs its own error path. Attaching to the existing *worktree* covers the real case |
| Attach to existing worktree | **Yes, listed in the field** | Worktrees are never auto-removed, so they accumulate. Attaching is how an agent, a terminal, and lazygit end up on one branch together |
| Cleanup | **Never automatic** | Closing a pane must not be able to delete uncommitted agent work. Removal is a later, explicit action |
| Which plugins | **Every `prompts_cwd` plugin** | The field is inert unless the chosen directory is a repository, so it costs nothing where it is irrelevant. A shell or lazygit in the agent's worktree is the obvious next thing wanted. Note *inert*, not *absent* — see the TUI section for why the row is always present |
| UX shape | **A field in the existing setup dialog** | Reuses the CWD → kube → toggles → session → Continue machinery. A modal after directory selection is unskippable on every pane created in a repo |
| Where the write runs | **Daemon side, on a worker goroutine** | The repository lives on the daemon's machine. `git worktree add` checks out a tree and can take seconds |
| Failure behaviour | **Report and create no pane** | Falling back to the repo root spawns an agent on `master` while the user believes it is isolated |
| Moving an existing pane | **Deferred to a follow-up** | Physically a respawn — a child's CWD cannot be changed — so it is the `restart_pane` path plus a new CWD, tangled with claude session resume |

## Components

### `internal/gitworktree` — a new package

`internal/gitinfo`'s package documentation states: *"Every call is a read;
nothing here can modify a repository."* That is a real invariant rather than a
courtesy — the git cache runs `gitinfo.Probe` on a ticker against every pane's
checkout, so a package that gains the ability to write is one refactor away from
a ticker that writes. Writes therefore get their own package.

```go
// List reports the repository's linked worktrees, main checkout included.
func List(ctx context.Context, dir string) ([]Worktree, error)

// Add creates a linked worktree at path on a NEW branch off HEAD.
func Add(ctx context.Context, repo, path, branch string) error

type Worktree struct {
    Path     string // absolute, as git reports it
    Branch   string // "" when Detached or Bare
    Detached bool
    Main     bool   // the FIRST porcelain entry; the format has no "main" key
    Locked   bool   // git worktree list --porcelain reports it locked
    Prunable bool   // its directory is gone
    Bare     bool   // no working tree; only ever on the main entry
}
```

**Status: implemented.** `internal/gitworktree` exists with this shape and
seven tests, built test-first. It is the whole of stage A's daemon-side half.

Same shape as its read-only sibling: stdlib-only, one `runGit` package-var seam
so tests never need a repository or a git binary, `hideWindow` on Windows (the
daemon runs `DETACHED_PROCESS` and owns no console, so an unhidden child
allocates a real window), and `cmd.WaitDelay` so a killed git cannot hold its
output pipe open past the deadline.

`List` parses `git worktree list --porcelain`. `Add` runs
`git worktree add -b <branch> <path>` and **never** passes `--force`: a refusal
is a fact to report, not an obstacle to override.

### IPC — one new pair

`MsgWorktreeListReq` / `MsgWorktreeListResp`, following the contract
`.claude/rules/remote-dialogs.md` establishes for every daemon-side filesystem
call:

```go
type WorktreeListReqPayload struct {
    Path string `json:"path"` // a directory that may be inside a repository
}

type WorktreeListRespPayload struct {
    Path      string      `json:"path"`      // echoed VERBATIM
    Repo      bool        `json:"repo"`      // Path resolved to a repository
    Root      string      `json:"root"`      // the repository's main checkout
    // WorktreeRoot is the DIRECTORY NEW WORKTREES GO IN, already joined by the
    // daemon using the daemon's own separators and its own [git] config.
    WorktreeRoot string   `json:"worktree_root"`
    Worktrees []Worktree  `json:"worktrees,omitempty"`
    Truncated bool        `json:"truncated,omitempty"`
    Error     string      `json:"error,omitempty"`
}
```

**`WorktreeRoot` exists because the client must not compute a path on the
daemon's disk.** Deriving `<parent-of-Root>/<repo>-worktrees/` client-side means
running `filepath.Dir` and `filepath.Join` — with the *client's* separators —
over a path that lives on another machine, which is precisely the wrong-machine
class `remote-dialogs.md` documents six instances of. It is also a two-config
problem: `[git] worktree_root` is the daemon's setting, since it describes where
files land on the daemon's disk, while `worktree_prefix` is the client's, since
it only prefills a text field. The daemon therefore sends the joined parent and
the client appends **only the branch segment** for its preview.

Rules inherited, each because omitting it has already cost this codebase a bug:

- **The handler runs on a worker goroutine**, behind its **own** single-flight
  atomic (`worktreeScanning`). Not shared with `browseScanning`: the browse
  holds its slot for as long as the syscall really runs — up to `browseTimeout`
  — while a client gives up at its own shorter deadline, so a shared guard
  fails exactly in the dead-mount case the bound exists for. This is the same
  reasoning that gave `dirsChecking` and `kubeDiscovering` their own slots.
- **The response echoes `Path` verbatim on every path**, including the error and
  single-flight-rejection paths. It is the client's staleness key, not a
  statement about what was read; daemon-side normalisation would make a live
  request look permanently stale.
- **The request carries a per-request generation in `ipc.Message.ID`**, echoed
  by the existing `respondTo`. A client-side counter cannot fix response
  crossing, because two responses for the same content key are
  wire-indistinguishable.
- **Every git invocation takes a `claimBlockingFSCall` permit** and shares one
  deadline across the whole call, for the reason `browse.go` documents: a
  goroutine parked in a syscall pins an OS thread, and a daemon runs for weeks.
- **Branch names and paths are sanitised at render, never in state.** They come
  from a host the user may not control, `lipgloss.Width` measures an escape
  sequence as zero cells so a truncation neither counts nor cuts it, and the
  chosen path becomes the pane's spawn CWD — so `sanitizeRemoteText` belongs on
  the render path and nowhere else.

### `CreatePanePayload` extension

```go
// Worktree, when set, makes the daemon create or attach a linked worktree and
// spawn the pane THERE rather than in CWD. Nil (the default) is unchanged
// behaviour.
//
// Trust: like Overlay and ResumeSessionID, any IPC client can set this.
// Validation differs by MODE and the two are opposites — create refuses a
// Path that already exists, attach requires one that does (and requires it to
// be a worktree OF Repo, not prunable, not the main checkout). Every
// rejection FAILS the create rather than falling back to CWD.
Worktree *WorktreeSpec `json:"worktree,omitempty"`

type WorktreeSpec struct {
    Repo   string `json:"repo"`             // checkout the CWD resolved to
    Branch string `json:"branch,omitempty"` // create mode: new branch off HEAD
    Path   string `json:"path,omitempty"`   // attach mode: existing worktree
}
```

Exactly one of `Branch` / `Path` is set. Both set, or neither, is a rejection —
the field is a discriminated choice and admitting an ambiguous value would make
the daemon guess which mode the user picked.

The MCP bridge deliberately does not expose this field, matching `Overlay` and
`ResumeSessionID`: creating directories and branches on the user's disk is not
something an agent should do as a side effect of opening a pane.

### Daemon — creation is asynchronous

`handleCreatePane` runs on the requesting connection's **dispatch goroutine**.
`git worktree add` checks out a full tree; on a large repository that is
seconds, during which every message from that client — including input — waits.
This is the hazard that moved `browse`, `discover` and `claudesessions` onto
workers, and it applies unchanged here.

So a create carrying a `Worktree` spec hands the work to a worker goroutine and
creates the pane only when it completes:

```
handleCreatePane
  ├─ Worktree == nil → existing path, unchanged
  └─ Worktree != nil → worker goroutine, behind worktreeAdding
       ├─ validate branch (ref shape AND derived-segment shape)
       ├─ re-validate the target TAB still exists
       ├─ gitworktree.Add        (own permit + own timeout)
       ├─ success → create pane with CWD = worktree path
       └─ failure → respond to the REQUESTER, NO pane
```

**The response is a request-response pair, not a broadcast**, and the first
draft of this spec was wrong about that. `MsgPluginError` is emitted through
`d.broadcast` (`daemon.go:2361`), so reusing it would put a modal on *every*
connected client for one client's failed create, and would give the requester
nothing correlatable to act on. The create carrying a `Worktree` spec therefore
uses `Message.ID` / `respondTo` — the same machinery the list RPC uses, and the
same machinery `MsgRestartPaneResp` already uses.

**The requester needs that response because the TUI has already mutated its own
layout by the time the worker starts.** `handleCreatePaneSplit` splits the
layout tree and arms `m.pendingSplit[tab.ID]` *before* the IPC send
(`dialog.go:1683`), and `applyWorkspaceState` fills that placeholder with the
next pane that arrives for the tab, whoever created it (`model.go:4072-4076`).
Today that window is a millisecond; a worktree add stretches it to seconds, and
in those seconds two things go wrong that cannot go wrong now — a pane created
by another client or by MCP `create_pane` fills the placeholder meant for this
one, and a failed add leaves the placeholder armed forever, because nothing on
the failure path unwinds it.

**The unwind itself needs no new machinery, which is the one piece of good news
here.** `LayoutNode.PrunePlaceholders` (`layout.go:230`) already removes a
placeholder leaf by promoting its sibling, and `applyWorkspaceState` already
calls it on two paths (`model.go:4097`, `4152`). So the failure handler is
`PrunePlaceholders` + `delete(m.pendingSplit, tab.ID)` + a layout re-send; the
daemon stores layout opaquely and prunes unknown panes on restore, so a
momentarily stale stored layout is recoverable rather than corrupting. What
stage B has to design is *when* to run it, not *how*.

**Replace mode is worse, is a reachable combination, and is unrecoverable
rather than merely wrong.** `handleCreatePane` dispatches to
`handleReplacePane` before creating anything (`daemon.go:1619-1622`), and the
client-side replace path (`dialog.go:1633-1650`) sets `leaf.Pane = nil` and
calls `old.Dispose()` **immediately, before the send**. A failed add there
leaves the daemon still running the old pane while the client has destroyed its
model of it — and unlike a dangling placeholder, `Dispose()` is not something
`PrunePlaceholders` can undo.

Two ways out, and the plan must pick one rather than inherit the behaviour:
defer the client-side dispose until the response confirms the swap, or **refuse
worktree mode on the replace path**. Refusing is the honest default —
"replace this pane with one in a new worktree" is a compound operation nobody
has asked for, and its failure mode costs a live pane.

**A failed add must never fall back to the repository root.** That spawns an
agent on `master` while the user believes it is isolated — the same class of
confidently-wrong answer as `--continue` attaching to a sibling's conversation,
as `↑0↓0` claiming a checkout is in sync when there is nothing to compare
against, and as a stale branch presented as current. Each of those was fixed by
making the wrong answer unreachable rather than unlikely.

**That guarantee is defeated by the code three lines further on unless this
spec changes it, and saying "fail the create" is not enough.**
`handleCreatePane` (`daemon.go:1600-1610`) validates the incoming CWD and, on
any failure, *silently substitutes the daemon's own directory*:

```go
if cwd != "" {
    if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
        log.Printf("create pane: rejecting cwd %q (err=%v); using daemon default", cwd, err)
        cwd = ""
    } else if resolved, evalErr := filepath.EvalSymlinks(cwd); evalErr == nil {
        cwd = resolved
    }
}
if cwd == "" {
    cwd = d.defaultCWD()
}
```

That is a sound convention for a browsed directory — a stale path should not
cost the user a pane — and exactly the wrong one for a worktree, where the
directory *is* the isolation. A worktree path that fails its stat (a slow
network mount, a drive that unmounted between the add and the spawn) currently
produces a pane in the daemon's default directory with a daemon-log line
nobody reads.

So: **a worktree-sourced CWD is exempt from the fallback.** It has just been
created or listed by the daemon itself, so its absence is a real failure rather
than a stale user input, and the create fails instead of relocating. The
exemption has to be explicit in the code path, because the fallback is what the
surrounding convention does by default.

**There are two other create paths, and the spec covers exactly one.**
`handleReplacePane` (`daemon.go:1619-1622`) is an early return *before* pane
creation, taken whenever `ReplacePaneID` is set — which the setup dialog does
use — so the worker must serve it as well or the worktree is silently ignored
on that branch. `handleCreatePaneReq` (`daemon.go:3985`) is the
request-response variant the MCP bridge uses; it is deliberately **out of
scope**, consistent with `Worktree` not being exposed over MCP, and that is a
decision rather than an omission.

The response carries the failure, and the client unwinds its placeholder on
receipt. A create that is silently ignored is not an option — a daemon that
accepted a create and did nothing has already read as a broken dialog for an
evening once.

**A failure AFTER the add succeeds leaves a worktree the user never saw.** The
add takes seconds, and in them the target tab can be destroyed (so
`session.CreatePane(tabID)` errors) or `spawnPane` can fail. The branch and the
directory now exist, and cleanup is never automatic by decision — so the worker
re-validates the tab immediately before the add to shrink the window, and the
failure message **names the worktree path that now exists on disk**. Reporting
"could not create the pane" while silently leaving a checkout behind is how a
user ends up with worktrees they cannot account for.

**The add needs its own budget, not the list's.** Single-flight, timeout and
permit accounting were specified for the *list* and left implicit for the add,
which is backwards: the list is a `readdir`-class call and the add checks out a
whole tree. So the add takes its own `worktreeAdding` guard, its own timeout
constant (sized for a monorepo checkout — `browseTimeout`'s 10 s is a
directory-read budget and is likely too small), and its permit hold is called
out explicitly. That hold is the hazard: `claimBlockingFSCall`'s pool is sized
for stats and directory reads and is shared with browse, git probes, kube
discovery and session scans, so several concurrent adds holding permits for
seconds each is the same permit-HOLD drain that `sweepRoots` was a P1 for.

Path derivation happens **daemon-side**, because it describes the daemon's disk
and separators belong to the machine holding it:

```
<parent-of-MAIN-checkout>/<main-checkout-name>-worktrees/<branch with "/" → "-">
```

**Derived from the repository's main checkout, never from the selected
directory.** The selected directory can be a subdirectory (`quil/internal/tui`)
or another linked worktree (`quil-worktrees/quil-feat-x`) — deriving from either
would nest worktree roots inside each other and scatter one repository's
worktrees across several parents. `WorktreeListRespPayload.Root` carries that
main-checkout path so the dialog can show the same preview the daemon will use.

A path that already exists is refused rather than reused. `git worktree add`
refuses it anyway; doing it first turns a git error message into a clear one.

**Mapping `/` to `-` is not sufficient to make a branch name a directory name,
and on Windows it is not close.** The two rule sets do not nest:
`git check-ref-format` forbids space, `~ ^ : ? * [ \`, control characters, `..`,
a trailing `.` and a trailing `.lock` — but it permits `< > | "`, and it permits
`con`, `nul`, `aux`, `com1`. Every one of those is a valid branch and an
unusable Windows directory, and the reserved device names are the nastiest of
them: the kernel intercepts `nul` before any filesystem driver sees it, so a
branch called `nul` does not fail to create a directory, it silently resolves
to the device.

`internal/persist/notes.go` already solved exactly this for pane IDs —
`windowsReservedNames` plus a forbidden-character scan, applied
**unconditionally on every platform** so behaviour does not diverge between a
Linux daemon and a Windows one. The derivation reuses that rule set rather than
inventing a second one.

Four more properties the derived segment must have, each because the daemon
**joins and stats the path before git ever validates the name**, and the trust
model says any IPC client can send one:

- Not `.` or `..` after mapping — `.` is untouched by a `/` → `-` rule, so
  `a/../b` is reachable. `config.RecentCWDsPath` sets the precedent: exclude the
  character from the passthrough set so traversal is unreachable by
  construction rather than filtered.
- Not leading `-`, which is argv-shaped — the same hazard `resumeSessionIDRe`
  exists to reject.
- Case-folded uniqueness on Windows and macOS: `Foo` and `foo` are distinct
  branches and the same directory, so the second add collides.
- Bounded length. A long branch under a deep repository can exceed Windows'
  `MAX_PATH`, and the failure surfaces from git rather than from anything that
  names the cause.

**A branch name that cannot become a directory name is REFUSED in the dialog,
never silently mangled.** Mangling would break the correspondence between the
branch and the directory, which is the thing that lets a user recognise which
worktree is which — the identification the feature exists to provide. The
dialog is where the refusal belongs because the user is typing the name at that
moment and can pick another; the daemon re-checks, as the ladder everywhere
else in this codebase does, because the client refuses when it knows and the
daemon guarantees when it could not.

### TUI — the Worktree field

Field order becomes **CWD → kube → toggles → worktree → session → Continue**.

**This does not preserve the session field's invariant, and claiming it did was
wrong.** That field is last because *"it is the only field that expands, so
focusing it cannot shift the fixed-height rows above"* — an argument about what
sits **below** an expander, not above it. Putting the worktree field above the
session field means expanding it shifts the session field and `[Continue]` down,
which is the exact motion the invariant exists to prevent, and `lipgloss.Place`
does not clip, so on a short terminal `[Continue]` goes off screen.

Two expanders in one dialog need a shared row budget: the worktree list gets its
own visible-rows cap, computed against the same height `sessionVisibleRows()`
divides up, so the two together can never exceed the content area. The
alternative — ordering worktree *after* session — is worse, because the session
listing is scoped to the directory the worktree field chooses, and a field must
come after what it depends on.

**The field is always present for a `prompts_cwd` plugin, never conditionally
inserted.** `setupFieldCount` and `setupFieldKind` are currently pure functions
of the plugin. Making the row count depend on whether the browsed directory is a
repository would make it change *while the dialog is open* — the user browses
from a repo to a plain directory and the cursor is left pointing at a field that
no longer exists. Instead the row renders `not a git repository` and is inert
there: fixed geometry, and no clamping logic to get wrong.

States:

| State | Rendering | Meaning |
|---|---|---|
| `off` | `Worktree      off` | Default. Pane spawns in the chosen directory |
| `new branch` | branch text input + derived path preview | Create a worktree on a new branch |
| existing | one row per `MsgWorktreeListResp` entry | Attach to a worktree that already exists |
| not a repo | `not a git repository` (dim, inert) | Chosen directory is outside any repository |

The branch input is prefilled with the configured prefix (`quil/` by default)
with the cursor after it. **An empty branch name means off** — no name is
invented, matching the way `blockedReason` is left empty rather than guessed
when a hook reports no tool.

**Changing the directory resets the field to `off` and discards any typed
branch name.** The field's every value is scoped to one repository: a branch
name is created *in* it, and a listed worktree *belongs* to it. Carrying either
across a directory change would submit a choice made about a repository the user
has navigated away from — the same failure the session picker's commit-time
guard exists to prevent, and reachable the same way, by moving the browser and
pressing Continue without re-focusing the field. Resetting on the CWD change is
sufficient here where re-scoping is right for the session picker, because a
worktree choice has no meaning in another repository, whereas a session listing
does.

The list request fires on **first focus per directory** and retries after a
timeout or failure, mirroring `ensureSessionScan`: only `scanning` and `ready`
count as settled, so a timeout message invites the retry it would otherwise
refuse. Scanning, empty, and failed render distinctly — an in-flight scan
showing "no worktrees" is the same confidently-wrong answer this design keeps
refusing elsewhere.

## Repository shapes the field must handle

Three shapes make the field's normal behaviour wrong rather than merely
unavailable, so each is classified rather than left to fail at `Add` time:

| Shape | Behaviour |
|---|---|
| **Bare repository** | Porcelain's first entry carries `bare` and has no working tree. The sibling-path derivation is meaningless (there is no checkout to be a sibling of) and the entry can never host a pane. Bare-plus-worktrees is a common layout for exactly this audience, so this is a real case: the field offers **attach only**, and the create mode is inert with a reason |
| **Unborn HEAD** (fresh `git init`, no commits) | `git worktree add -b` always fails with `invalid reference: HEAD`. The field must render inert up front rather than accept a branch name and fail seconds later, after the user has typed one |
| **Submodule** | **Refused**, and the reason is this spec's own sibling-location rationale rather than taste: the submodule's checkout lives *inside* the parent repository's working tree, so deriving a sibling from it puts `<sub>-worktrees/` inside that tree — producing exactly the nested second checkout the sibling layout was chosen to avoid, for every tool that walks the parent |

`Main` is **defined as the first porcelain entry**, because the format has no
`main` attribute to read. That is a documented property of the output order, not
an inference, and it is the reason a bare entry is marked rather than dropped:
dropping it would renumber the list and promote the first linked worktree into
being reported as the main checkout.

## Interactions that fall out

### Session picker scoping

`claudesessions.List(cwd)` lists the transcripts recorded for a directory, and
Claude keys a transcript's project directory off the **session's own working
directory** — which is precisely why the recorded transcript path is persisted
rather than derived. That recording lives in **`internal/claudehook`**
(`SessionRecord` / `transcriptFile`, `claudehook.go:193-263`), not in
`internal/claudesessions`, which only reads and titles transcripts it is
pointed at. A worktree-owned pane makes the moved-transcript case the normal
one rather than the exception.

| Worktree choice | Session picker |
|---|---|
| off | unchanged — lists the repository's sessions |
| new branch | **cleared and disabled** — a directory that does not exist yet has no sessions, by construction |
| existing worktree | **re-scoped** to the worktree path, exactly like changing the CWD |

`submitSetupDialog` already drops a chosen session whose `sessionScanCWD` does
not match the directory being submitted. That guard **extends** to compare
against the effective spawn directory rather than being duplicated: it is the
commit-time check, and the commit-time check is the load-bearing one — the user
can pick a session, move the browser, and press Continue without ever
re-focusing the session field.

**"Disabled" is not a state this dialog has, and that is the actual work in
this section.** `onSetupFieldFocused` (`dialog.go:3162`) fires
`ensureSessionScan` whenever the cursor lands on the session field, and no
field today can be disabled *as a whole field* — only rows within one can be.
So with the worktree field set to `new branch`, the cursor still lands on
session and still fires a scan, for a directory that does not exist yet. The
field needs whole-field inert semantics: `moveSetupCursor` either skips it or
lands on it with the scan suppressed and a reason rendered. Skipping is the
weaker option — a field the cursor cannot reach gives the user no explanation
for why their session choice vanished — so it lands, inert, and says why. This
is new machinery, not a configuration of existing machinery, and the plan
should treat it as such.

### Sidebar

No changes required. A worktree-owned pane's CWD resolves to its own
per-checkout git dir, so `gitCache` gives it its own entry, `gitinfo.Probe`
reports `LinkedWorktree: true`, and `gitRow` renders the branch with the ` wt`
marker already.

Second-order cost: more distinct checkouts means more probes per tick. Bounded
by pane count, which is what `referencedDirs` already bounds it to, and each
probe is skipped entirely while HEAD's mtime is unchanged.

### Restore, and the second way a pane silently changes branch

The workspace snapshot persists a pane's CWD, so a worktree-owned pane restores
into its worktree with no new persisted state — which is why `WorktreeSpec` is
a create-time instruction and not a stored pane field.

**But the restore path has the same silent relocation as the create path, and
it is the more likely one to fire.** `spawnRestoredPane`
(`daemon.go:834-843`) stats the saved CWD and, when it is gone, blanks it:

```go
if info, err := os.Stat(pane.CWD); err != nil || !info.IsDir() {
    log.Printf("pane %s: saved cwd %q gone, using default", pane.ID, pane.CWD)
    pane.CWD = ""
}
```

A blanked CWD reaches `d.defaultCWD()`. So: remove a worktree, or restart while
its drive is unmounted, and the agent pane comes back **in the main checkout,
on whatever branch that is** — with only a daemon-log line to say so. For a
claude pane it is worse than a wrong directory: the pane still resumes its
recorded session, so the conversation continues against the wrong tree.

This matters more than the create-time case because worktrees are never
auto-removed by design, which means users will remove them by hand, and a
daemon restart is routine.

The pane must instead come back **visibly broken rather than quietly
relocated** — the pane exists, its CWD is not substituted, and the failure is
on screen rather than in a log. The existing fallback-to-terminal recovery in
the same function is the shape to follow, not to bypass. This is deliberately
stated as a constraint rather than a mechanism: the choice between an error
pane, a restore-checklist failure state, and a prompt belongs in the plan, and
each reuses machinery that already exists.

### Resume

A claude pane in a worktree records its transcript under the worktree's path.
`ReadPersistedSession` already merges the recorded path with the session id and
verifies the path names the id it was recorded with, so restore works — but it
works by way of machinery written before worktrees were common, so it is pinned
with a test rather than assumed.

## Configuration

```toml
[git]
worktree_root   = ""        # empty = <parent>/<repo>-worktrees ; {parent} {repo}
worktree_prefix = "quil/"   # prefilled branch prefix in the dialog
```

Both keys are optional and an empty value keeps the default, so existing
installs need no migration. `worktree_root` is a template rather than a fixed
path because a user with several repositories wants one rule, not one setting
per repository.

## Testing

| Area | Test |
|---|---|
| `gitworktree` | Table tests through the `runGit` seam — `List` porcelain parsing including locked/prunable/detached entries, `Add` argv shape, and that `--force` never appears |
| Daemon handler | `worktreeListResponse` as the pure decode→list→echo split (the `claudeSessionsResponse` pattern), with verbatim echo pinned on the success, error, **and** rejection paths |
| Single-flight | Slot independence exercised through the handler's **own** claim path, not by poking the atomic directly — a test that claims `d.worktreeScanning` itself stays green when the handler is wired to the wrong slot |
| Dialog geometry | Field count and cursor stability across a repo → non-repo → repo directory change |
| Dialog reset | A typed branch name and a selected existing worktree are both discarded when the directory changes — asserted at **submit**, since that is the path a user reaches without re-focusing the field |
| Dialog wiring | Session cleared when `new branch` is chosen; session re-scoped when an existing worktree is chosen; both asserted at **submit**, not only on field focus |
| Daemon failure | A failed `Add` creates no pane and answers the REQUESTER (echoed `Message.ID`), not a broadcast — asserted on pane count as well as the response, so a fix that reports the error *and* spawns cannot pass |
| Placeholder unwind | A failed create clears `pendingSplit[tab.ID]`, asserted by driving a subsequent unrelated pane arrival for that tab and checking it does NOT get adopted into the abandoned placeholder |
| Fallback exemption | A worktree CWD that fails its `os.Stat` fails the create — asserted by pane CWD, so the pre-existing `defaultCWD()` substitution cannot quietly reappear. The same assertion on the `ReplacePaneID` branch, which is a separate early return |
| Restore | A restored pane whose worktree directory is gone does NOT come back in the daemon's default directory. Pinned on `pane.CWD`, since that is the field the silent substitution writes |
| Branch → directory | Table test over names that are valid git refs and invalid Windows directories (`feat/a\|b`, `nul`, `con`, a trailing-dot name) — each refused, none mangled. Runs on every platform, since the rule is platform-independent by decision |
| Resume | Integration test: a claude pane created in a worktree resumes its own session after a daemon restart |

## Out of scope

Deferred deliberately, each with its own reason:

- **Moving an existing pane into a worktree.** Physically a respawn, so it is
  the `restart_pane` path with a new CWD, and for a claude pane it interacts
  with session resume and the recorded transcript path. Depends on everything
  here existing first.
- **Removing worktrees from Quil.** Cleanup is never automatic by decision; an
  explicit removal action is a separate, destructive feature that needs its own
  guards.
- **Inferring which worktree an agent is working in.** There is no signal — the
  agent's process never leaves its spawn directory. Ownership is what makes the
  answer trustworthy, and a guess presented next to a trustworthy answer is
  worse than no answer.
- **Extended sidebar git row** (workmux-style diff stats). Needs
  `git diff --shortstat` and `git status --porcelain`; the latter is the one
  call `internal/gitinfo` deliberately excludes because it can take seconds on a
  large repository without fsmonitor. It needs its own cadence, config gate and
  timeout budget — a separate design.
- **Sidebar width in the Settings dialog, and interactive resize.** `[ui]
  sidebar_width` exists and is honoured, but is reachable only by editing
  `config.toml`. Separate, small, unrelated to worktrees.
- **Two sidebar bugs** — the project badge's `⚠` overpainting its count
  (East-Asian-Ambiguous glyph measured as one cell, painted as two), and the
  absence of any way to clear a stuck blocked mark. Both are bugs, both are
  independent of this design, and neither should wait behind it.
