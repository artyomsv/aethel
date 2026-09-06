# Overlay Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reclaim lazygit overlay panes that are hidden but still running — evict after an idle timeout, and cap how many may live at once.

**Architecture:** The daemon owns the policy and the timer because it owns pane lifetime and keeps running with no TUI attached. Visibility is TUI state, so the TUI reports it through the existing `UpdatePanePayload` partial-update message. Idle eviction sweeps from the existing 1 s `idleChecker`; the cap is enforced at overlay creation.

**Tech Stack:** Go 1.25, `internal/daemon`, `internal/ipc`, `internal/config`, `internal/tui`. No new dependencies.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-08-12-overlay-lifecycle-design.md`.
- **Build/test:** Go is NOT installed on the host. Use `./scripts/dev.sh test <pkg>`, `./scripts/dev.sh test-race <pkg>`, `./scripts/dev.sh vet`. `go test` results are CACHED — a re-run reporting `(cached)` proves nothing after an edit; force a fresh run with a direct `docker run … go test -count=1 ./pkg/...`.
- **Never touch `~/.quil`** (the maintainer's live production daemon). Any test touching config or sockets MUST call `t.Setenv("QUIL_HOME", t.TempDir())` — Docker's throwaway `/root` hides this failure locally.
- **Lock discipline:** `Pane.Overlay`, `Muted`, `Type`, `CWD` are `PluginMu`-protected. Never hold `sm.mu` across `DestroyPane`/`releasePanes` — a parked writer starves every reader.
- **`emitEvent` locks the pane's `PluginMu`** and is non-reentrant: never call it while holding that pane's `PluginMu`.
- **Defaults are 5 and 5.** `0` disables a policy. An absent `[overlay]` section must yield the defaults, never zeros.
- Commit messages: conventional commits, imperative, ≤72 chars, **no AI/agent attribution of any kind**.

---

### Task 1: `[overlay]` config section

**Files:**
- Modify: `internal/config/config.go` (add `OverlayConfig`, field on `Config`, entry in `Default()`)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.OverlayConfig{IdleTimeoutMinutes int, MaxLive int}`, reachable as `cfg.Overlay`. Defaults `IdleTimeoutMinutes: 5`, `MaxLive: 5`.

- [ ] **Step 1: Write the failing test**

```go
func TestDefault_OverlayPolicy(t *testing.T) {
	c := Default()
	if c.Overlay.IdleTimeoutMinutes != 5 {
		t.Errorf("IdleTimeoutMinutes = %d, want 5", c.Overlay.IdleTimeoutMinutes)
	}
	if c.Overlay.MaxLive != 5 {
		t.Errorf("MaxLive = %d, want 5", c.Overlay.MaxLive)
	}
}

// An absent [overlay] section must load the DEFAULTS, not zeros — zero means
// "disabled" for both knobs, so a config written before this feature existed
// would silently opt out of it.
func TestLoad_AbsentOverlaySection_KeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Overlay.IdleTimeoutMinutes != 5 || c.Overlay.MaxLive != 5 {
		t.Errorf("Overlay = %+v, want the defaults (5, 5)", c.Overlay)
	}
}

func TestOverlayConfig_TOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	in := Default()
	in.Overlay.IdleTimeoutMinutes = 11
	in.Overlay.MaxLive = 2
	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Overlay != in.Overlay {
		t.Errorf("round trip = %+v, want %+v", out.Overlay, in.Overlay)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/config`
Expected: FAIL — `c.Overlay undefined`.

- [ ] **Step 3: Implement**

Add to `internal/config/config.go`, beside the other section structs:

```go
// OverlayConfig bounds how long an overlay pane (the Alt+G lazygit pane) may
// stay alive while hidden, and how many may live at once.
//
// An overlay is only HIDDEN when its tab is closed out of, never destroyed, so
// before these bounds existed one lazygit process survived per tab that had
// ever opened one — measured at 116 MB each, and they outlived every TUI
// restart because the daemon keeps them.
type OverlayConfig struct {
	// IdleTimeoutMinutes destroys an overlay hidden for at least this long.
	// 0 disables idle eviction.
	IdleTimeoutMinutes int `toml:"idle_timeout_minutes"`
	// MaxLive caps live overlays across ALL tabs; opening one past the cap
	// evicts the least recently shown. 0 disables the cap.
	MaxLive int `toml:"max_live"`
}
```

Add the field to `Config` (after `Notification`, keeping TOML section order readable):

```go
	Overlay      OverlayConfig      `toml:"overlay"`
```

Add to `Default()`:

```go
		Overlay: OverlayConfig{
			IdleTimeoutMinutes: 5,
			MaxLive:            5,
		},
```

Confirm `Load` starts from `Default()` and decodes over it (that is what makes the absent-section test pass). If it does not, make `Load` begin with `c := Default()` before `toml.DecodeFile`.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/config`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add the [overlay] retention section"
```

---

### Task 2: IPC — visibility field and policy message

**Files:**
- Modify: `internal/ipc/protocol.go`
- Test: `internal/ipc/protocol_test.go`

**Interfaces:**
- Produces: `UpdatePanePayload.OverlayVisible *bool` (json `overlay_visible,omitempty`); `ipc.MsgOverlayPolicy = "overlay_policy"`; `ipc.OverlayPolicyPayload{IdleTimeoutMinutes int, MaxLive int}`.

- [ ] **Step 1: Write the failing test**

```go
// The tri-state matters for the same reason it does for Muted: handleUpdatePane
// is a PARTIAL update handler, so a plain bool would mark an overlay hidden on
// every rename and every OSC 7 CWD change.
func TestUpdatePanePayload_OverlayVisibleOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(ipc.UpdatePanePayload{PaneID: "pane-1", Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "overlay_visible") {
		t.Errorf("nil OverlayVisible must not appear on the wire: %s", b)
	}
}

func TestUpdatePanePayload_OverlayVisibleRoundTripsFalse(t *testing.T) {
	no := false
	b, err := json.Marshal(ipc.UpdatePanePayload{PaneID: "pane-1", OverlayVisible: &no})
	if err != nil {
		t.Fatal(err)
	}
	var out ipc.UpdatePanePayload
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.OverlayVisible == nil || *out.OverlayVisible {
		t.Errorf("OverlayVisible = %v, want an explicit false", out.OverlayVisible)
	}
}

func TestOverlayPolicyPayload_RoundTrip(t *testing.T) {
	msg, err := ipc.NewMessage(ipc.MsgOverlayPolicy, ipc.OverlayPolicyPayload{IdleTimeoutMinutes: 7, MaxLive: 3})
	if err != nil {
		t.Fatal(err)
	}
	var out ipc.OverlayPolicyPayload
	if err := msg.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if out.IdleTimeoutMinutes != 7 || out.MaxLive != 3 {
		t.Errorf("payload = %+v, want {7 3}", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/ipc`
Expected: FAIL — `OverlayVisible` / `MsgOverlayPolicy` undefined.

- [ ] **Step 3: Implement**

In `internal/ipc/protocol.go`, add to `UpdatePanePayload` (after `PinnedAttention`, ~line 374):

```go
	// OverlayVisible reports whether the TUI is currently SHOWING this overlay
	// pane. Pointer for the same tri-state reason as Muted: this is a partial
	// update handler, so a plain bool would report every rename and every OSC 7
	// CWD change as "hidden" and hand the idle sweep a pane the user is looking
	// at. Visibility is client state the daemon cannot observe, and the daemon
	// needs it because an idle lazygit emits nothing whether shown or not.
	OverlayVisible *bool `json:"overlay_visible,omitempty"`
```

Add the message type beside the other `Msg*` constants:

```go
	// MsgOverlayPolicy pushes the client's overlay retention settings. The
	// daemon starts from its own config, but F1 → Settings edits only reach
	// disk on TUI exit, so without this a setting would not apply until the
	// next daemon start. Last writer wins across clients.
	MsgOverlayPolicy = "overlay_policy"
```

And the payload beside the other payload structs:

```go
// OverlayPolicyPayload carries the overlay retention settings. Both fields use
// 0 for "disabled", matching config.OverlayConfig.
type OverlayPolicyPayload struct {
	IdleTimeoutMinutes int `json:"idle_timeout_minutes"`
	MaxLive            int `json:"max_live"`
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/ipc`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -m "feat(ipc): carry overlay visibility and retention policy"
```

---

### Task 3: Daemon — record overlay visibility

**Files:**
- Modify: `internal/daemon/session.go` (two fields on `Pane`)
- Modify: `internal/daemon/daemon.go:2063` (`handleUpdatePane`), `daemon.go:1744` (creation stamps shown)
- Test: `internal/daemon/overlay_lifecycle_test.go` (create)

**Interfaces:**
- Consumes: `ipc.UpdatePanePayload.OverlayVisible` (Task 2).
- Produces: `Pane.OverlayHiddenAt time.Time` (zero = visible), `Pane.OverlayShownAt time.Time`, both `PluginMu`-protected.

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// overlayPane creates a published overlay pane for the lifecycle tests.
func overlayPane(t *testing.T, d *Daemon, tabID string) *Pane {
	t.Helper()
	p, err := d.session.CreatePane(tabID, "")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	p.PluginMu.Lock()
	p.Overlay = true
	p.OverlayShownAt = time.Now()
	p.PluginMu.Unlock()
	return p
}

func TestHandleUpdatePane_OverlayVisibilityStampsHiddenAndShown(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	no := false
	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, OverlayVisible: &no})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden := p.OverlayHiddenAt
	p.PluginMu.Unlock()
	if hidden.IsZero() {
		t.Fatal("hiding an overlay did not stamp OverlayHiddenAt")
	}

	yes := true
	msg, err = ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, OverlayVisible: &yes})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden, shown := p.OverlayHiddenAt, p.OverlayShownAt
	p.PluginMu.Unlock()
	if !hidden.IsZero() {
		t.Error("showing an overlay must clear OverlayHiddenAt")
	}
	if shown.IsZero() {
		t.Error("showing an overlay must stamp OverlayShownAt for the LRU order")
	}
}

// A rename must not be readable as "hidden" — the partial-update tri-state is
// the whole reason OverlayVisible is a pointer.
func TestHandleUpdatePane_RenameLeavesOverlayVisibilityAlone(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{PaneID: p.ID, Name: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	d.handleUpdatePane(msg)

	p.PluginMu.Lock()
	hidden := p.OverlayHiddenAt
	p.PluginMu.Unlock()
	if !hidden.IsZero() {
		t.Error("a rename marked the overlay hidden; the nil pointer must mean 'unchanged'")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `p.OverlayHiddenAt undefined`.

- [ ] **Step 3: Implement**

Add to the `Pane` struct in `internal/daemon/session.go`, beside `Overlay`:

```go
	// OverlayHiddenAt is when the TUI last hid this overlay; zero means it is
	// on screen. OverlayShownAt is when it was last shown, and orders the LRU
	// eviction. Both are PluginMu-protected like Overlay itself, and both are
	// runtime-only — a restored overlay starts hidden, which is correct: no
	// client is showing it yet.
	OverlayHiddenAt time.Time
	OverlayShownAt  time.Time
```

In `handleUpdatePane` (`daemon.go`), after the `PinnedAttention` block and before `d.broadcastState()`:

```go
	if payload.OverlayVisible != nil {
		pane.PluginMu.Lock()
		if *payload.OverlayVisible {
			pane.OverlayHiddenAt = time.Time{}
			pane.OverlayShownAt = time.Now()
		} else if pane.OverlayHiddenAt.IsZero() {
			// Only the FIRST hide stamps: a client re-sending hidden must not
			// keep pushing the eviction deadline out.
			pane.OverlayHiddenAt = time.Now()
		}
		pane.PluginMu.Unlock()
	}
```

In `createPaneAt` (`daemon.go:1744`), inside the existing `if payload.Overlay` block, stamp it shown — an overlay is created because the user just opened it:

```go
		pane.OverlayShownAt = time.Now()
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/session.go internal/daemon/daemon.go internal/daemon/overlay_lifecycle_test.go
git commit -m "feat(daemon): track overlay visibility for retention"
```

---

### Task 4: Daemon — idle eviction sweep

**Files:**
- Create: `internal/daemon/overlay.go`
- Modify: `internal/daemon/daemon.go` (call the sweep from `idleChecker`; store the policy on `Daemon`)
- Test: `internal/daemon/overlay_lifecycle_test.go`

**Interfaces:**
- Consumes: `Pane.OverlayHiddenAt` (Task 3), `config.OverlayConfig` (Task 1).
- Produces: `func (d *Daemon) overlayPolicy() ipc.OverlayPolicyPayload`, `func (d *Daemon) setOverlayPolicy(ipc.OverlayPolicyPayload)`, `func (d *Daemon) sweepIdleOverlays(now time.Time) []string` (returns evicted pane ids, for tests and logging).

- [ ] **Step 1: Write the failing test**

```go
func TestSweepIdleOverlays_DestroysAnOverlayHiddenPastTheTimeout(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now().Add(-6 * time.Minute)
	p.PluginMu.Unlock()

	got := d.sweepIdleOverlays(time.Now())
	if len(got) != 1 || got[0] != p.ID {
		t.Fatalf("evicted %v, want [%s]", got, p.ID)
	}
	if d.session.Pane(p.ID) != nil {
		t.Error("evicted overlay is still in the session")
	}
}

// The case an activity-based implementation gets wrong: lazygit emits nothing
// while you read it, so a VISIBLE overlay looks identical to an idle one.
func TestSweepIdleOverlays_NeverEvictsAVisibleOverlay(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayShownAt = time.Now().Add(-time.Hour) // shown long ago, still shown
	p.OverlayHiddenAt = time.Time{}
	p.PluginMu.Unlock()

	if got := d.sweepIdleOverlays(time.Now()); len(got) != 0 {
		t.Fatalf("evicted %v; a visible overlay must never be evicted", got)
	}
}

func TestSweepIdleOverlays_LeavesNormalPanesAlone(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	p, err := d.session.CreatePane(tab.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.sweepIdleOverlays(time.Now().Add(24 * time.Hour)); len(got) != 0 {
		t.Fatalf("evicted %v; only overlay panes are subject to this policy", got)
	}
	if d.session.Pane(p.ID) == nil {
		t.Error("a normal pane was destroyed by the overlay sweep")
	}
}

func TestSweepIdleOverlays_ZeroTimeoutDisablesEviction(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.IdleTimeoutMinutes = 0
	d := New(cfg)
	tab := d.session.CreateTab("t")
	p := overlayPane(t, d, tab.ID)

	p.PluginMu.Lock()
	p.OverlayHiddenAt = time.Now().Add(-24 * time.Hour)
	p.PluginMu.Unlock()

	if got := d.sweepIdleOverlays(time.Now()); len(got) != 0 {
		t.Fatalf("evicted %v with the timeout disabled", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `d.sweepIdleOverlays undefined`.

- [ ] **Step 3: Implement**

Create `internal/daemon/overlay.go`:

```go
package daemon

import (
	"log"
	"sync"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
)

// overlayPolicyState holds the live retention settings. It is mutable at
// runtime because F1 → Settings pushes changes (MsgOverlayPolicy) and those
// must apply without a daemon restart.
type overlayPolicyState struct {
	mu     sync.RWMutex
	policy ipc.OverlayPolicyPayload
}

func (s *overlayPolicyState) get() ipc.OverlayPolicyPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *overlayPolicyState) set(p ipc.OverlayPolicyPayload) {
	s.mu.Lock()
	s.policy = p
	s.mu.Unlock()
}

func (d *Daemon) overlayPolicy() ipc.OverlayPolicyPayload { return d.overlayPolicyState.get() }

func (d *Daemon) setOverlayPolicy(p ipc.OverlayPolicyPayload) {
	d.overlayPolicyState.set(p)
	log.Printf("overlay policy: idle_timeout=%dm max_live=%d", p.IdleTimeoutMinutes, p.MaxLive)
}

// sweepIdleOverlays destroys every overlay hidden for longer than the policy
// allows, returning the ids it evicted.
//
// A VISIBLE overlay is never evicted however long it idles: lazygit writes
// nothing while the user reads it, so "no output" and "not on screen" are
// indistinguishable from the daemon's side — which is exactly why visibility is
// reported by the client rather than inferred here.
//
// The snapshot-then-destroy shape is the lock discipline this package
// documents: collecting under the read lock and destroying after it means
// DestroyPane (which closes a PTY via releasePanes) never runs under sm.mu.
func (d *Daemon) sweepIdleOverlays(now time.Time) []string {
	timeout := time.Duration(d.overlayPolicy().IdleTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		return nil
	}

	var expired []string
	for _, p := range d.session.Panes() {
		p.PluginMu.Lock()
		isOverlay, hiddenAt := p.Overlay, p.OverlayHiddenAt
		p.PluginMu.Unlock()
		if !isOverlay || hiddenAt.IsZero() {
			continue
		}
		if now.Sub(hiddenAt) >= timeout {
			expired = append(expired, p.ID)
		}
	}

	for _, id := range expired {
		log.Printf("overlay evict: %s idle for %s", id, timeout)
		d.destroyOverlay(id)
	}
	return expired
}

// destroyOverlay removes one overlay through the ordinary pane-destroy path so
// it emits the same broadcast, snapshot and artifact cleanup as any other
// destroy — the TUI then reconciles it with machinery that already exists.
func (d *Daemon) destroyOverlay(paneID string) {
	if err := d.session.DestroyPane(paneID); err != nil {
		log.Printf("overlay evict %s: %v", paneID, err)
		return
	}
	d.cleanupPaneArtifacts(paneID)
	d.broadcastState()
	d.requestSnapshot()
}
```

Add the field to the `Daemon` struct in `daemon.go`:

```go
	overlayPolicyState overlayPolicyState
```

Seed it in `New(...)` from config, beside the other initialisation:

```go
	d.overlayPolicyState.set(ipc.OverlayPolicyPayload{
		IdleTimeoutMinutes: cfg.Overlay.IdleTimeoutMinutes,
		MaxLive:            cfg.Overlay.MaxLive,
	})
```

Call the sweep from `idleChecker`'s existing 1 s tick, next to `checkIdlePanes()`:

```go
			d.sweepIdleOverlays(time.Now())
```

If `d.session.Panes()` does not exist, add it to `SessionManager` mirroring `Tabs()` — RLock, copy the map values into a slice, return. Do not expose the map.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/daemon` then `./scripts/dev.sh test-race internal/daemon`
Expected: PASS both.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/overlay.go internal/daemon/daemon.go internal/daemon/session.go internal/daemon/overlay_lifecycle_test.go
git commit -m "feat(daemon): evict overlays idle past the timeout"
```

---

### Task 5: Daemon — LRU cap at creation

**Files:**
- Modify: `internal/daemon/overlay.go`, `internal/daemon/daemon.go:1744` (`createPaneAt` overlay branch)
- Test: `internal/daemon/overlay_lifecycle_test.go`

**Interfaces:**
- Consumes: `overlayPolicy()`, `destroyOverlay()` (Task 4).
- Produces: `func (d *Daemon) enforceOverlayCap(exclude string) []string`.

- [ ] **Step 1: Write the failing test**

```go
// LRU, not FIFO: the overlay you keep using must survive even when it was
// created first. This test fails against a FIFO implementation.
func TestEnforceOverlayCap_EvictsLeastRecentlyShownNotOldest(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 2
	d := New(cfg)
	tab := d.session.CreateTab("t")

	oldestButActive := overlayPane(t, d, tab.ID)
	stale := overlayPane(t, d, tab.ID)

	// The oldest one was shown a second ago; the newer one has not been looked
	// at in an hour.
	oldestButActive.PluginMu.Lock()
	oldestButActive.OverlayShownAt = time.Now().Add(-time.Second)
	oldestButActive.PluginMu.Unlock()
	stale.PluginMu.Lock()
	stale.OverlayShownAt = time.Now().Add(-time.Hour)
	stale.PluginMu.Unlock()

	got := d.enforceOverlayCap("")
	if len(got) != 1 || got[0] != stale.ID {
		t.Fatalf("evicted %v, want [%s] (the least recently SHOWN)", got, stale.ID)
	}
	if d.session.Pane(oldestButActive.ID) == nil {
		t.Error("the oldest-created overlay was evicted; the policy is LRU, not FIFO")
	}
}

func TestEnforceOverlayCap_ZeroDisablesTheCap(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 0
	d := New(cfg)
	tab := d.session.CreateTab("t")
	for i := 0; i < 8; i++ {
		overlayPane(t, d, tab.ID)
	}
	if got := d.enforceOverlayCap(""); len(got) != 0 {
		t.Fatalf("evicted %v with the cap disabled", got)
	}
}

// The pane being admitted must never evict itself.
func TestEnforceOverlayCap_ExcludesTheNewOverlay(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	cfg := config.Default()
	cfg.Overlay.MaxLive = 1
	d := New(cfg)
	tab := d.session.CreateTab("t")
	fresh := overlayPane(t, d, tab.ID)

	got := d.enforceOverlayCap(fresh.ID)
	for _, id := range got {
		if id == fresh.ID {
			t.Fatal("the overlay being admitted evicted itself")
		}
	}
	if d.session.Pane(fresh.ID) == nil {
		t.Error("the new overlay was destroyed by its own cap check")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `d.enforceOverlayCap undefined`.

- [ ] **Step 3: Implement**

Append to `internal/daemon/overlay.go`:

```go
// enforceOverlayCap evicts least-recently-shown overlays until at most MaxLive
// remain, skipping exclude (the overlay currently being admitted, which must
// never evict itself).
//
// Enforced at CREATION rather than in the idle sweep so opening one past the
// cap is instant and deterministic instead of "eventually". The cap is global
// rather than per tab: it exists to bound total process count.
func (d *Daemon) enforceOverlayCap(exclude string) []string {
	max := d.overlayPolicy().MaxLive
	if max <= 0 {
		return nil
	}

	type entry struct {
		id    string
		shown time.Time
	}
	var live []entry
	for _, p := range d.session.Panes() {
		if p.ID == exclude {
			continue
		}
		p.PluginMu.Lock()
		isOverlay, shown := p.Overlay, p.OverlayShownAt
		p.PluginMu.Unlock()
		if isOverlay {
			live = append(live, entry{id: p.ID, shown: shown})
		}
	}

	// keep = max-1 when admitting one, because the excluded pane occupies a
	// slot the caller is about to fill.
	keep := max
	if exclude != "" {
		keep = max - 1
	}
	if len(live) <= keep {
		return nil
	}

	// Least recently shown first. A zero OverlayShownAt sorts oldest, which is
	// right: it has never been shown at all.
	sort.Slice(live, func(i, j int) bool { return live[i].shown.Before(live[j].shown) })

	var evicted []string
	for i := 0; i < len(live)-keep; i++ {
		log.Printf("overlay evict: %s (cap %d reached, least recently shown)", live[i].id, max)
		d.destroyOverlay(live[i].id)
		evicted = append(evicted, live[i].id)
	}
	return evicted
}
```

Add `"sort"` to the imports. Call it from `createPaneAt`'s overlay branch (`daemon.go:1744`), AFTER the `PluginMu` block that sets `Overlay = true` — the new pane must already be marked an overlay so the exclusion is meaningful:

```go
		d.enforceOverlayCap(pane.ID)
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/daemon` then `./scripts/dev.sh test-race internal/daemon`
Expected: PASS both.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/overlay.go internal/daemon/daemon.go internal/daemon/overlay_lifecycle_test.go
git commit -m "feat(daemon): cap live overlays, evicting least recently shown"
```

---

### Task 6: Daemon — no client attached means hidden

**Files:**
- Modify: `internal/daemon/overlay.go`, `internal/daemon/daemon.go:250-253` (the `onDisconnect` callback passed to `ipc.NewServer`)
- Test: `internal/daemon/overlay_lifecycle_test.go`

**Interfaces:**
- Produces: `func (d *Daemon) markOverlaysHidden(now time.Time) int` (count stamped, for the test).

- [ ] **Step 1: Write the failing test**

```go
// Nothing can be displaying an overlay when no client is attached. Without this
// an overlay hidden by a TUI that then exited would keep a zero
// OverlayHiddenAt forever and never become eligible — the case where reclaiming
// matters most, since the user is away.
func TestMarkOverlaysHidden_StampsOverlaysThatLackATimestamp(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	tab := d.session.CreateTab("t")
	visible := overlayPane(t, d, tab.ID)
	already := overlayPane(t, d, tab.ID)

	earlier := time.Now().Add(-time.Hour)
	already.PluginMu.Lock()
	already.OverlayHiddenAt = earlier
	already.PluginMu.Unlock()

	if n := d.markOverlaysHidden(time.Now()); n != 1 {
		t.Errorf("stamped %d overlays, want 1", n)
	}

	visible.PluginMu.Lock()
	got := visible.OverlayHiddenAt
	visible.PluginMu.Unlock()
	if got.IsZero() {
		t.Error("a visible overlay was not marked hidden on last disconnect")
	}

	already.PluginMu.Lock()
	got = already.OverlayHiddenAt
	already.PluginMu.Unlock()
	if !got.Equal(earlier) {
		t.Error("an already-hidden overlay had its deadline pushed out")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — `d.markOverlaysHidden undefined`.

- [ ] **Step 3: Implement**

Append to `internal/daemon/overlay.go`:

```go
// markOverlaysHidden stamps every overlay that has no hidden timestamp,
// returning how many it stamped.
//
// Called when the LAST client disconnects: nothing can be showing an overlay
// with nothing attached, and this is what makes the idle timer fire while the
// user is away — which is the case the whole feature exists for, since a TUI
// exit used to leave every overlay running indefinitely.
//
// An overlay that is ALREADY hidden keeps its original timestamp; re-stamping
// would push the deadline out on every disconnect.
func (d *Daemon) markOverlaysHidden(now time.Time) int {
	var n int
	for _, p := range d.session.Panes() {
		p.PluginMu.Lock()
		if p.Overlay && p.OverlayHiddenAt.IsZero() {
			p.OverlayHiddenAt = now
			n++
		}
		p.PluginMu.Unlock()
	}
	return n
}
```

In `daemon.go`, extend the existing `onDisconnect` callback (currently `requestSnapshot` + `RemoveWatchersByConn`):

```go
	d.server = ipc.NewServer(sockPath, d.handleMessage, func(conn *ipc.Conn) {
		d.requestSnapshot()
		d.events.RemoveWatchersByConn(conn)
		// The disconnecting conn is removed from the server's list BEFORE this
		// callback runs (handleConn's defer calls removeConn first), so a zero
		// count here means this was the last client.
		if d.server != nil && d.server.ConnCount() == 0 {
			if n := d.markOverlaysHidden(time.Now()); n > 0 {
				log.Printf("overlay: %d marked hidden (no clients attached)", n)
			}
		}
	})
```

Verify the ordering claim against `ipc.Server.handleConn`'s defer before relying on it; if `onDisconnect` runs before `removeConn`, compare against `1` instead and say so in the comment.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/overlay.go internal/daemon/daemon.go internal/daemon/overlay_lifecycle_test.go
git commit -m "feat(daemon): treat a detached session as hidden overlays"
```

---

### Task 7: Daemon — accept a pushed policy

**Files:**
- Modify: `internal/daemon/daemon.go` (dispatch arm beside `case ipc.MsgUpdatePane:` at line 1027)
- Test: `internal/daemon/overlay_lifecycle_test.go`

**Interfaces:**
- Consumes: `ipc.MsgOverlayPolicy`, `ipc.OverlayPolicyPayload` (Task 2), `setOverlayPolicy` (Task 4).

- [ ] **Step 1: Write the failing test**

```go
func TestHandleMessage_OverlayPolicyAppliesImmediately(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())

	msg, err := ipc.NewMessage(ipc.MsgOverlayPolicy, ipc.OverlayPolicyPayload{IdleTimeoutMinutes: 1, MaxLive: 2})
	if err != nil {
		t.Fatal(err)
	}
	d.handleMessage(nil, msg)

	if got := d.overlayPolicy(); got.IdleTimeoutMinutes != 1 || got.MaxLive != 2 {
		t.Fatalf("policy = %+v, want {1 2}", got)
	}
}

// A malformed frame must not silently zero the policy — zero means "disabled"
// for both knobs, so a decode failure would turn the feature off.
func TestHandleMessage_MalformedOverlayPolicyIsIgnored(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	d := New(config.Default())
	before := d.overlayPolicy()

	msg := &ipc.Message{Type: ipc.MsgOverlayPolicy, Payload: []byte(`"not an object"`)}
	d.handleMessage(nil, msg)

	if got := d.overlayPolicy(); got != before {
		t.Fatalf("policy = %+v, want it unchanged at %+v", got, before)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/daemon`
Expected: FAIL — policy unchanged / no such message case.

- [ ] **Step 3: Implement**

Add the dispatch arm in `handleMessage`, beside `case ipc.MsgUpdatePane:`:

```go
	case ipc.MsgOverlayPolicy:
		var p ipc.OverlayPolicyPayload
		// Checked, not best-effort: both fields use 0 for "disabled", so a
		// malformed frame decoded into a zero struct would silently turn off
		// both retention policies.
		if err := msg.DecodePayload(&p); err != nil {
			log.Printf("overlay policy: malformed payload: %v", err)
			return
		}
		d.setOverlayPolicy(p)
```

If `handleMessage` dereferences `conn` before this arm, pass a real conn in the test instead of `nil`, or move the arm above any such use.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/daemon`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/overlay_lifecycle_test.go
git commit -m "feat(daemon): accept a pushed overlay retention policy"
```

---

### Task 8: TUI — report overlay visibility

**Files:**
- Modify: `internal/tui/overlay.go` (`showOverlay` ~line 124, the hide path ~line 39 and ~line 159), `internal/tui/model.go:4721,4737`
- Test: `internal/tui/overlay_test.go`

**Interfaces:**
- Consumes: `ipc.UpdatePanePayload.OverlayVisible` (Task 2).
- Produces: `func (m *Model) overlayVisibilityCmd(tab *TabModel, visible bool) tea.Cmd`.

- [ ] **Step 1: Write the failing test**

```go
// The daemon cannot see overlayVisible, so a hide that does not report itself
// leaves the overlay alive forever. Assert on what reaches the wire, not on the
// local flag — the flag flipping is not the behaviour that matters.
func TestOverlayVisibility_HideReportsFalseToTheDaemon(t *testing.T) {
	conn := newFakeConn()
	m := Model{client: conn}
	tab := &TabModel{overlayPane: &PaneModel{ID: "pane-ov"}, overlayVisible: true}

	cmd := m.overlayVisibilityCmd(tab, false)
	if cmd == nil {
		t.Fatal("no command produced for a hide")
	}
	cmd()

	var got *ipc.UpdatePanePayload
	for _, sent := range conn.sent {
		if sent.Type == ipc.MsgUpdatePane {
			var p ipc.UpdatePanePayload
			if err := sent.DecodePayload(&p); err == nil && p.PaneID == "pane-ov" {
				got = &p
			}
		}
	}
	if got == nil {
		t.Fatal("no update_pane sent for the overlay")
	}
	if got.OverlayVisible == nil || *got.OverlayVisible {
		t.Errorf("OverlayVisible = %v, want explicit false", got.OverlayVisible)
	}
}

func TestOverlayVisibility_ShowReportsTrue(t *testing.T) {
	conn := newFakeConn()
	m := Model{client: conn}
	tab := &TabModel{overlayPane: &PaneModel{ID: "pane-ov"}}

	if cmd := m.overlayVisibilityCmd(tab, true); cmd != nil {
		cmd()
	}
	for _, sent := range conn.sent {
		if sent.Type != ipc.MsgUpdatePane {
			continue
		}
		var p ipc.UpdatePanePayload
		if err := sent.DecodePayload(&p); err == nil && p.PaneID == "pane-ov" {
			if p.OverlayVisible == nil || !*p.OverlayVisible {
				t.Errorf("OverlayVisible = %v, want explicit true", p.OverlayVisible)
			}
			return
		}
	}
	t.Fatal("no update_pane sent for the overlay")
}

func TestOverlayVisibility_NoOverlayProducesNoCommand(t *testing.T) {
	m := Model{client: newFakeConn()}
	if cmd := m.overlayVisibilityCmd(&TabModel{}, false); cmd != nil {
		t.Error("a tab with no overlay must not send anything")
	}
}
```

`newFakeConn`/`fakeConn` already exist in `internal/tui/router_test.go`. Check its recorded-message field name and use it verbatim rather than the `sent` used above if it differs.

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — `m.overlayVisibilityCmd undefined`.

- [ ] **Step 3: Implement**

Add to `internal/tui/overlay.go`:

```go
// overlayVisibilityCmd tells the daemon whether this tab's overlay is on
// screen, so its idle timer measures HIDDEN time rather than quiet time.
//
// Sent for the TAB's destination, like every other overlay message: Alt+G is
// reachable from a background project's tab.
func (m *Model) overlayVisibilityCmd(tab *TabModel, visible bool) tea.Cmd {
	if tab == nil || tab.overlayPane == nil {
		return nil
	}
	paneID, dest := tab.overlayPane.ID, tab.Dest
	v := visible
	return func() tea.Msg {
		msg, err := ipc.NewMessage(ipc.MsgUpdatePane, ipc.UpdatePanePayload{
			PaneID:         paneID,
			OverlayVisible: &v,
		})
		if err != nil {
			log.Printf("overlay: visibility encode: %v", err)
			return nil
		}
		if err := m.sendForDest(dest, msg); err != nil {
			log.Printf("overlay: visibility send: %v", err)
		}
		return nil
	}
}
```

Then batch it into every site that flips `overlayVisible`:

- `overlay.go:125` (`showOverlay` sets `true`) — batch `m.overlayVisibilityCmd(tab, true)` into its returned command.
- `overlay.go:39` and `overlay.go:159` (hide paths) — batch `m.overlayVisibilityCmd(tab, false)`.
- `model.go:4721` (`= false`) and `model.go:4737` (`= true`) — same treatment.

Where a site currently returns no command, return the visibility command; where it returns one, wrap both in `tea.Batch`. Do not change any other behaviour.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/tui`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/overlay.go internal/tui/model.go internal/tui/overlay_test.go
git commit -m "feat(tui): report overlay visibility to the daemon"
```

---

### Task 9: TUI — Settings rows and policy push

**Files:**
- Modify: `internal/tui/dialog.go` (`settingsFields`, ~line 232-294), `internal/tui/model.go` (push after attach)
- Test: `internal/tui/overlay_settings_test.go` (create)

**Interfaces:**
- Consumes: `config.OverlayConfig` (Task 1), `ipc.MsgOverlayPolicy` (Task 2).
- Produces: `func (m *Model) overlayPolicyCmd() tea.Cmd`.

- [ ] **Step 1: Write the failing test**

```go
func TestSettings_OverlayRowsEditAndFlagConfigChanged(t *testing.T) {
	m := &Model{cfg: config.Default(), client: newFakeConn()}

	idle := settingsFieldByLabel(t, "Overlay idle timeout (min)")
	idle.set(m, "9")
	if m.cfg.Overlay.IdleTimeoutMinutes != 9 {
		t.Errorf("IdleTimeoutMinutes = %d, want 9", m.cfg.Overlay.IdleTimeoutMinutes)
	}
	if !m.configChanged {
		t.Error("editing the row did not flag configChanged; the edit would be lost on exit")
	}

	m.configChanged = false
	capRow := settingsFieldByLabel(t, "Max live overlays")
	capRow.set(m, "3")
	if m.cfg.Overlay.MaxLive != 3 {
		t.Errorf("MaxLive = %d, want 3", m.cfg.Overlay.MaxLive)
	}
	if !m.configChanged {
		t.Error("editing the cap row did not flag configChanged")
	}
}

// Negative and non-numeric input must be refused, not stored: a negative
// timeout would make every overlay instantly expired.
func TestSettings_OverlayRowsRefuseInvalidInput(t *testing.T) {
	m := &Model{cfg: config.Default(), client: newFakeConn()}
	idle := settingsFieldByLabel(t, "Overlay idle timeout (min)")
	for _, bad := range []string{"-1", "abc", ""} {
		idle.set(m, bad)
		if m.cfg.Overlay.IdleTimeoutMinutes != 5 {
			t.Fatalf("input %q stored %d; want the default 5 retained", bad, m.cfg.Overlay.IdleTimeoutMinutes)
		}
	}
}

func TestOverlayPolicyCmd_SendsTheCurrentSettings(t *testing.T) {
	conn := newFakeConn()
	m := &Model{cfg: config.Default(), client: conn}
	m.cfg.Overlay.IdleTimeoutMinutes = 4
	m.cfg.Overlay.MaxLive = 2

	cmd := m.overlayPolicyCmd()
	if cmd == nil {
		t.Fatal("no policy command produced")
	}
	cmd()

	for _, sent := range conn.sent {
		if sent.Type != ipc.MsgOverlayPolicy {
			continue
		}
		var p ipc.OverlayPolicyPayload
		if err := sent.DecodePayload(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p.IdleTimeoutMinutes != 4 || p.MaxLive != 2 {
			t.Errorf("payload = %+v, want {4 2}", p)
		}
		return
	}
	t.Fatal("no overlay_policy message sent")
}
```

Add the helper in the same file:

```go
func settingsFieldByLabel(t *testing.T, label string) settingsField {
	t.Helper()
	for _, f := range settingsFields {
		if f.label == label {
			return f
		}
	}
	t.Fatalf("no settings row labelled %q", label)
	return settingsField{}
}
```

Check the real type name of the `settingsFields` element in `dialog.go` and use it; the literal above assumes `settingsField`.

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test internal/tui`
Expected: FAIL — no such rows.

- [ ] **Step 3: Implement**

Add two rows to `settingsFields` in `dialog.go`, following the `Page scroll lines` shape (validate, compare, assign, flag):

```go
		{
			label: "Overlay idle timeout (min)",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.Overlay.IdleTimeoutMinutes) },
			set: func(m *Model, v string) {
				// Refused rather than clamped, like Sidebar width: a stored
				// value the daemon would not honour must never be displayed.
				// 0 is legal and means "never evict".
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n == m.cfg.Overlay.IdleTimeoutMinutes {
					return
				}
				m.cfg.Overlay.IdleTimeoutMinutes = n
				m.configChanged = true
			},
		},
		{
			label: "Max live overlays",
			get:   func(m *Model) string { return strconv.Itoa(m.cfg.Overlay.MaxLive) },
			set: func(m *Model, v string) {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || n == m.cfg.Overlay.MaxLive {
					return
				}
				m.cfg.Overlay.MaxLive = n
				m.configChanged = true
			},
		},
```

Add the push command to `internal/tui/overlay.go`:

```go
// overlayPolicyCmd pushes the client's overlay retention settings to every
// daemon.
//
// Needed because Settings edits only flip configChanged and reach disk when the
// TUI exits, so a daemon reading the file would not see a change until its next
// start. Sent once after attach and again on every edit.
func (m *Model) overlayPolicyCmd() tea.Cmd {
	p := ipc.OverlayPolicyPayload{
		IdleTimeoutMinutes: m.cfg.Overlay.IdleTimeoutMinutes,
		MaxLive:            m.cfg.Overlay.MaxLive,
	}
	dests := m.knownDests()
	return func() tea.Msg {
		for _, dest := range dests {
			msg, err := ipc.NewMessage(ipc.MsgOverlayPolicy, p)
			if err != nil {
				log.Printf("overlay policy: encode: %v", err)
				return nil
			}
			if err := m.sendForDest(dest, msg); err != nil {
				log.Printf("overlay policy: send to %q: %v", dest, err)
			}
		}
		return nil
	}
}
```

Build a fresh `ipc.Message` per destination — `sendForDest` stamps `Origin`, so one shared message would be re-stamped mid-flight.

Wire two call sites:
1. In `handleSettingsKey`, after the `set` call that already triggers the relayout sequence, batch `m.overlayPolicyCmd()` when the edited row is one of the two overlay rows (simplest correct approach: send it after ANY settings commit — it is two ints).
2. Where `attachAllDests` completes (the same place other post-attach requests are issued, e.g. `requestPluginListFor`), batch `m.overlayPolicyCmd()`.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test internal/tui` then `./scripts/dev.sh test-race internal/tui`
Expected: PASS both.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dialog.go internal/tui/overlay.go internal/tui/model.go internal/tui/overlay_settings_test.go
git commit -m "feat(tui): expose overlay retention in settings and push it"
```

---

### Task 10: Documentation and changelog

**Files:**
- Modify: `.claude/rules/plugins.md` (the overlay section), `.claude/rules/daemon-lifecycle.md` (pane lifecycle), `docs/configuration.md`, `CHANGELOG.md`

- [ ] **Step 1: Update the scoped rules**

In `.claude/rules/plugins.md`, under the overlay material, record: the overlay is hidden rather than destroyed on Alt+G; the daemon now bounds that with an idle timeout and a global LRU cap; visibility is reported by the TUI through `UpdatePanePayload.OverlayVisible` **because an idle lazygit emits nothing whether shown or not**, so activity cannot stand in for visibility; a detached session counts as hidden; the cap is enforced at creation and evicts least-recently-**shown**, not oldest-created.

In `.claude/rules/daemon-lifecycle.md`, add one line to the idle/events section noting `idleChecker` now also runs `sweepIdleOverlays`.

- [ ] **Step 2: Document the config**

In `docs/configuration.md`, add the `[overlay]` section with both keys, their defaults (5 and 5), and that `0` disables each.

- [ ] **Step 3: Changelog**

Add under `## [Unreleased]` → `### Fixed`, written from the user's point of view (the release workflow copies this verbatim):

```markdown
- **Closing the lazygit overlay now reclaims it.** `Alt+G` only hid the overlay
  — the lazygit process kept running for the life of the tab, so a session that
  had opened it in several tabs carried one live process per tab indefinitely
  (measured at ~116 MB each). Hidden overlays are now closed after five minutes,
  and at most five are kept alive at once, the least recently shown being
  dropped first. Both limits are configurable in F1 → Settings and under
  `[overlay]` in `config.toml`; `0` turns either off.
```

- [ ] **Step 4: Verify the docs gate**

Run: `./scripts/dev.sh build`
Expected: passes (it runs `docs-size` first and refuses if a rule file is over its limit).

- [ ] **Step 5: Commit**

```bash
git add .claude/rules/plugins.md .claude/rules/daemon-lifecycle.md docs/configuration.md CHANGELOG.md
git commit -m "docs: record overlay retention limits"
```

---

## Final verification

- [ ] `./scripts/dev.sh vet`
- [ ] Full uncached suite: `docker run --rm -v "<worktree>":/src -v quil-gomod:/go/pkg/mod -v quil-gocache:/root/.cache/go-build -w //src golang:1.25-alpine go test -count=1 ./...`
- [ ] `./scripts/dev.sh test-race internal/daemon internal/tui internal/ipc`
- [ ] `./scripts/dev.sh build`
- [ ] Mutation-check the two policy tests: invert the LRU comparison in `enforceOverlayCap` and confirm `TestEnforceOverlayCap_EvictsLeastRecentlyShownNotOldest` fails; make `sweepIdleOverlays` ignore `OverlayHiddenAt.IsZero()` and confirm `TestSweepIdleOverlays_NeverEvictsAVisibleOverlay` fails. Restore both and confirm `git diff` is empty.
