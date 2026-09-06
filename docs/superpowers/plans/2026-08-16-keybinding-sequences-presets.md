# Key Sequences and Binding Presets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `Ctrl+B` then `c` open a tab, and let a user select a whole keymap (`default`, `tmux`) from `~/.quil/bindings.toml` instead of editing 42 individual strings in `config.toml`.

**Architecture:** Stage 2 adds two maps to the existing `Keymap` (full sequences, and every proper prefix of one) plus a small pending-chord state machine in `handleKey` that sits before the early-tier lookup and is tier-agnostic. Stage 3 layers three binding sources (embedded `default` → selected preset → user overrides) through a pure merge function, reads them from a new `bindings.toml`, and migrates existing `[keybindings]` tables by diffing against shipped defaults.

**Tech Stack:** Go 1.25, `charm.land/bubbletea/v2` (key strings), `BurntSushi/toml`, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-08-16-keybinding-sequences-presets-design.md`

## Global Constraints

- Go files use **tabs**; TOML/JSON/YAML use 2 spaces. `gofmt` is mandatory.
- `internal/keymap` imports **stdlib + `BurntSushi/toml` only**. No `internal/config`, no `internal/tui`, and no knowledge of where files live — that keeps the whole package testable without `QUIL_HOME` and without building a `Model`.
- Build and test via Docker: `./scripts/dev.sh test ./internal/keymap/`. Go is **not** installed on the host.
- **`dev.sh test` consumes only its first argument.** `-run` and `-v` are silently dropped, and a second package argument is silently ignored — a three-package run tests one and greps clean. Every command in this plan is therefore a single package path.
- **Never** run `./quil` without `--dev`; never touch `~/.quil/`. See `.claude/rules/dev-environment.md`.
- Exported identifiers use `MixedCaps`; acronyms stay uppercase (`ID`, `TUI`).
- Commit messages: imperative mood, ≤72 chars first line, **no AI attribution** — no `Co-Authored-By`, no model or vendor names, no "generated with" notes.
- **Do not edit `CHANGELOG.md`.** Master replaced hand-editing with per-PR fragments in `changelog.d/` (`#163`). Task 20 writes the fragment.
- `gofmt -l` inside the Linux container flags **every** file, because the working tree is CRLF. Strip `\r` before checking, and only format files you actually touched.

## Branch

This work goes on a **fresh branch off `origin/master`**. The old `feat/keybinding-registry` branch was squash-merged as `0486578` and carries nothing unique — do not build on it, and do not `rebase --onto` it.

```bash
git fetch origin
git switch -c feat/keymap-sequences origin/master
```

## File Structure

**Created:**

| File | Responsibility |
|---|---|
| `internal/keymap/sequence.go` | `MatchKind`, `MatchSeq`, sequence + prefix table construction |
| `internal/keymap/sequence_test.go` | Sequence matching, shadowing, prefix set |
| `internal/keymap/resolve.go` | `Resolve(layers...)` — pure layer merge |
| `internal/keymap/resolve_test.go` | Merge semantics: inherit / replace / unbind |
| `internal/keymap/preset.go` | `//go:embed presets/*.toml`, `Preset(name)`, `PresetNames()` |
| `internal/keymap/preset_test.go` | Preset parity with defaults, canonical chords, no conflicts |
| `internal/keymap/presets/default.toml` | Shipped default keymap |
| `internal/keymap/presets/tmux.toml` | tmux preset |
| `internal/keymap/prefix.go` | `${prefix}` expansion + validation |
| `internal/keymap/prefix_test.go` | Expansion, and each rejection reason |
| `internal/config/bindings.go` | `bindings.toml` read/write, migration, path derivation |
| `internal/config/bindings_test.go` | Round-trip, migration diff, `O_EXCL`, `Load`-error abort |
| `internal/tui/sequence_test.go` | Sequences driven through `Update` |
| `changelog.d/added-keymap-sequences-presets.md` | Release-notes fragment |
| `techdebt/3-1-strip-legacy-keybindings-table.md` | The release N+1 follow-up |

**Modified:** `internal/keymap/{keymap,conflict,action}.go`, `internal/tui/{model,keyspecs,keymatch}.go`, `internal/config/config.go`, `cmd/quil/main.go`, `docs/keybindings.md`, `docs/configuration.md`, `.claude/rules/tui-rendering.md`.

---

# Stage 2 — the prefix sequence machine

### Task 1: Sequence and prefix tables

**Files:**
- Create: `internal/keymap/sequence.go`, `internal/keymap/sequence_test.go`
- Modify: `internal/keymap/keymap.go` (the `Keymap` struct and `Build`'s insertion loop)

**Interfaces:**
- Consumes: existing `Sequence`, `Chord`, `ParseSpec`, `Action.Order`.
- Produces: `type MatchKind uint8` with `MatchNone`/`MatchPartial`/`MatchExact`; `func (k *Keymap) MatchSeq(pending []Chord) (ActionID, MatchKind)`; two new unexported `Keymap` fields `seqs map[string]ActionID` and `partial map[string]ActionID`.

`Build` currently refuses any sequence longer than one chord and reports `ConflictUnsupportedSequence` (`keymap.go:66-76`). This task makes it store them instead.

`partial` maps every **proper** prefix of every sequence to an owning action. It is a map rather than a `map[string]bool` set so a shadowing conflict can name the sequence that won.

- [ ] **Step 1: Write the failing test**

Create `internal/keymap/sequence_test.go`:

```go
package keymap

import "testing"

func mustChords(t *testing.T, spec string) []Chord {
	t.Helper()
	seqs, err := ParseSpec(spec)
	if err != nil {
		t.Fatalf("ParseSpec(%q): %v", spec, err)
	}
	if len(seqs) != 1 {
		t.Fatalf("ParseSpec(%q) returned %d alternatives, want 1", spec, len(seqs))
	}
	return seqs[0]
}

func TestMatchSeq_PartialThenExact(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})

	if id, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("MatchSeq(ctrl+b) = (%q, %v), want MatchPartial", id, kind)
	}
	id, kind := km.MatchSeq(mustChords(t, "ctrl+b c"))
	if kind != MatchExact || id != "tab.new" {
		t.Errorf("MatchSeq(ctrl+b c) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b z")); kind != MatchNone {
		t.Errorf("MatchSeq(ctrl+b z) = %v, want MatchNone", kind)
	}
}

// A single-chord binding must never report Partial. This is the property that
// makes Stage 2 safe to merge: if it ever fails, every existing binding starts
// swallowing its own keypress instead of firing.
func TestMatchSeq_SingleChordIsNeverPartial(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+t"})
	if id, kind := km.MatchSeq(mustChords(t, "ctrl+t")); kind != MatchExact || id != "tab.new" {
		t.Fatalf("MatchSeq(ctrl+t) = (%q, %v), want (tab.new, MatchExact)", id, kind)
	}
}

// Tier-agnostic: pane.close is TierLate, and its opening chord must still be
// reported as Partial or the sequence can never complete.
func TestMatchSeq_IsTierAgnostic(t *testing.T) {
	if a, _ := Lookup("pane.close"); a.Tier != TierLate {
		t.Fatalf("fixture assumes pane.close is TierLate, got %v", a.Tier)
	}
	km, _ := Build(map[ActionID]string{"pane.close": "ctrl+b x"})
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("a late-tier sequence head must report MatchPartial, got %v", kind)
	}
}

func TestMatchSeq_EmptyPendingIsNone(t *testing.T) {
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})
	if _, kind := km.MatchSeq(nil); kind != MatchNone {
		t.Errorf("MatchSeq(nil) = %v, want MatchNone", kind)
	}
}

func TestMatchSeq_NilKeymapIsNone(t *testing.T) {
	var km *Keymap
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchNone {
		t.Errorf("nil Keymap must answer MatchNone, got %v", kind)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: MatchKind`, `undefined: MatchPartial`, `km.MatchSeq undefined`.

- [ ] **Step 3: Create the sequence file**

Create `internal/keymap/sequence.go`:

```go
package keymap

// MatchKind is the result of probing a pending chord sequence.
type MatchKind uint8

const (
	// MatchNone: the chords match no binding, and extend none.
	MatchNone MatchKind = iota
	// MatchPartial: the chords are a proper prefix of at least one binding.
	// The caller swallows the key and keeps the sequence pending.
	MatchPartial
	// MatchExact: the chords are a complete binding. The caller runs it.
	MatchExact
)

// MatchSeq resolves a pending chord sequence.
//
// Deliberately TIER-AGNOSTIC, and that is the whole subtlety of the feature.
// pane.close is a late-tier action; bind it to "ctrl+b x" and the opening chord
// is in neither tier's chord map. A tier-scoped probe answers MatchNone, the
// key falls through to tryPluginRawKey or to the PTY, and no amount of pressing
// x can ever complete the sequence. The tier split governs Exact resolution of
// SINGLE CHORDS only; partial detection is global.
//
// Nil-safe: TUI tests build Model literals with no keymap and drive handleKey.
func (k *Keymap) MatchSeq(pending []Chord) (ActionID, MatchKind) {
	if k == nil || len(pending) == 0 {
		return "", MatchNone
	}
	key := Sequence(pending).String()
	if id, ok := k.seqs[key]; ok {
		return id, MatchExact
	}
	// A single chord is resolved by the tier lookups, not here — but MatchSeq
	// is also the sequence machine's only view of the keymap, so it must report
	// a length-1 binding as Exact when asked. Both tiers are consulted because
	// the machine runs before the tier split.
	if len(pending) == 1 {
		for _, tier := range []Tier{TierEarly, TierLate} {
			if id, ok := k.chords[tier][key]; ok {
				return id, MatchExact
			}
		}
	}
	if id, ok := k.partial[key]; ok {
		return id, MatchPartial
	}
	return "", MatchNone
}
```

- [ ] **Step 4: Store sequences in Build**

In `internal/keymap/keymap.go`, add the two fields to the struct:

```go
type Keymap struct {
	// chords maps tier -> canonical chord -> action. Only single-chord
	// bindings land here; multi-step sequences live in seqs and are matched
	// by the prefix machine before either tier lookup runs.
	chords   map[Tier]map[string]ActionID
	bindings map[ActionID][]Sequence
	// seqs maps a full canonical sequence ("ctrl+b c") to its action.
	seqs map[string]ActionID
	// partial maps every PROPER prefix of every sequence to one owning action.
	// A map rather than a set so a shadowing conflict can name the winner.
	partial map[string]ActionID
}
```

Initialise them in `Build`:

```go
	km := &Keymap{
		chords:   map[Tier]map[string]ActionID{TierEarly: {}, TierLate: {}},
		bindings: make(map[ActionID][]Sequence, len(specs)),
		seqs:     map[string]ActionID{},
		partial:  map[string]ActionID{},
	}
```

Replace the `if len(seq) != 1 { ... }` block (currently `keymap.go:66-76`) with:

```go
		for _, seq := range seqs {
			if len(seq) > 1 {
				full := seq.String()
				if prev, taken := km.seqs[full]; taken {
					conflicts = append(conflicts, Conflict{
						Kind: ConflictDuplicate, Key: full, Winner: prev, Loser: a.ID,
					})
					continue // lower Order already claimed it: legacy case order
				}
				km.seqs[full] = a.ID
				// Record every PROPER prefix. First writer wins so the recorded
				// owner follows registry Order, like every other tie-break here.
				for i := 1; i < len(seq); i++ {
					head := seq[:i].String()
					if _, seen := km.partial[head]; !seen {
						km.partial[head] = a.ID
					}
				}
				continue
			}
			key := seq[0].String()
			// ... existing single-chord logic unchanged ...
		}
```

- [ ] **Step 5: Run the test and verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS. `TestActions_RegistryIntegrity` and the Stage 1 suite must still pass — no shipped default is multi-step, so none of them change.

- [ ] **Step 6: Commit**

```bash
git add internal/keymap/sequence.go internal/keymap/sequence_test.go internal/keymap/keymap.go
git commit -m "feat(keymap): store and match multi-chord sequences"
```

---

### Task 2: Shadowing conflicts replace the unsupported-sequence conflict

**Files:**
- Modify: `internal/keymap/conflict.go`, `internal/keymap/keymap.go` (`detectShadowing`)
- Test: `internal/keymap/conflict_test.go`

**Interfaces:**
- Consumes: `Keymap.seqs`, `Keymap.partial`, `Keymap.chords` from Task 1.
- Produces: `ConflictShadowed` constant; `ConflictUnsupportedSequence` **removed**.

`ConflictUnsupportedSequence` existed only to keep `Build` honest while sequences parsed but did not dispatch. Now they dispatch, so it is a lie — leaving it makes F1 warn about working bindings.

`ConflictShadowed` replaces it: a chord or sequence that is a proper prefix of a longer sequence can never fire without a timeout. **Intra-layer rule: the shorter is refused**, and refused means removed from the lookup tables, not merely warned about.

- [ ] **Step 1: Write the failing test**

Append to `internal/keymap/conflict_test.go`:

```go
// A chord that opens a longer sequence can never fire — pressing it always
// arms the machine instead. The shorter binding is refused, and refused means
// gone from the tables, not just warned about.
func TestBuild_ShorterBindingIsRefusedWhenItShadows(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{
		"pane.rename": "ctrl+b",
		"tab.new":     "ctrl+b c",
	})

	var got *Conflict
	for i := range conflicts {
		if conflicts[i].Kind == ConflictShadowed {
			got = &conflicts[i]
		}
	}
	if got == nil {
		t.Fatalf("want a ConflictShadowed, got %v", conflicts)
	}
	if got.Loser != "pane.rename" || got.Winner != "tab.new" {
		t.Errorf("Conflict = winner %q loser %q, want winner tab.new loser pane.rename", got.Winner, got.Loser)
	}
	if id, ok := km.MatchTier(TierLate, "ctrl+b"); ok {
		t.Errorf("the shadowed chord must be removed from the tier map, still resolves to %q", id)
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b")); kind != MatchPartial {
		t.Errorf("ctrl+b must now be MatchPartial, got %v", kind)
	}
}

func TestConflictShadowed_Message(t *testing.T) {
	c := Conflict{Kind: ConflictShadowed, Key: "ctrl+b", Winner: "tab.new", Loser: "pane.rename"}
	want := `shadowed by a longer sequence: "ctrl+b" → tab.new wins, pane.rename never fires`
	if got := c.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: ConflictShadowed`.

- [ ] **Step 3: Swap the conflict kind**

In `internal/keymap/conflict.go`, replace the `ConflictUnsupportedSequence` const and its `String()` arm:

```go
	// ConflictShadowed: a chord or sequence is a strict prefix of a longer
	// sequence, so pressing it always arms the prefix machine and it can never
	// fire on its own. Within one layer the SHORTER binding is refused —
	// length is a safe tie-break here only because both sides tie on layer.
	// Stage 3's cross-layer rule is different and deliberately so.
	ConflictShadowed
```

```go
	case ConflictShadowed:
		return "shadowed by a longer sequence"
```

Delete every remaining reference to `ConflictUnsupportedSequence`, including the one in `keymap.go`'s insertion loop (already gone after Task 1) and its Stage 1 test.

- [ ] **Step 4: Detect and refuse shadowed bindings**

In `internal/keymap/keymap.go`, add to `detectShadowing` before the sort:

```go
	// Sequence shadowing. A chord or sequence that is a proper prefix of a
	// longer sequence can never fire: the probe reports Partial and swallows
	// the key every time. Refuse the shorter one so the tables cannot offer a
	// binding that dispatch will never reach.
	for _, tier := range []Tier{TierEarly, TierLate} {
		for key, id := range k.chords[tier] {
			if winner, ok := k.partial[key]; ok {
				out = append(out, Conflict{Kind: ConflictShadowed, Key: key, Winner: winner, Loser: id})
				delete(k.chords[tier], key)
			}
		}
	}
	for full, id := range k.seqs {
		if winner, ok := k.partial[full]; ok {
			out = append(out, Conflict{Kind: ConflictShadowed, Key: full, Winner: winner, Loser: id})
			delete(k.seqs, full)
		}
	}
```

Deleting the current key during a `range` over a map is defined behaviour in Go and is safe here.

- [ ] **Step 5: Run the test and verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/keymap/conflict.go internal/keymap/keymap.go internal/keymap/conflict_test.go
git commit -m "feat(keymap): refuse bindings shadowed by a longer sequence"
```

---

### Task 3: Extract the two dispatch switches into methods

**Files:**
- Modify: `internal/tui/model.go` (`handleKey`, lines ~3928-4055 and ~4114-4232)
- Test: `internal/tui/keydispatch_test.go` (existing — must keep passing unchanged)

**Interfaces:**
- Produces: `func (m Model) runEarlyAction(id keymap.ActionID, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool)`, `func (m Model) runLateAction(id keymap.ActionID, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool)`, `func (m Model) runAction(id keymap.ActionID, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool)`.

**This task changes no behaviour.** It is a pure refactor, and it exists because Task 4 needs to *run* an action resolved from a sequence — but the dispatch logic currently lives in two inline `switch` blocks that only `handleKey` can reach.

The third return value reports whether the switch matched. `handleKey` falls through on `false` exactly as the `switch` did.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/keydispatch_test.go`:

```go
// runAction routes by the action's registered Tier. The sequence machine is
// its only caller and has no tier of its own to work from.
func TestRunAction_RoutesByTier(t *testing.T) {
	m := newModelForTest(t)

	// pane.focus_toggle is TierLate; toggling it twice returns to the start.
	before := m.activeTabModel().FocusMode()
	updated, _, handled := m.runAction("pane.focus_toggle", tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if !handled {
		t.Fatal("runAction(pane.focus_toggle) reported unhandled")
	}
	if got := updated.(Model).activeTabModel().FocusMode(); got == before {
		t.Errorf("focus mode did not toggle: still %v", got)
	}
}

func TestRunAction_UnknownIDIsUnhandled(t *testing.T) {
	m := newModelForTest(t)
	if _, _, handled := m.runAction("no.such.action", tea.KeyPressMsg{Code: 'x'}); handled {
		t.Error("an unregistered action must report unhandled, not silently succeed")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.runAction undefined`.

- [ ] **Step 3: Extract the switches**

In `internal/tui/model.go`, move the body of the early switch into a method. The `case` labels and their bodies are copied **verbatim**; only the wrapper changes:

```go
// runEarlyAction dispatches an early-tier action. Extracted from handleKey so
// the sequence machine can run an action it resolved without duplicating the
// switch. handled is false when the ID matches no case, and the caller then
// falls through exactly as the switch did.
//
// The early/late split is not cosmetic: tryPluginRawKey runs between the two
// lookups, so an early action beats a plugin's raw_keys claim and a late one
// loses to it. Moving a case between these two methods silently changes that.
func (m Model) runEarlyAction(id keymap.ActionID, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch id {
	case "notification.toggle":
		m.notifications.visible = !m.notifications.visible
		m.sidebarFocused = false
		if m.notifications.visible {
			return m, tea.Batch(tea.ClearScreen, m.startSidebarTick()), true
		}
		return m, tea.ClearScreen, true
	// ... every remaining early case, verbatim, with `, true` appended to each return ...
	}
	return m, nil, false
}
```

Do the same for the late switch as `runLateAction`. Then add the router:

```go
// runAction dispatches by the action's registered Tier. The sequence machine
// is the caller this exists for: a sequence resolves to an ActionID with no
// tier context of its own, and running it through the wrong switch would put
// it on the wrong side of tryPluginRawKey.
func (m Model) runAction(id keymap.ActionID, msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	a, ok := keymap.Lookup(id)
	if !ok {
		return m, nil, false
	}
	if a.Tier == keymap.TierEarly {
		return m.runEarlyAction(id, msg)
	}
	return m.runLateAction(id, msg)
}
```

Replace both switches in `handleKey` with calls:

```go
	earlyID, _ := m.keymap.MatchTier(keymap.TierEarly, key)
	if next, cmd, handled := m.runEarlyAction(earlyID, msg); handled {
		return next, cmd
	}
```

```go
	lateID, _ := m.keymap.MatchTier(keymap.TierLate, key)
	if next, cmd, handled := m.runLateAction(lateID, msg); handled {
		return next, cmd
	}
```

- [ ] **Step 4: Run the full TUI suite and verify nothing moved**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS, including `TestActions_TierSplitMatchesLegacySwitches` and `TestDispatch_RawKeysPrecedence`. A failure here means a `case` landed in the wrong method — that is the one mistake this refactor can make, and it is silent at runtime.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/keydispatch_test.go
git commit -m "refactor(tui): extract handleKey's tier switches into methods"
```

---

### Task 4: Pending state and the sequence probe

**Files:**
- Modify: `internal/tui/model.go` (`Model` struct, `handleKey` at ~3924)
- Create: `internal/tui/sequence_test.go`

**Interfaces:**
- Consumes: `MatchSeq`/`MatchKind` (Task 1), `runAction` (Task 3).
- Produces: `Model.pendingSeq []keymap.Chord`, `Model.pendingGen int`, `func (m Model) stepSequence(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd, bool)`.

The probe goes at **one** site: after the overlay guard (`model.go:3922`) and before the early-tier lookup.

Everything that fully owns the keyboard returns above that point — dialog, rename, pane-rename, context menu, overlay. The reconnect/parked screen never reaches `handleKey` at all: `freezeInput` is called unconditionally at `model.go:1068` and returns `frozen`. Only two modes sit downstream and must be named explicitly.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/sequence_test.go`:

```go
package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// press drives one key through Update and returns the resulting Model.
func press(t *testing.T, m Model, msg tea.KeyPressMsg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	got, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return got
}

// Sequences are driven through Update, never through MatchSeq directly. A
// direct-call test and its mutation can both pass against a decision the call
// site makes unreachable — this repo has hit that trap before.
func TestSequence_CompletesThroughUpdate(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	before := len(m.activeProject().Tabs)
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 1 {
		t.Fatalf("after ctrl+b, pendingSeq = %v, want one chord armed", m.pendingSeq)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'c'})
	if len(m.pendingSeq) != 0 {
		t.Errorf("pendingSeq not cleared after completion: %v", m.pendingSeq)
	}
	if got := len(m.activeProject().Tabs); got != before+1 {
		t.Errorf("tab count = %d, want %d — the sequence did not fire tab.new", got, before+1)
	}
}

// The property that makes Stage 2 safe to merge: a chord that opens no
// sequence must dispatch exactly as it did before the machine existed.
func TestSequence_UnrelatedChordStillDispatches(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	before := m.activeTabModel().FocusMode()
	m = press(t, m, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}) // pane.focus_toggle
	if len(m.pendingSeq) != 0 {
		t.Errorf("an unrelated chord must not arm the machine: %v", m.pendingSeq)
	}
	if m.activeTabModel().FocusMode() == before {
		t.Error("ctrl+e stopped toggling focus mode once the machine existed")
	}
}

// Tier-agnostic probe, end to end. pane.close is late-tier, so its opening
// chord is in neither tier map — if the probe were tier-scoped this hangs at
// the first chord forever.
func TestSequence_LateTierSequenceCompletes(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.ClosePane = "ctrl+b x"
	m.initKeymap()

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 1 {
		t.Fatalf("a late-tier sequence head must arm the machine, pendingSeq = %v", m.pendingSeq)
	}
}

func TestSequence_InertWhileSidebarFocused(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.notifications.visible = true
	m.sidebarFocused = true

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert while the sidebar has focus, got %v", m.pendingSeq)
	}
}

func TestSequence_InertDuringSelection(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.selection = &Selection{}

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert during an active selection, got %v", m.pendingSeq)
	}
}

func TestSequence_InertWhileDialogOpen(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.dialog = dialogSettings

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 0 {
		t.Errorf("the machine must be inert while a dialog owns the keyboard, got %v", m.pendingSeq)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.pendingSeq undefined`.

- [ ] **Step 3: Add the state fields**

In `internal/tui/model.go`, on the `Model` struct beside `keymap` / `keyConflicts`:

```go
	// pendingSeq holds the chords typed so far in a multi-step binding. Empty
	// means the machine is idle. pendingGen is bumped on every state change so
	// a cancelled sequence's late timeout tick cannot clear a newly started one.
	pendingSeq []keymap.Chord
	pendingGen int
```

- [ ] **Step 4: Add stepSequence**

```go
// stepSequence advances the prefix machine by one chord. handled is true when
// the machine consumed the key; false means handleKey continues as it always
// did.
//
// TIER-AGNOSTIC on purpose. See Keymap.MatchSeq — a late-tier sequence's
// opening chord is in neither tier map, so a tier-scoped probe could never
// complete it.
func (m Model) stepSequence(msg tea.KeyPressMsg, key string) (tea.Model, tea.Cmd, bool) {
	c, err := keymap.ParseChord(key)
	if err != nil {
		return m, nil, false
	}

	cand := make([]keymap.Chord, 0, len(m.pendingSeq)+1)
	cand = append(cand, m.pendingSeq...)
	cand = append(cand, c)

	switch id, kind := m.keymap.MatchSeq(cand); kind {
	case keymap.MatchPartial:
		m.pendingSeq = cand
		m.pendingGen++
		return m, nil, true
	case keymap.MatchExact:
		// Only a genuine multi-step completion is dispatched here. A length-1
		// Exact is a plain chord: leave it to the tier lookups, or it would run
		// on the wrong side of tryPluginRawKey and quietly beat a plugin's
		// raw_keys claim it is supposed to lose to.
		if len(cand) == 1 {
			return m, nil, false
		}
		m.pendingSeq = nil
		m.pendingGen++
		next, cmd, _ := m.runAction(id, msg)
		return next, cmd, true
	}

	// No match. A pending sequence is dropped; a bare chord falls through to
	// the existing dispatch untouched, which is what keeps every single-chord
	// binding byte-identical to its pre-Stage-2 behaviour.
	if len(m.pendingSeq) > 0 {
		m.pendingSeq = nil
		m.pendingGen++
		return m, nil, true
	}
	return m, nil, false
}
```

- [ ] **Step 5: Wire the probe into handleKey**

Immediately after the overlay guard (`model.go:3922`) and before `earlyID, _ := ...`:

```go
	// Prefix sequence machine. Placed here so every mode that fully owns the
	// keyboard is already inert by construction: dialog, rename, pane-rename,
	// context menu and overlay all return above. The reconnect/parked screen
	// never reaches handleKey at all — freezeInput (Update, ~line 1068) is
	// called unconditionally and returns frozen.
	//
	// The two modes below sit DOWNSTREAM of this point, so ordering does not
	// cover them and they must be named. Add a mode here if you add one that
	// consumes keys after this line.
	sequenceInert := (m.sidebarFocused && m.notifications.visible) || m.selection != nil
	if !sequenceInert {
		if next, cmd, handled := m.stepSequence(msg, key); handled {
			return next, cmd
		}
	}
```

- [ ] **Step 6: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS, and the whole Stage 1 dispatch suite still green.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/model.go internal/tui/sequence_test.go
git commit -m "feat(tui): dispatch multi-chord key sequences"
```

---

### Task 5: Cancellation

**Files:**
- Modify: `internal/tui/model.go` (`Update` — `tea.PasteMsg` at ~1825, `tea.MouseClickMsg`, `stepSequence`)
- Test: `internal/tui/sequence_test.go`

**Interfaces:**
- Produces: `func (m *Model) cancelSequence()`.

Three cancellations, each for a concrete reason:

- **Esc** — the universal escape hatch. No mode may swallow it.
- **Mouse click** — a click can change the active pane mid-sequence, so a completed action would target a different pane than the one the prefix was pressed in.
- **`tea.PasteMsg`** — paste bypasses `handleKey` entirely and lands in the PTY. Leaving a prefix armed across it means the next keystroke is read as a sequence step.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/sequence_test.go`:

```go
func TestSequence_EscCancels(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if len(m.pendingSeq) != 0 {
		t.Errorf("esc must cancel a pending sequence, got %v", m.pendingSeq)
	}
}

// A click can change the active pane, so a sequence completed after one would
// target a different pane than the one the prefix was pressed in.
func TestSequence_MouseClickCancels(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	updated, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("a mouse click must cancel a pending sequence, got %v", got.pendingSeq)
	}
}

// Paste bypasses handleKey and lands in the PTY; an armed prefix would read
// the next keystroke as a sequence step.
func TestSequence_PasteCancels(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	updated, _ := m.Update(tea.PasteMsg("hello"))
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("a paste must cancel a pending sequence, got %v", got.pendingSeq)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — the mouse and paste cases leave `pendingSeq` armed.

- [ ] **Step 3: Add the helper and the three call sites**

In `internal/tui/model.go`:

```go
// cancelSequence clears any pending prefix sequence. Bumping the generation is
// what stops a cancelled sequence's in-flight timeout tick from clearing a
// sequence started after it.
func (m *Model) cancelSequence() {
	if len(m.pendingSeq) == 0 {
		return
	}
	m.pendingSeq = nil
	m.pendingGen++
}
```

In `stepSequence`, before the `MatchSeq` call:

```go
	// Esc always cancels, and must do so before the probe: a binding could
	// legitimately use esc as a sequence step, and the escape hatch outranks it.
	if key == "esc" && len(m.pendingSeq) > 0 {
		m.cancelSequence()
		return m, nil, true
	}
```

In `Update`'s `tea.PasteMsg` arm (~1825) and in the `tea.MouseClickMsg` arm, add `m.cancelSequence()` as the first statement.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/sequence_test.go
git commit -m "feat(tui): cancel pending key sequences on esc, click and paste"
```

---

### Task 6: The literal escape

**Files:**
- Modify: `internal/tui/model.go` (`stepSequence`)
- Test: `internal/tui/sequence_test.go`

Pressing the prefix chord twice sends **one** literal chord to the PTY.

This is not a nicety. Quil panes routinely `ssh` into hosts running real tmux, and without it the remote tmux has no reachable prefix — the outer Quil eats every `Ctrl+B` forever. tmux solves it the same way, so it is already in the muscle memory of the users the tmux preset exists for.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/sequence_test.go`:

```go
// prefix prefix sends ONE literal chord downstream. Without this, a pane
// running ssh to a host running tmux has no reachable prefix at all.
func TestSequence_LiteralEscape(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: ctrl+b did not arm the machine, got %v", m.pendingSeq)
	}

	msg := tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
	next, _, handled := m.stepSequence(msg, msg.String())
	if handled {
		t.Fatal("the second prefix press must fall through to the PTY, not be consumed")
	}
	if len(next.(Model).pendingSeq) != 0 {
		t.Errorf("the machine must disarm on the literal escape, got %v", next.(Model).pendingSeq)
	}
	if got := string(keyToBytes(msg)); got != "\x02" {
		t.Errorf("keyToBytes(ctrl+b) = %q, want \\x02 — the literal escape relies on this", got)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — the second press re-probes and the machine stays armed.

- [ ] **Step 3: Implement the escape**

In `stepSequence`, immediately after the Esc guard:

```go
	// Literal escape: prefix prefix sends ONE raw chord to the PTY. Returning
	// unhandled hands the ORIGINAL KeyPressMsg to handleKey's default branch,
	// so keyToBytes does the encoding and there is no second byte table to
	// keep in sync.
	if len(m.pendingSeq) == 1 && m.pendingSeq[0] == c {
		m.cancelSequence()
		return m, nil, false
	}
```

`Chord` is a struct of a `Mod` and a `string`, so `==` is the correct comparison.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/model.go internal/tui/sequence_test.go
git commit -m "feat(tui): send a literal chord on a doubled sequence prefix"
```

---

### Task 7: Status bar indicator

**Files:**
- Modify: `internal/tui/model.go` (`renderStatusBar` at ~5630, `stepSequence`)
- Test: `internal/tui/sequence_test.go`

**Interfaces:**
- Produces: `Model.seqFlash string`.

A silent armed state is indistinguishable from a wedged TUI — that is the support ticket this prevents. A dropped sequence flashes rather than silently no-opping, on the same principle that puts conflict warnings in F1: the user pressed two keys deliberately and deserves to know why nothing happened.

The flash clears on the next keypress rather than on a timer. One less timer, one less generation guard, and the message is on screen exactly as long as the user is looking at it.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/sequence_test.go`:

```go
func TestSequence_StatusBarShowsPending(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.lastWidth, m.lastHeight = 120, 40

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if !strings.Contains(m.renderStatusBar(), "ctrl+b") {
		t.Errorf("the status bar must show the pending chords, got %q", m.renderStatusBar())
	}
}

func TestSequence_DroppedSequenceFlashes(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.lastWidth, m.lastHeight = 120, 40

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = press(t, m, tea.KeyPressMsg{Code: 'z'}) // no ctrl+b z binding
	if m.seqFlash == "" {
		t.Error("a dropped sequence must set a flash message, not silently no-op")
	}
	if !strings.Contains(m.renderStatusBar(), "ctrl+b z") {
		t.Errorf("the flash must name the sequence that was dropped, got %q", m.renderStatusBar())
	}
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.seqFlash undefined`.

- [ ] **Step 3: Add the field and set it**

On the `Model` struct, beside `pendingSeq`:

```go
	// seqFlash reports a dropped sequence in the status bar. Cleared on the
	// next keypress rather than by a timer — one less timer to guard, and it
	// stays up exactly as long as the user is still looking at the screen.
	seqFlash string
```

In `stepSequence`, at the top:

```go
	m.seqFlash = ""
```

and in the no-match-with-pending branch, before `cancelSequence`:

```go
		m.seqFlash = keymap.Sequence(cand).String() + " is not bound"
```

- [ ] **Step 4: Render it**

In `renderStatusBar`, before the existing segments are joined:

```go
	// Pending prefix, or the message from the last dropped one. An armed
	// machine that shows nothing is indistinguishable from a frozen TUI.
	if len(m.pendingSeq) > 0 {
		segments = append(segments, keymap.Sequence(m.pendingSeq).String()+"…")
	} else if m.seqFlash != "" {
		segments = append(segments, m.seqFlash)
	}
```

Adapt `segments` to whatever the function's existing accumulator is named.

- [ ] **Step 5: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/sequence_test.go
git commit -m "feat(tui): show pending and dropped key sequences in the status bar"
```

---

### Task 8: Sequence timeout with a generation guard

**Files:**
- Modify: `internal/tui/model.go` (`stepSequence`, `Update`)
- Test: `internal/tui/sequence_test.go`

**Interfaces:**
- Produces: `type sequenceTimeoutMsg struct{ gen int }`, `func (m Model) armSequenceTimeout() tea.Cmd`, `Model.seqTimeout time.Duration`.

**Default is off**, matching tmux. Stage 3 supplies the configured value; this task builds the machinery with the field zero.

The generation guard is the point of the task. Without it, a cancelled sequence's in-flight tick clears a sequence started after it — the `paletteSearchTimeout` compare-on-key precedent. **Like every local timer in this codebase, the Update arm must not re-arm `listenForMessages`.**

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/sequence_test.go`:

```go
// A stale tick from a cancelled sequence must not clear the one started after
// it. Without the generation guard this is a race that only shows up under a
// fast typist, which is the worst way to find it.
func TestSequence_StaleTimeoutTickIsIgnored(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.seqTimeout = time.Second

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	stale := m.pendingGen
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // cancel
	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if len(m.pendingSeq) != 1 {
		t.Fatalf("setup: the second ctrl+b did not arm, got %v", m.pendingSeq)
	}

	updated, _ := m.Update(sequenceTimeoutMsg{gen: stale})
	if got := updated.(Model); len(got.pendingSeq) != 1 {
		t.Errorf("a stale tick cleared a live sequence: pendingSeq = %v", got.pendingSeq)
	}
}

func TestSequence_CurrentTimeoutTickClears(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()
	m.seqTimeout = time.Second

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	updated, _ := m.Update(sequenceTimeoutMsg{gen: m.pendingGen})
	if got := updated.(Model); len(got.pendingSeq) != 0 {
		t.Errorf("the current tick must clear the sequence, got %v", got.pendingSeq)
	}
}

// Off by default, matching tmux. A zero duration must arm no timer at all.
func TestSequence_TimeoutOffArmsNoTimer(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NewTab = "ctrl+b c"
	m.initKeymap()

	if m.seqTimeout != 0 {
		t.Fatalf("the shipped default must be off, got %v", m.seqTimeout)
	}
	if cmd := m.armSequenceTimeout(); cmd != nil {
		t.Error("a zero timeout must arm no timer")
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `undefined: sequenceTimeoutMsg`.

- [ ] **Step 3: Add the field, message and arming helper**

On the `Model` struct:

```go
	// seqTimeout drops a pending sequence after this long. Zero = off, which
	// is the shipped default and matches tmux.
	seqTimeout time.Duration
```

```go
// sequenceTimeoutMsg drops a pending sequence. gen pins it to the sequence
// that armed it: without that, a cancelled sequence's in-flight tick clears
// the one the user started after it.
type sequenceTimeoutMsg struct{ gen int }

// armSequenceTimeout returns a tick for the current sequence generation, or
// nil when the timeout is off.
func (m Model) armSequenceTimeout() tea.Cmd {
	if m.seqTimeout <= 0 {
		return nil
	}
	gen := m.pendingGen
	return tea.Tick(m.seqTimeout, func(time.Time) tea.Msg {
		return sequenceTimeoutMsg{gen: gen}
	})
}
```

- [ ] **Step 4: Arm it and handle the tick**

In `stepSequence`'s `MatchPartial` branch, return the tick:

```go
	case keymap.MatchPartial:
		m.pendingSeq = cand
		m.pendingGen++
		return m, m.armSequenceTimeout(), true
```

In `Update`, add an arm. **It must not re-arm `listenForMessages`** — this is a local timer, and re-arming the listener from a local tick is how this codebase has previously multiplied its own message loop:

```go
	case sequenceTimeoutMsg:
		if msg.gen == m.pendingGen {
			m.cancelSequence()
		}
		return m, nil
```

- [ ] **Step 5: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/model.go internal/tui/sequence_test.go
git commit -m "feat(tui): add an optional key-sequence timeout"
```

---

### Task 9: Retire the last two `kbMatches` dispatch sites

**Files:**
- Modify: `internal/tui/model.go` (notes block ~3864-3912, `notesKeyExempt` ~3571-3627), `internal/tui/keymatch.go` (comment)
- Test: `internal/tui/keyreaders_test.go`

`keyspecs.go`'s own comment names this as Stage 2's job **and Stage 3's hard prerequisite**: a Stage 3 that empties the legacy table while these readers remain leaves them comparing against empty strings, at which point `Alt+E` stops exiting notes mode and every structural key stops flushing the editor.

The third site — `Update`'s reconnect resume-key check at `model.go:1053` — matches against the hardcoded `reconnectResumeKey` constant (`reconnect.go:527`), not a config value. **It stays.**

These are mechanical swaps. Behaviour must not move.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/keyreaders_test.go`:

```go
// The notes-mode key split must resolve through the registry, or Stage 3's
// empty [keybindings] table leaves it comparing against "" — at which point
// Alt+E stops exiting notes mode.
func TestNotesMode_ToggleHonoursMultiBinding(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.NotesToggle = "alt+e,f9"
	m.initKeymap()
	m.notesMode = true
	m.notesEditor = newTestNotesEditor(t)

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyF9})
	if m.notesMode && m.notesEditor != nil {
		// exitNotesMode returns a new Model; re-drive through Update instead.
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyF9})
		if updated.(Model).notesMode {
			t.Error("a non-primary notes_toggle binding must still exit notes mode")
		}
	}
}

func TestNotesKeyExempt_ResolvesThroughRegistry(t *testing.T) {
	m := newModelForTest(t)
	m.cfg.Keybindings.CloseTab = "alt+w,f10"
	m.initKeymap()

	if !m.notesKeyExempt("f10") {
		t.Error("a non-primary close_tab binding must be exempt in notes mode")
	}
	if m.notesKeyExempt("ctrl+shift+f12") {
		t.Error("an unbound key must not be exempt")
	}
}
```

If `newTestNotesEditor` does not exist, add a small helper in the same file that constructs a notes editor over `t.TempDir()`.

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — the fallback binding is not recognised.

- [ ] **Step 3: Convert the notes-mode key split**

Replace the four `kbMatches` cases (`model.go:3868-3884`) and the `structural` expression (`3891-3892`):

```go
		switch {
		case m.isAction(key, "pane.notes_toggle"):
			return m.exitNotesMode()
		case m.isAction(key, "app.quit"):
			if err := m.notesEditor.Close(); err != nil {
				log.Printf("save notes on quit: %v", err)
			}
			return m, tea.Quit
		case m.isAction(key, "pane.left"):
			m.notesPaneFocused = true
			return m, nil
		case m.isAction(key, "pane.right"):
			m.notesPaneFocused = false
			return m, nil
		}

		structural := m.isAction(key, "pane.close") || m.isAction(key, "tab.close") ||
			m.isAction(key, "pane.split_h") || m.isAction(key, "pane.split_v")
```

Keep every surrounding comment — they explain the layout reasoning, not the matcher.

- [ ] **Step 4: Convert notesKeyExempt**

Replace the `[]string` of config values and its `kbMatches` loop with a list of action IDs:

```go
	// Exempt ACTIONS, not config strings. The comments below explain why each
	// one belongs here; they are unchanged from the config-string version.
	exempt := []keymap.ActionID{
		// Vertical spatial nav — there's no up/down axis in the notes 2-panel
		// layout (pane|editor), so Alt+Up/Alt+Down flush and exit notes, then
		// the global handler runs NavigateDirection to the closest neighbor.
		// Alt+Left and Alt+Right are handled by the notes-mode focus toggle
		// earlier in handleKey and never reach this function.
		"pane.up", "pane.down",
		// Structural — close/split implicitly destroys the bound pane and must
		// flush + exit notes before running.
		"pane.close", "tab.close", "pane.split_h", "pane.split_v",
		// Tab management.
		"tab.new", "tab.rename", "pane.rename", "tab.cycle_color",
		// Other modes.
		"pane.focus_toggle",
		// Force repaint — view-level, harmless while the editor is open.
		"app.redraw",
		// Notification center.
		"notification.toggle", "notification.focus", "pane.go_back",
		"pane.mute", "pane.toggle_eager",
		// Project sidebar — view-level, and resizeAllPanes covers the notes
		// layout's own dependency on paneAreaWidth().
		"sidebar.toggle",
		// Preview wrap toggle — pane-level view state, harmless in notes mode.
		"pane.toggle_wrap",
		// Pane process restart — opens a confirm dialog, never types into
		// the notes editor.
		"pane.restart",
		// Tools and dialogs.
		"json.transform", "pane.quick_actions", "pane.command_history", "project.new",
		// Project navigation — switchProject (reached by both) already calls
		// exitNotesModeInPlace itself, so exempting these just lets the key
		// reach it instead of being swallowed as editor text.
		"project.picker", "project.toggle", "project.next", "project.prev",
		// Attention queue — notes are exactly the sort of thing left open
		// while an agent grinds in another pane.
		"project.attention_queue",
	}
	for _, id := range exempt {
		if m.isAction(key, id) {
			return true
		}
	}
```

Delete the now-unused `kb := m.cfg.Keybindings` line. Leave the trailing `switch key { case "f1", "ctrl+n": ... }` block alone — those are hardcoded keys, not actions.

- [ ] **Step 5: Update the keymatch.go header comment**

`kbMatches` now has exactly one production caller. Rewrite the comment to say so, and keep the "do not add new call sites" instruction.

- [ ] **Step 6: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS, including `TestModel_NotesKeyExempt_AllowsGlobalShortcuts`.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/model.go internal/tui/keymatch.go internal/tui/keyreaders_test.go
git commit -m "refactor(tui): resolve notes-mode keys through the action registry"
```

---

# Stage 3 — `bindings.toml`, presets, migration

### Task 10: Pure layer merge

**Files:**
- Create: `internal/keymap/resolve.go`, `internal/keymap/resolve_test.go`

**Interfaces:**
- Produces: `func Resolve(layers ...map[ActionID]string) map[ActionID]string`.

Merge semantics: **replace, not union**. Per action, a higher layer replaces the lower entirely. Absence inherits; presence replaces; `""` unbinds.

A pure function with no I/O, so `internal/keymap` keeps importing nothing that needs `QUIL_HOME`.

- [ ] **Step 1: Write the failing test**

Create `internal/keymap/resolve_test.go`:

```go
package keymap

import "testing"

func TestResolve_AbsenceInherits(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"tab.new": "ctrl+t", "pane.close": "ctrl+w"},
		map[ActionID]string{"tab.new": "ctrl+b c"},
	)
	if got["pane.close"] != "ctrl+w" {
		t.Errorf("an action the higher layer omits must inherit, got %q", got["pane.close"])
	}
	if got["tab.new"] != "ctrl+b c" {
		t.Errorf("a present binding must replace, got %q", got["tab.new"])
	}
}

// "" is a VALUE — an explicit unbind — not an absence.
func TestResolve_EmptyStringUnbinds(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"project.picker": "alt+p"},
		map[ActionID]string{"project.picker": ""},
	)
	binding, present := got["project.picker"]
	if !present {
		t.Fatal("an explicit unbind must survive as a key, or the lower layer leaks back in")
	}
	if binding != "" {
		t.Errorf("got %q, want an empty binding", binding)
	}
}

// Each layer replaces wholesale; "" does not propagate upward. A preset that
// unbinds an action must not veto the user's own override of it.
func TestResolve_UserOverrideBeatsPresetUnbind(t *testing.T) {
	got := Resolve(
		map[ActionID]string{"project.picker": "alt+p"},   // default
		map[ActionID]string{"project.picker": ""},        // preset unbinds
		map[ActionID]string{"project.picker": "alt+j"},   // user reclaims
	)
	if got["project.picker"] != "alt+j" {
		t.Errorf("the user layer must win, got %q", got["project.picker"])
	}
}

func TestResolve_NoLayersIsEmpty(t *testing.T) {
	if got := Resolve(); len(got) != 0 {
		t.Errorf("Resolve() with no layers = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: Resolve`.

- [ ] **Step 3: Implement Resolve**

Create `internal/keymap/resolve.go`:

```go
package keymap

// Resolve merges binding layers, lowest priority first.
//
// Semantics are REPLACE, not union: per action, a higher layer replaces the
// lower entirely. Absence inherits, presence replaces, and "" is a value — an
// explicit unbind — rather than a deletion that propagates upward.
//
// That last distinction is what lets a user reclaim an action a preset
// unbound. A union merge, or treating "" as a removal, would make a preset
// able to veto an explicit user override — inverting the layering model.
//
// Pure: no I/O, no paths, no config types. Reading the layers off disk belongs
// to internal/config, which already owns every QuilDir()-derived path.
func Resolve(layers ...map[ActionID]string) map[ActionID]string {
	out := map[ActionID]string{}
	for _, layer := range layers {
		for id, spec := range layer {
			out[id] = spec
		}
	}
	return out
}
```

- [ ] **Step 4: Run the test and verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/resolve.go internal/keymap/resolve_test.go
git commit -m "feat(keymap): add pure binding-layer resolution"
```

---

### Task 11: `${prefix}` expansion and validation

**Files:**
- Create: `internal/keymap/prefix.go`, `internal/keymap/prefix_test.go`
- Modify: `internal/keymap/conflict.go` (add `ConflictPrefixInvalid`)

**Interfaces:**
- Consumes: `ParseChord`, `Chord.String`.
- Produces: `func ExpandPrefix(specs map[ActionID]string, prefix string) (map[ActionID]string, []Conflict)`.

`${prefix}` expands **at parse time, before insertion**, so conflict detection and the `partial` set see real chord sequences rather than templates.

Each rejection has a concrete failure behind it:

- **Unset while referenced** — expanding to `""` turns `"${prefix} c"` into the bare chord `c`, which then eats every `c` before the PTY sees it.
- **Contains a comma** (a tmux `prefix2` instinct) — expands into the *alternatives* grammar: `"${prefix} c"` becomes `chord(ctrl+a)` OR `seq(ctrl+b, c)`. Silently wrong.
- **Contains a space** — turns every `"${prefix} c"` into a three-step sequence.

An unmodified printable prefix (`"a"`) is a **warning**, not an error. It eats a letter globally, but it is the user's letter to eat — mirroring the existing `sanitizeRawKeys` warning in `internal/plugin/registry.go`.

- [ ] **Step 1: Write the failing test**

Create `internal/keymap/prefix_test.go`:

```go
package keymap

import "testing"

func TestExpandPrefix_Expands(t *testing.T) {
	got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, "ctrl+b")
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %v", conflicts)
	}
	if got["tab.new"] != "ctrl+b c" {
		t.Errorf("got %q, want %q", got["tab.new"], "ctrl+b c")
	}
}

func TestExpandPrefix_Canonicalizes(t *testing.T) {
	got, _ := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, "Shift+Ctrl+A")
	if got["tab.new"] != "ctrl+shift+a c" {
		t.Errorf("the prefix must be canonicalized before expansion, got %q", got["tab.new"])
	}
}

func TestExpandPrefix_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		// Expanding to "" turns "${prefix} c" into the bare chord c, which
		// then eats every c before the PTY sees it.
		{"unset while referenced", ""},
		// Expands into the ALTERNATIVES grammar: chord(ctrl+a) OR seq(ctrl+b, c).
		{"comma", "ctrl+a, ctrl+b"},
		// Turns every two-step binding into a three-step one.
		{"space", "ctrl+a b"},
		{"not a chord", "ctrl+"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "${prefix} c"}, tt.prefix)
			var found bool
			for _, c := range conflicts {
				if c.Kind == ConflictPrefixInvalid {
					found = true
				}
			}
			if !found {
				t.Errorf("want a ConflictPrefixInvalid, got %v", conflicts)
			}
			if _, present := got["tab.new"]; present {
				t.Error("a binding referencing an invalid prefix must be dropped, not half-expanded")
			}
		})
	}
}

// A spec that never mentions ${prefix} is untouched, and an unset prefix is
// not an error for it.
func TestExpandPrefix_UnreferencedIsInert(t *testing.T) {
	got, conflicts := ExpandPrefix(map[ActionID]string{"tab.new": "ctrl+t"}, "")
	if len(conflicts) != 0 {
		t.Fatalf("an unreferenced prefix must not conflict: %v", conflicts)
	}
	if got["tab.new"] != "ctrl+t" {
		t.Errorf("got %q, want it untouched", got["tab.new"])
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: ExpandPrefix`.

- [ ] **Step 3: Add the conflict kind**

In `internal/keymap/conflict.go`, add the constant and its label and message arm:

```go
	// ConflictPrefixInvalid: ${prefix} was referenced while prefix is unset,
	// or prefix is not exactly one chord. Every binding referencing it is
	// dropped rather than half-expanded.
	ConflictPrefixInvalid
```

```go
	case ConflictPrefixInvalid:
		return "unusable prefix"
```

```go
	case ConflictPrefixInvalid:
		return fmt.Sprintf("%s: %q → %s dropped; %s", c.Kind, c.Key, c.Loser, c.Detail)
```

- [ ] **Step 4: Implement ExpandPrefix**

Create `internal/keymap/prefix.go`:

```go
package keymap

import (
	"fmt"
	"strings"
)

// prefixVar is the token a preset writes where the user's prefix chord goes.
const prefixVar = "${prefix}"

// ExpandPrefix substitutes ${prefix} in every spec, BEFORE parsing. Doing it
// here rather than at match time means conflict detection and the prefix set
// see real chord sequences instead of templates.
//
// A binding that references an unusable prefix is DROPPED, with a conflict —
// never half-expanded. The alternative is worse than useless: "${prefix} c"
// with an empty prefix becomes the bare chord "c", which then eats every c
// before the PTY sees it.
func ExpandPrefix(specs map[ActionID]string, prefix string) (map[ActionID]string, []Conflict) {
	out := make(map[ActionID]string, len(specs))
	var conflicts []Conflict

	canonical, err := validatePrefix(prefix)
	for id, spec := range specs {
		if !strings.Contains(spec, prefixVar) {
			out[id] = spec
			continue
		}
		if err != nil {
			conflicts = append(conflicts, Conflict{
				Kind: ConflictPrefixInvalid, Key: spec, Loser: id, Detail: err.Error(),
			})
			continue
		}
		out[id] = strings.ReplaceAll(spec, prefixVar, canonical)
	}
	return out, conflicts
}

// validatePrefix returns the canonical form of a prefix chord.
//
// Each rejection maps to a specific silent failure. A comma expands into the
// ALTERNATIVES grammar, so "${prefix} c" with prefix "ctrl+a, ctrl+b" becomes
// chord(ctrl+a) OR seq(ctrl+b, c) — a plausible tmux prefix2 instinct that
// produces a keymap nobody wrote. A space turns every two-step binding into a
// three-step one.
func validatePrefix(prefix string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", fmt.Errorf("prefix is unset but ${prefix} is used")
	}
	if strings.Contains(prefix, ",") {
		return "", fmt.Errorf("prefix %q contains a comma; it must be exactly one chord", prefix)
	}
	if strings.Contains(strings.TrimSpace(prefix), " ") {
		return "", fmt.Errorf("prefix %q contains a space; it must be exactly one chord", prefix)
	}
	c, err := ParseChord(prefix)
	if err != nil {
		return "", fmt.Errorf("prefix %q is not a valid chord: %w", prefix, err)
	}
	return c.String(), nil
}

// PrefixWarning reports a prefix that is legal but globally costly: an
// unmodified printable key is swallowed everywhere it is pressed. A warning
// rather than an error — it is the user's letter to spend. Mirrors the
// existing sanitizeRawKeys warning in internal/plugin.
func PrefixWarning(prefix string) string {
	c, err := ParseChord(prefix)
	if err != nil || c.Mods != 0 || len([]rune(c.Key)) != 1 {
		return ""
	}
	return fmt.Sprintf("prefix %q has no modifier: every press of it is swallowed before the pane sees it", prefix)
}
```

- [ ] **Step 5: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/keymap/prefix.go internal/keymap/prefix_test.go internal/keymap/conflict.go
git commit -m "feat(keymap): expand and validate the \${prefix} binding variable"
```

---

### Task 12: Embedded presets and the `default` preset

**Files:**
- Create: `internal/keymap/preset.go`, `internal/keymap/preset_test.go`, `internal/keymap/presets/default.toml`
- Modify: `go.mod` is unchanged — `BurntSushi/toml` is already a dependency

**Interfaces:**
- Produces: `func Preset(name string) (map[ActionID]string, error)`, `func PresetNames() []string`.

`default.toml` must reproduce `config.Default().Keybindings` exactly. Task 18 adds the cross-package test that pins it; this task pins what it can from inside `keymap` — that every action is named and every chord is canonical.

- [ ] **Step 1: Write the failing test**

Create `internal/keymap/preset_test.go`:

```go
package keymap

import "testing"

func TestPreset_DefaultNamesEveryAction(t *testing.T) {
	got, err := Preset("default")
	if err != nil {
		t.Fatalf("Preset(default): %v", err)
	}
	for _, a := range Actions() {
		if _, ok := got[a.ID]; !ok {
			t.Errorf("preset default does not name %q; every action needs a binding or an explicit \"\"", a.ID)
		}
	}
}

// A chord spelled unlike the canonical form is silently dead with the conflict
// checker green — this is the cheapest place to catch it.
func TestPresets_AllChordsCanonical(t *testing.T) {
	for _, name := range PresetNames() {
		specs, err := Preset(name)
		if err != nil {
			t.Fatalf("Preset(%s): %v", name, err)
		}
		for id, spec := range specs {
			if spec == "" {
				continue
			}
			// ${prefix} is expanded later; skip templates.
			if containsPrefixVar(spec) {
				continue
			}
			seqs, err := ParseSpec(spec)
			if err != nil {
				t.Errorf("preset %s, %s = %q: %v", name, id, spec, err)
				continue
			}
			for _, seq := range seqs {
				if got := seq.String(); !specHasSequence(spec, got) {
					t.Errorf("preset %s, %s = %q: chord is not canonical, want %q", name, id, spec, got)
				}
			}
		}
	}
}

func TestPreset_UnknownNameErrors(t *testing.T) {
	if _, err := Preset("no-such-preset"); err == nil {
		t.Error("an unknown preset name must error, not return an empty keymap")
	}
}

func TestPresetNames_IncludesDefault(t *testing.T) {
	var found bool
	for _, n := range PresetNames() {
		if n == "default" {
			found = true
		}
	}
	if !found {
		t.Errorf("PresetNames() = %v, must include default", PresetNames())
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: Preset`.

- [ ] **Step 3: Write the default preset**

Create `internal/keymap/presets/default.toml`. Every value is copied verbatim from the corresponding `Default` field in `internal/config/config.go` — this file **is** the shipped keymap once Task 18 lands:

```toml
# The shipped default keymap. Every value here must equal the matching field in
# config.Default().Keybindings — TestDefaultPreset_MatchesLegacyDefaults pins
# it, because a drift here silently rebinds every existing user's keyboard.

[bindings]
"app.quit"                = "ctrl+q"
"app.redraw"              = "alt+shift+l"
"app.command_palette"     = "alt+shift+p"
"tab.new"                 = "ctrl+t"
"tab.close"               = "alt+w"
"tab.rename"              = "f2"
"tab.cycle_color"         = "alt+c"
"pane.close"              = "ctrl+w"
"pane.restart"            = "alt+r"
"pane.rename"             = "alt+f2,alt+shift+r"
"pane.split_h"            = "alt+shift+h"
"pane.split_v"            = "alt+shift+v"
"pane.focus_toggle"       = "ctrl+e"
"pane.notes_toggle"       = "alt+e"
"pane.paste"              = "ctrl+v"
"pane.scroll_page_up"     = "alt+pgup"
"pane.scroll_page_down"   = "alt+pgdown"
"pane.left"               = "alt+left"
"pane.right"              = "alt+right"
"pane.up"                 = "alt+up"
"pane.down"               = "alt+down"
"pane.next"               = ""
"pane.prev"               = ""
"pane.go_back"            = "alt+backspace"
"pane.mute"               = "alt+m"
"pane.toggle_eager"       = "alt+shift+e"
"pane.toggle_wrap"        = "alt+shift+w"
"pane.toggle_lazygit"     = "alt+g"
"pane.toggle_hunk"        = "alt+d"
"pane.command_history"    = "alt+shift+i"
"pane.quick_actions"      = "alt+a"
"notification.toggle"     = "alt+n"
"notification.focus"      = "f3"
"sidebar.toggle"          = "alt+shift+s"
"project.new"             = "alt+shift+n"
"project.destroy"         = "alt+shift+x"
"project.picker"          = "alt+p"
"project.next"            = "alt+shift+right"
"project.prev"            = "alt+shift+left"
"project.toggle"          = "alt+o"
"project.attention_queue" = "alt+shift+a"
"json.transform"          = "ctrl+j"
```

- [ ] **Step 4: Implement preset loading**

Create `internal/keymap/preset.go`:

```go
package keymap

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed presets/*.toml
var presetFS embed.FS

// presetFile is the on-disk shape of a preset or of bindings.toml. The two
// share a schema deliberately: a user who likes their bindings.toml can rename
// it into presets/ and select it by name with no edits.
type presetFile struct {
	Preset          string             `toml:"preset"`
	Prefix          string             `toml:"prefix"`
	SequenceTimeout string             `toml:"sequence_timeout"`
	Bindings        map[string]string  `toml:"bindings"`
}

// Preset returns a shipped preset's bindings.
func Preset(name string) (map[ActionID]string, error) {
	data, err := presetFS.ReadFile("presets/" + name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("unknown preset %q", name)
	}
	var pf presetFile
	if err := toml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("preset %q: %w", name, err)
	}
	out := make(map[ActionID]string, len(pf.Bindings))
	for id, spec := range pf.Bindings {
		out[ActionID(id)] = spec
	}
	return out, nil
}

// PresetNames lists the shipped presets, sorted.
func PresetNames() []string {
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(path.Base(e.Name()), ".toml"))
	}
	sort.Strings(out)
	return out
}

// containsPrefixVar reports whether a spec still holds an unexpanded ${prefix}.
func containsPrefixVar(spec string) bool { return strings.Contains(spec, prefixVar) }

// specHasSequence reports whether canonical appears in spec's alternatives.
// Used by the preset canonical-form test.
func specHasSequence(spec, canonical string) bool {
	for _, alt := range strings.Split(spec, ",") {
		if strings.Join(strings.Fields(strings.TrimSpace(alt)), " ") == canonical {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/keymap/preset.go internal/keymap/preset_test.go internal/keymap/presets/default.toml
git commit -m "feat(keymap): embed shipped binding presets"
```

---

### Task 13: The twelve new actions

**Files:**
- Modify: `internal/keymap/action.go`, `internal/keymap/conflict.go` (`hardcodedKeys`), `internal/tui/model.go` (`runLateAction`, reserved-key switch), `internal/keymap/presets/default.toml`
- Test: `internal/keymap/action_test.go`, `internal/tui/keydispatch_test.go`

**Interfaces:**
- Produces: actions `tab.next`, `tab.prev`, `tab.switch_1` … `tab.switch_9`, `system.shortcuts`.

The tmux preset needs all twelve and none exist as bindable actions today. `tab.switch_N` is **nine discrete actions**, mirroring `alt+1`…`alt+9`, not a parameterized action the registry has no other use for.

**Promoting `alt+1..9` removes nine entries from `hardcodedKeys`**, which changes the `ConflictHardcoded` messages an affected user sees. That is correct — they are no longer hardcoded — but it is a visible change to F1 rows and log lines, and it looks like an unrelated regression if nobody wrote it down.

`Order` values sit above the current maximum (`json.transform`, 4300), because `alt+1..9` is checked last in `handleKey` today and `Order` is what reproduces that rank.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/keydispatch_test.go`:

```go
func TestTabSwitchActions_SwitchTabs(t *testing.T) {
	m := newModelForTest(t)
	for len(m.activeProject().Tabs) < 3 {
		m, _ = mustModel(t, m.handleNewTab())
	}
	m.initKeymap()

	updated, _, handled := m.runAction("tab.switch_2", tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
	if !handled {
		t.Fatal("tab.switch_2 reported unhandled")
	}
	if got := updated.(Model).activeTabIdx(); got != 1 {
		t.Errorf("active tab index = %d, want 1 (tab.switch_2 is 1-based)", got)
	}
}

func TestTabNextPrev_WrapAround(t *testing.T) {
	m := newModelForTest(t)
	for len(m.activeProject().Tabs) < 3 {
		m, _ = mustModel(t, m.handleNewTab())
	}
	m.initKeymap()
	m.setActiveTabIdx(2)

	updated, _, _ := m.runAction("tab.next", tea.KeyPressMsg{Code: 'n'})
	if got := updated.(Model).activeTabIdx(); got != 0 {
		t.Errorf("tab.next from the last tab = %d, want 0 (wrap)", got)
	}
	updated, _, _ = m.runAction("tab.prev", tea.KeyPressMsg{Code: 'p'})
	if got := updated.(Model).activeTabIdx(); got != 1 {
		t.Errorf("tab.prev = %d, want 1", got)
	}
}

// alt+1..9 are registry actions now, so they must NOT still be reported as
// built-in collisions.
func TestHardcodedKeys_NoLongerClaimAltDigits(t *testing.T) {
	_, conflicts := keymap.Build(map[keymap.ActionID]string{"tab.switch_1": "alt+1"})
	for _, c := range conflicts {
		if c.Kind == keymap.ConflictHardcoded && c.Key == "alt+1" {
			t.Errorf("alt+1 is a registry action now and must not report a hardcoded collision: %v", c)
		}
	}
}
```

Add `mustModel` / `setActiveTabIdx` helpers to the test file if they do not already exist.

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `tab.switch_2` is not a registered action.

- [ ] **Step 3: Register the actions**

Append to `registry` in `internal/keymap/action.go`, after `json.transform`:

```go
	// --- Promoted out of handleKey's reserved-key switch (Stage 3) ---
	//
	// Order sits above every pre-existing action because alt+1..9 was checked
	// LAST in handleKey, once both tier switches had declined. Order is what
	// reproduces that rank for the duplicate tie-break.
	{ID: "tab.next", Label: "Next tab", Group: "Tabs", Tier: TierLate, Order: 4400, Default: ""},
	{ID: "tab.prev", Label: "Previous tab", Group: "Tabs", Tier: TierLate, Order: 4500, Default: ""},
	{ID: "tab.switch_1", Label: "Switch to tab 1", Group: "Tabs", Tier: TierLate, Order: 4600, Default: "alt+1"},
	{ID: "tab.switch_2", Label: "Switch to tab 2", Group: "Tabs", Tier: TierLate, Order: 4700, Default: "alt+2"},
	{ID: "tab.switch_3", Label: "Switch to tab 3", Group: "Tabs", Tier: TierLate, Order: 4800, Default: "alt+3"},
	{ID: "tab.switch_4", Label: "Switch to tab 4", Group: "Tabs", Tier: TierLate, Order: 4900, Default: "alt+4"},
	{ID: "tab.switch_5", Label: "Switch to tab 5", Group: "Tabs", Tier: TierLate, Order: 5000, Default: "alt+5"},
	{ID: "tab.switch_6", Label: "Switch to tab 6", Group: "Tabs", Tier: TierLate, Order: 5100, Default: "alt+6"},
	{ID: "tab.switch_7", Label: "Switch to tab 7", Group: "Tabs", Tier: TierLate, Order: 5200, Default: "alt+7"},
	{ID: "tab.switch_8", Label: "Switch to tab 8", Group: "Tabs", Tier: TierLate, Order: 5300, Default: "alt+8"},
	{ID: "tab.switch_9", Label: "Switch to tab 9", Group: "Tabs", Tier: TierLate, Order: 5400, Default: "alt+9"},
	{ID: "system.shortcuts", Label: "Show keyboard shortcuts", Group: "System", Tier: TierLate, Order: 5500, Default: ""},
```

- [ ] **Step 4: Drop `alt+1..9` from hardcodedKeys**

In `internal/keymap/conflict.go`, delete the loop that adds them:

```go
	// alt+1..9 were promoted to tab.switch_1..9 registry actions in Stage 3.
	// They are no longer intercepted outside the registry, so a binding on one
	// is an ordinary duplicate rather than a built-in collision.
```

- [ ] **Step 5: Add the handlers**

In `runLateAction`, before the closing `return m, nil, false`:

```go
	case "tab.next":
		return m, m.switchTabBy(1), true
	case "tab.prev":
		return m, m.switchTabBy(-1), true
	case "tab.switch_1", "tab.switch_2", "tab.switch_3", "tab.switch_4", "tab.switch_5",
		"tab.switch_6", "tab.switch_7", "tab.switch_8", "tab.switch_9":
		// The ID's last rune is the 1-based tab number; switchTab is 0-based.
		return m, m.switchTab(int(id[len(id)-1]-'1')), true
	case "system.shortcuts":
		return m.openShortcutsDialog()
```

Add the wrapping helper beside `switchTab`:

```go
// switchTabBy moves the active tab by delta, wrapping at both ends. tmux's
// next-window/previous-window wrap, and a non-wrapping version makes the last
// tab a dead end for anyone driving purely from the keyboard.
func (m Model) switchTabBy(delta int) tea.Cmd {
	tabs := len(m.activeProject().Tabs)
	if tabs == 0 {
		return nil
	}
	idx := ((m.activeTabIdx()+delta)%tabs + tabs) % tabs
	return m.switchTab(idx)
}
```

Delete the `alt+1..9` case from the reserved-key switch (`model.go:~4262`). `ctrl+n` and `f1` stay.

If `openShortcutsDialog` does not exist as its own entry point, add one that opens the About dialog directly on the Shortcuts pane.

- [ ] **Step 6: Add the new IDs to the default preset**

Append to `internal/keymap/presets/default.toml`:

```toml
"tab.next"         = ""
"tab.prev"         = ""
"tab.switch_1"     = "alt+1"
"tab.switch_2"     = "alt+2"
"tab.switch_3"     = "alt+3"
"tab.switch_4"     = "alt+4"
"tab.switch_5"     = "alt+5"
"tab.switch_6"     = "alt+6"
"tab.switch_7"     = "alt+7"
"tab.switch_8"     = "alt+8"
"tab.switch_9"     = "alt+9"
"system.shortcuts" = ""
```

- [ ] **Step 7: Update the registry count assertions**

`internal/keymap/action_test.go` asserts the total action count and the early-tier table size. The total goes from 42 to **54**; the early table is unchanged at 18 (every new action is late).

- [ ] **Step 8: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/keymap/` then `./scripts/dev.sh test ./internal/tui/`
Expected: PASS both.

- [ ] **Step 9: Commit**

```bash
git add internal/keymap internal/tui/model.go internal/tui/keydispatch_test.go
git commit -m "feat(keymap): make tab switching and the shortcuts dialog bindable"
```

---

### Task 14: Paste aliases become registry alternatives

**Files:**
- Modify: `internal/tui/model.go` (paste-alias block ~4249), `internal/keymap/conflict.go`, `internal/keymap/presets/default.toml`
- Test: `internal/tui/keydispatch_test.go`

`ctrl+alt+v` and `f8` become alternatives on `pane.paste` in the `default` preset. That makes them visible in F1, user-removable, and removes a compound case from `handleKey`. They leave `hardcodedKeys` with `alt+1..9`.

- [ ] **Step 1: Write the failing test**

```go
func TestPastAliases_AreRegistryAlternatives(t *testing.T) {
	specs, err := keymap.Preset("default")
	if err != nil {
		t.Fatalf("Preset(default): %v", err)
	}
	got := specs["pane.paste"]
	for _, want := range []string{"ctrl+v", "ctrl+alt+v", "f8"} {
		if !strings.Contains(got, want) {
			t.Errorf("pane.paste = %q, must still offer %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — the preset lists only `ctrl+v`.

- [ ] **Step 3: Move the aliases**

In `internal/keymap/presets/default.toml`:

```toml
# ctrl+v is eaten by Windows Terminal, which never delivers the key event to
# the TUI. ctrl+alt+v is ambiguous with AltGr on European layouts. f8 is the
# guaranteed pass-through and the recommended Windows trigger — a user who
# rebinds this to a single chord loses it, which docs/keybindings.md must say.
"pane.paste" = "ctrl+v, ctrl+alt+v, f8"
```

Delete the `if key == "ctrl+alt+v" || key == "f8"` block from `handleKey`, and remove both keys from `hardcodedKeys`.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS. `TestHandleKey_PasteAliasesLoseToLateActions` pins the old precedence and must be **updated**, not deleted — the aliases are ordinary alternatives now, so a late action bound to `f8` is a plain `ConflictDuplicate`.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap internal/tui/model.go internal/tui/keydispatch_test.go
git commit -m "feat(keymap): make the paste aliases ordinary bindings"
```

---

### Task 15: `bindings.toml` read and write

**Files:**
- Create: `internal/config/bindings.go`, `internal/config/bindings_test.go`

**Interfaces:**
- Produces: `func BindingsPath() string`, `type Bindings struct{ Preset, Prefix string; SequenceTimeout time.Duration; Overrides map[keymap.ActionID]string }`, `func LoadBindings() (Bindings, error)`, `func WriteBindings(b Bindings) error`.

`internal/config` owns this because it already owns every `QuilDir()`-derived path (`ConfigPath`, `PluginsDir`, `PasteDir`, `RecentCWDsPath`). `internal/keymap` stays path-free and therefore testable without `QUIL_HOME`.

**Every test in this file must `t.Setenv("QUIL_HOME", t.TempDir())`.** `dev.sh test` runs with a throwaway `/root`, so a test that writes to the real home is green in Docker and pollutes `~/.quil` everywhere else.

- [ ] **Step 1: Write the failing test**

Create `internal/config/bindings_test.go`:

```go
package config

import (
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/keymap"
)

func TestLoadBindings_RoundTrip(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	want := Bindings{
		Preset:          "tmux",
		Prefix:          "ctrl+a",
		SequenceTimeout: 500 * time.Millisecond,
		Overrides:       map[keymap.ActionID]string{"pane.rename": "alt+shift+r"},
	}
	if err := WriteBindings(want); err != nil {
		t.Fatalf("WriteBindings: %v", err)
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if got.Preset != want.Preset || got.Prefix != want.Prefix {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.SequenceTimeout != want.SequenceTimeout {
		t.Errorf("SequenceTimeout = %v, want %v", got.SequenceTimeout, want.SequenceTimeout)
	}
	if got.Overrides["pane.rename"] != "alt+shift+r" {
		t.Errorf("Overrides = %v", got.Overrides)
	}
}

// A missing file is the normal first-launch state, not an error.
func TestLoadBindings_MissingFileIsNotAnError(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("a missing bindings.toml must not error: %v", err)
	}
	if got.Preset != "default" {
		t.Errorf("Preset = %q, want default", got.Preset)
	}
}

// Zero means off, matching tmux, and must survive the round trip as zero
// rather than becoming a parse error or a default.
func TestLoadBindings_ZeroTimeoutIsOff(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := os.WriteFile(BindingsPath(), []byte("sequence_timeout = \"0\"\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if got.SequenceTimeout != 0 {
		t.Errorf("SequenceTimeout = %v, want 0 (off)", got.SequenceTimeout)
	}
}

// Two TUI clients can attach concurrently; the create must not race.
func TestWriteBindingsExclusive_RefusesToClobber(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())
	if err := WriteBindingsExclusive(Bindings{Preset: "default"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteBindingsExclusive(Bindings{Preset: "tmux"}); err == nil {
		t.Error("a second exclusive write must fail rather than overwrite")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: FAIL — `undefined: Bindings`.

- [ ] **Step 3: Implement it**

Create `internal/config/bindings.go`:

```go
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/artyomsv/quil/internal/keymap"
)

// Bindings is the parsed ~/.quil/bindings.toml.
//
// It lives here rather than in internal/keymap because it needs QuilDir(), and
// keeping paths out of keymap is what lets that package be tested without
// QUIL_HOME and without a Model.
type Bindings struct {
	Preset          string
	Prefix          string
	SequenceTimeout time.Duration
	Overrides       map[keymap.ActionID]string
}

// bindingsFile is the wire shape. SequenceTimeout is a string so "0" means off
// and "500ms" parses through time.ParseDuration.
type bindingsFile struct {
	Preset          string            `toml:"preset"`
	Prefix          string            `toml:"prefix"`
	SequenceTimeout string            `toml:"sequence_timeout"`
	Bindings        map[string]string `toml:"bindings"`
}

// BindingsPath is where the user's keymap lives. Derived from QuilDir(), never
// a literal ~/.quil — dev-mode isolation depends on it.
func BindingsPath() string { return filepath.Join(QuilDir(), "bindings.toml") }

// LoadBindings reads bindings.toml. A missing file is the normal first-launch
// state and yields the default preset with no overrides.
func LoadBindings() (Bindings, error) {
	out := Bindings{Preset: "default", Overrides: map[keymap.ActionID]string{}}

	data, err := os.ReadFile(BindingsPath())
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("read bindings: %w", err)
	}
	var bf bindingsFile
	if err := toml.Unmarshal(data, &bf); err != nil {
		return out, fmt.Errorf("parse bindings: %w", err)
	}
	if bf.Preset != "" {
		out.Preset = bf.Preset
	}
	out.Prefix = bf.Prefix
	if bf.SequenceTimeout != "" && bf.SequenceTimeout != "0" {
		d, err := time.ParseDuration(bf.SequenceTimeout)
		if err != nil {
			return out, fmt.Errorf("parse sequence_timeout %q: %w", bf.SequenceTimeout, err)
		}
		out.SequenceTimeout = d
	}
	for id, spec := range bf.Bindings {
		out.Overrides[keymap.ActionID(id)] = spec
	}
	return out, nil
}

// WriteBindings writes bindings.toml atomically: temp file then rename, the
// same crash-safety pattern Save uses.
func WriteBindings(b Bindings) error {
	data, err := encodeBindings(b)
	if err != nil {
		return err
	}
	path := BindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write bindings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename bindings: %w", err)
	}
	return nil
}

// WriteBindingsExclusive creates bindings.toml only if it does not exist.
// O_EXCL rather than a Stat-then-Write, because two TUI clients can attach
// concurrently and a check-then-act would let the second clobber the first.
func WriteBindingsExclusive(b Bindings) error {
	data, err := encodeBindings(b)
	if err != nil {
		return err
	}
	path := BindingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create quil dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create bindings: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write bindings: %w", err)
	}
	return nil
}

func encodeBindings(b Bindings) ([]byte, error) {
	bf := bindingsFile{
		Preset:          b.Preset,
		Prefix:          b.Prefix,
		SequenceTimeout: "0",
		Bindings:        map[string]string{},
	}
	if b.SequenceTimeout > 0 {
		bf.SequenceTimeout = b.SequenceTimeout.String()
	}
	for id, spec := range b.Overrides {
		bf.Bindings[string(id)] = spec
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(bf); err != nil {
		return nil, fmt.Errorf("encode bindings: %w", err)
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/bindings.go internal/config/bindings_test.go
git commit -m "feat(config): read and write bindings.toml"
```

---

### Task 16: One-way migration off `[keybindings]`

**Files:**
- Modify: `internal/config/bindings.go`
- Test: `internal/config/bindings_test.go`

**Interfaces:**
- Produces: `func MigrateBindings(cfg Config, loadErr error) (bool, error)`.

**The diff is load-bearing.** Most installs carry all 42 fields on disk from a past `Save`; copying rather than diffing produces 42 overrides and pins every user to today's defaults permanently — the exact bug this move exists to fix.

**Abort if `Load` errored.** A transiently malformed `config.toml` otherwise migrates as pure defaults and *permanently discards* the user's customizations. The migration is one-way.

**Run after the legacy patches.** `config.go:525` rewrites `quick_actions` from `ctrl+a` to `alt+a` in memory. Migrating before it would faithfully preserve a binding the project deliberately killed. Taking a `Config` that `Load` already returned gives this for free.

- [ ] **Step 1: Write the failing test**

```go
// The whole point of the diff. A config carrying all 42 fields at their shipped
// values must migrate to ZERO overrides — copying instead of diffing pins the
// user to today's defaults forever, which is the bug this move exists to fix.
func TestMigrateBindings_UntouchedConfigYieldsNoOverrides(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	migrated, err := MigrateBindings(Default(), nil)
	if err != nil {
		t.Fatalf("MigrateBindings: %v", err)
	}
	if !migrated {
		t.Fatal("a first launch with no bindings.toml must migrate")
	}
	got, err := LoadBindings()
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(got.Overrides) != 0 {
		t.Errorf("Overrides = %v, want none", got.Overrides)
	}
	if got.Preset != "default" {
		t.Errorf("Preset = %q, want default", got.Preset)
	}
}

func TestMigrateBindings_OneCustomizationYieldsOneOverride(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	cfg := Default()
	cfg.Keybindings.NewTab = "ctrl+shift+t"
	if _, err := MigrateBindings(cfg, nil); err != nil {
		t.Fatalf("MigrateBindings: %v", err)
	}
	got, _ := LoadBindings()
	if len(got.Overrides) != 1 || got.Overrides["tab.new"] != "ctrl+shift+t" {
		t.Errorf("Overrides = %v, want exactly {tab.new: ctrl+shift+t}", got.Overrides)
	}
}

// A transiently malformed config.toml would otherwise migrate as pure defaults
// and permanently discard the user's customizations.
func TestMigrateBindings_LoadErrorWritesNoFile(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	migrated, err := MigrateBindings(Default(), errors.New("parse error"))
	if err == nil {
		t.Error("a Load error must be reported, not swallowed")
	}
	if migrated {
		t.Error("a Load error must not migrate")
	}
	if _, statErr := os.Stat(BindingsPath()); !os.IsNotExist(statErr) {
		t.Error("no bindings.toml may be written when Load errored")
	}
}

func TestMigrateBindings_SecondLaunchIsANoOp(t *testing.T) {
	t.Setenv("QUIL_HOME", t.TempDir())

	if _, err := MigrateBindings(Default(), nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	migrated, err := MigrateBindings(Default(), nil)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if migrated {
		t.Error("a second launch must not migrate again")
	}
}
```

Add `"errors"` to the test imports.

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: FAIL — `undefined: MigrateBindings`.

- [ ] **Step 3: Implement the migration**

Append to `internal/config/bindings.go`:

```go
// MigrateBindings creates bindings.toml from a legacy [keybindings] table on
// first launch. Returns whether it wrote anything.
//
// TUI-ONLY. Keybindings are read exclusively by internal/tui and this package;
// the daemon and the MCP bridge never touch them. config.go's quick_actions
// comment documents why that matters: a startup write from every process that
// loads config would race the same file.
//
// cfg must be the Config that Load already returned, so the in-memory legacy
// patches (notably the quick_actions ctrl+a -> alt+a rewrite) have run. Taking
// a Config rather than a path is what guarantees the ordering.
//
// loadErr is Load's error. A transiently malformed config.toml must NOT
// migrate: it would produce pure defaults and permanently discard the user's
// customizations, and the migration is one-way.
func MigrateBindings(cfg Config, loadErr error) (bool, error) {
	if loadErr != nil {
		return false, fmt.Errorf("refusing to migrate bindings from an unreadable config: %w", loadErr)
	}
	if _, err := os.Stat(BindingsPath()); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat bindings: %w", err)
	}

	// Diff against the shipped defaults. Copying every field instead would
	// produce 42 overrides for an untouched install and pin that user to
	// today's defaults permanently.
	shipped := KeySpecsFromConfig(Default().Keybindings)
	current := KeySpecsFromConfig(cfg.Keybindings)
	overrides := map[keymap.ActionID]string{}
	for id, spec := range current {
		if shipped[id] != spec {
			overrides[id] = spec
			log.Printf("bindings: adopting override %s = %q", id, spec)
		}
	}

	b := Bindings{Preset: "default", Overrides: overrides}
	if err := WriteBindingsExclusive(b); err != nil {
		// A concurrent client won the race and wrote it first. That is a
		// success for our purposes: the file exists and holds the same diff.
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
```

Move `keySpecsFromConfig` from `internal/tui/keyspecs.go` into this package, **exported** as `config.KeySpecsFromConfig(kb KeybindingsConfig) map[keymap.ActionID]string`, body unchanged. It must be exported because both the migration here and `internal/tui`'s `TestDefaultPreset_MatchesLegacyDefaults` need it, and there must be exactly one copy — two drifting maps is how a migrated binding goes missing silently.

Delete the `internal/tui` original and repoint its remaining callers. `internal/config` already imports `internal/keymap` for `Bindings.Overrides`, so this adds no new dependency edge.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config internal/tui/keyspecs.go
git commit -m "feat(config): migrate legacy keybindings into bindings.toml"
```

---

### Task 17: Cross-layer shadowing

**Files:**
- Modify: `internal/keymap/keymap.go` (`Build` signature or a new `BuildLayered`)
- Test: `internal/keymap/conflict_test.go`

**Interfaces:**
- Produces: `func BuildLayered(base, preset, user map[ActionID]string) (*Keymap, []Conflict)`.

Within one layer, the **shorter** sequence is refused (Task 2). **Across layers, resolution is by layer, not by length**: the higher-layer binding survives.

The asymmetry is not arbitrary. A user override `pane.rename = "ctrl+b"` under the tmux preset must be able to reclaim the prefix key as a plain chord. A length-based rule would let a preset veto an explicit user override, inverting the whole layering model. Length is safe as the *intra*-layer tie-break precisely because no cross-layer inversion is possible there.

- [ ] **Step 1: Write the failing test**

```go
// A user override must be able to reclaim the prefix key as a plain chord.
// A length-based rule would let the preset veto it, inverting the layering.
func TestBuildLayered_UserOverrideReclaimsPrefixChord(t *testing.T) {
	km, _ := BuildLayered(
		map[ActionID]string{},                          // base
		map[ActionID]string{"tab.new": "ctrl+b c"},     // preset
		map[ActionID]string{"pane.rename": "ctrl+b"},   // user
	)
	if id, ok := km.MatchTier(TierLate, "ctrl+b"); !ok || id != "pane.rename" {
		t.Errorf("the user's chord must survive, got (%q, %v)", id, ok)
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b c")); kind == MatchExact {
		t.Error("the preset sequence must lose to the higher layer's chord")
	}
}

// Same collision inside ONE layer resolves by length instead: the shorter is
// refused, because both sides tie on layer and length is then unambiguous.
func TestBuildLayered_IntraLayerStillRefusesTheShorter(t *testing.T) {
	km, _ := BuildLayered(
		map[ActionID]string{},
		map[ActionID]string{},
		map[ActionID]string{"pane.rename": "ctrl+b", "tab.new": "ctrl+b c"},
	)
	if _, ok := km.MatchTier(TierLate, "ctrl+b"); ok {
		t.Error("within one layer the shorter binding must be refused")
	}
	if _, kind := km.MatchSeq(mustChords(t, "ctrl+b c")); kind != MatchExact {
		t.Error("the longer sequence must survive within one layer")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: BuildLayered`.

- [ ] **Step 3: Implement it**

Add to `internal/keymap/keymap.go`:

```go
// BuildLayered resolves three binding layers and builds a Keymap, applying the
// cross-layer shadowing rule that Build cannot: Build sees one flat map and
// has no way to tell which layer a binding came from.
//
// Cross-layer, the HIGHER layer wins regardless of length. Intra-layer, the
// SHORTER binding is refused. The asymmetry is deliberate: a user override
// `pane.rename = "ctrl+b"` under the tmux preset must be able to reclaim the
// prefix key as a chord, and a length rule would let a preset veto an explicit
// user override. Length is safe within a layer precisely because no
// cross-layer inversion is possible there.
func BuildLayered(base, preset, user map[ActionID]string) (*Keymap, []Conflict) {
	// Drop any lower-layer binding whose head a higher layer claims, before
	// the flat Build ever sees the collision.
	pruned, dropped := pruneShadowedLayers(base, preset, user)
	km, conflicts := Build(pruned)
	return km, append(dropped, conflicts...)
}

// pruneShadowedLayers removes lower-layer bindings that collide, by head chord,
// with a higher-layer one. Returns the merged map plus a conflict per drop.
func pruneShadowedLayers(layers ...map[ActionID]string) (map[ActionID]string, []Conflict) {
	var conflicts []Conflict
	// heads maps a first chord to the highest layer index that claimed it.
	heads := map[string]int{}
	for i, layer := range layers {
		for _, spec := range layer {
			for _, seq := range parseQuietly(spec) {
				heads[seq[0].String()] = i
			}
		}
	}
	out := map[ActionID]string{}
	for i, layer := range layers {
		for id, spec := range layer {
			var keep []string
			for _, alt := range strings.Split(spec, ",") {
				seqs := parseQuietly(strings.TrimSpace(alt))
				if len(seqs) == 0 {
					continue
				}
				if owner, ok := heads[seqs[0][0].String()]; ok && owner > i {
					conflicts = append(conflicts, Conflict{
						Kind: ConflictShadowed, Key: seqs[0].String(), Loser: id,
						Detail: "a higher binding layer claims this key",
					})
					continue
				}
				keep = append(keep, strings.TrimSpace(alt))
			}
			// Drop granularity is PER-ALTERNATIVE: a binding that collides on
			// one alternative keeps the others rather than losing everything.
			out[id] = strings.Join(keep, ", ")
		}
	}
	return out, conflicts
}

// parseQuietly parses a spec, returning nil on any error. Build reports the
// malformed spec properly; this helper only needs the head chords.
func parseQuietly(spec string) []Sequence {
	seqs, err := ParseSpec(spec)
	if err != nil {
		return nil
	}
	return seqs
}
```

Add `"strings"` to the file's imports.

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/keymap.go internal/keymap/conflict_test.go
git commit -m "feat(keymap): resolve cross-layer binding collisions by layer"
```

---

### Task 18: Wire bindings.toml into the TUI

**Files:**
- Modify: `internal/tui/keyspecs.go`, `internal/tui/model.go` (`initKeymap`), `cmd/quil/main.go`
- Test: `internal/tui/keyspecs_test.go`

**Interfaces:**
- Produces: `func (m *Model) SetBindings(b config.Bindings)`.

**Keymap load stays out of `Model` construction.** ~46 tests build a `Model` directly without setting `QUIL_HOME`; a disk read in the constructor points every one of them at the real `~/.quil`. Follow the `SetRecentCWDs` precedent — a pure setter on the `Model`, loaded in `cmd/quil/main.go`.

This is also where `default.toml` gets pinned against the legacy defaults.

- [ ] **Step 1: Write the failing test**

```go
// Without this, a preset edit silently rebinds every existing user's keyboard.
func TestDefaultPreset_MatchesLegacyDefaults(t *testing.T) {
	preset, err := keymap.Preset("default")
	if err != nil {
		t.Fatalf("Preset(default): %v", err)
	}
	legacy := config.KeySpecsFromConfig(config.Default().Keybindings)

	for id, want := range legacy {
		got, ok := preset[id]
		if !ok {
			t.Errorf("preset default is missing %q", id)
			continue
		}
		// The paste aliases moved INTO the preset deliberately; they have no
		// legacy field to compare against.
		if id == "pane.paste" {
			continue
		}
		if got != want {
			t.Errorf("preset default %s = %q, legacy default = %q", id, got, want)
		}
	}
}

func TestSetBindings_AppliesPresetAndOverrides(t *testing.T) {
	m := newModelForTest(t)
	m.SetBindings(config.Bindings{
		Preset:    "default",
		Overrides: map[keymap.ActionID]string{"tab.new": "ctrl+shift+t"},
	})
	if got := m.keymap.Display("tab.new"); got != "ctrl+shift+t" {
		t.Errorf("Display(tab.new) = %q, want the override", got)
	}
	if got := m.keymap.Display("pane.close"); got != "ctrl+w" {
		t.Errorf("Display(pane.close) = %q, want the preset's value", got)
	}
}

// A Model built directly, with no bindings set, must still dispatch. ~46 tests
// depend on this.
func TestModel_WithoutBindingsStillDispatches(t *testing.T) {
	m := newModelForTest(t)
	if got := m.keymap.Display("tab.new"); got == "" {
		t.Error("a Model with no bindings applied must still carry the shipped defaults")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.SetBindings undefined`.

- [ ] **Step 3: Add SetBindings**

In `internal/tui/keyspecs.go`:

```go
// SetBindings applies a loaded bindings.toml. Pure — no disk access — because
// ~46 tests build a Model directly without QUIL_HOME, and a read here would
// point every one of them at the real ~/.quil. cmd/quil/main.go does the load,
// following the SetRecentCWDs precedent.
func (m *Model) SetBindings(b config.Bindings) {
	base, err := keymap.Preset("default")
	if err != nil {
		logger.Warn("keybindings: default preset unreadable: %v", err)
		base = map[keymap.ActionID]string{}
	}
	var chosen map[keymap.ActionID]string
	if b.Preset != "" && b.Preset != "default" {
		chosen, err = keymap.Preset(b.Preset)
		if err != nil {
			// A named preset that does not exist keeps the default rather than
			// leaving the user with no keymap at all.
			logger.Warn("keybindings: %v; keeping the default preset", err)
			chosen = nil
		}
	}
	if warning := keymap.PrefixWarning(b.Prefix); warning != "" {
		logger.Warn("keybindings: %s", warning)
	}

	// Expand ${prefix} in each layer SEPARATELY, before any collision
	// analysis. BuildLayered decides who wins by comparing head chords, and a
	// literal "${prefix}" is not a chord — expanding after the merge would have
	// it compare templates and find no collisions at all.
	var prefixConflicts []keymap.Conflict
	expand := func(layer map[keymap.ActionID]string) map[keymap.ActionID]string {
		out, cs := keymap.ExpandPrefix(layer, b.Prefix)
		prefixConflicts = append(prefixConflicts, cs...)
		return out
	}

	km, conflicts := keymap.BuildLayered(expand(base), expand(chosen), expand(b.Overrides))

	m.keymap = km
	m.keyConflicts = append(prefixConflicts, conflicts...)
	m.seqTimeout = b.SequenceTimeout
	for _, c := range m.keyConflicts {
		logger.Warn("keybindings: %s", c)
	}
}
```

`ExpandPrefix` must tolerate a nil map — `chosen` is nil whenever the default preset is selected. Add a `len(specs) == 0` early return to it in Task 11 if the range does not already handle that (ranging nil is safe, but the returned map should stay nil rather than becoming empty-non-nil, so `Resolve` and `BuildLayered` treat "no preset" and "empty preset" alike).

- [ ] **Step 4: Load it in main.go**

In `cmd/quil/main.go`, beside the existing `SetRecentCWDs` call:

```go
	cfg, cfgErr := config.Load(config.ConfigPath())
	if migrated, err := config.MigrateBindings(cfg, cfgErr); err != nil {
		log.Printf("bindings migration: %v", err)
	} else if migrated {
		log.Printf("bindings: migrated [keybindings] into %s", config.BindingsPath())
	}
	bindings, err := config.LoadBindings()
	if err != nil {
		log.Printf("bindings: %v; falling back to the default preset", err)
		bindings = config.Bindings{Preset: "default"}
	}
	model.SetBindings(bindings)
```

- [ ] **Step 5: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS, especially `TestDefaultPreset_MatchesLegacyDefaults`.

- [ ] **Step 6: Commit**

```bash
git add internal/tui cmd/quil/main.go
git commit -m "feat(tui): resolve keybindings from bindings.toml"
```

---

### Task 19: The tmux preset

**Files:**
- Create: `internal/keymap/presets/tmux.toml`
- Test: `internal/keymap/preset_test.go`

**Note the off-by-one:** tmux windows are 0-indexed, Quil tabs are 1-indexed and render a 1-based prefix. The preset binds `${prefix} 1`…`${prefix} 9` and leaves `${prefix} 0` **unbound** — mapping tmux's `0` to Quil's tab 1 would double-bind tab 1 and hide tab 9 from anyone counting from zero.

- [ ] **Step 1: Write the failing test**

```go
func TestPreset_TmuxResolvesWithoutConflicts(t *testing.T) {
	base, err := Preset("default")
	if err != nil {
		t.Fatalf("Preset(default): %v", err)
	}
	tmux, err := Preset("tmux")
	if err != nil {
		t.Fatalf("Preset(tmux): %v", err)
	}
	expandedBase, _ := ExpandPrefix(base, "ctrl+b")
	expandedTmux, c1 := ExpandPrefix(tmux, "ctrl+b")
	if len(c1) != 0 {
		t.Fatalf("tmux preset failed to expand: %v", c1)
	}
	_, conflicts := BuildLayered(expandedBase, expandedTmux, nil)
	for _, c := range conflicts {
		if c.Kind == ConflictMalformed || c.Kind == ConflictUnknownAction {
			t.Errorf("tmux preset conflict: %s", c)
		}
	}
}

// Every action must resolve to a binding or an explicit "" AFTER the merge —
// not that each preset file names all of them.
func TestEveryPreset_CoversEveryActionAfterMerge(t *testing.T) {
	base, _ := Preset("default")
	for _, name := range PresetNames() {
		layer, err := Preset(name)
		if err != nil {
			t.Fatalf("Preset(%s): %v", name, err)
		}
		merged := Resolve(base, layer)
		for _, a := range Actions() {
			if _, ok := merged[a.ID]; !ok {
				t.Errorf("preset %s: %q resolves to nothing after merge", name, a.ID)
			}
		}
	}
}

// tmux windows are 0-indexed, Quil tabs are 1-indexed. Binding ${prefix} 0 to
// tab 1 would double-bind it and hide tab 9 from anyone counting from zero.
func TestPreset_TmuxLeavesPrefixZeroUnbound(t *testing.T) {
	tmux, _ := Preset("tmux")
	for id, spec := range tmux {
		if spec == "${prefix} 0" {
			t.Errorf("%s binds ${prefix} 0; it must stay unbound", id)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `unknown preset "tmux"`.

- [ ] **Step 3: Write the preset**

Create `internal/keymap/presets/tmux.toml`:

```toml
# tmux-compatible keymap.
#
# Presets REPLACE, they do not add: every action named here loses its default
# chord. tab.new = "${prefix} c" means Ctrl+T no longer opens a tab. That is
# deliberate — a preset that keeps both key sets is neither keymap, and it
# doubles the conflict surface. A user who wants both writes the alternative in
# their own [bindings] table.
#
# Actions NOT named here inherit the default preset: Alt+Shift+P still opens the
# command palette, Alt+F2 still renames a pane, Alt+G still toggles lazygit.

preset = "tmux"
prefix = "ctrl+b"
sequence_timeout = "0"

[bindings]
"tab.new"           = "${prefix} c"
"tab.rename"        = "${prefix} ,"
"tab.close"         = "${prefix} &"
"tab.next"          = "${prefix} n"
"tab.prev"          = "${prefix} p"

"pane.split_h"      = "${prefix} %"
"pane.split_v"      = "${prefix} \""
"pane.close"        = "${prefix} x"
"pane.focus_toggle" = "${prefix} z"
"pane.next"         = "${prefix} o"
"pane.left"         = "${prefix} left"
"pane.right"        = "${prefix} right"
"pane.up"           = "${prefix} up"
"pane.down"         = "${prefix} down"
"pane.scroll_page_up" = "${prefix} ["

# tmux's detach. Quitting Quil leaves the daemon running, so the semantics
# match exactly.
"app.quit"          = "${prefix} d"
"system.shortcuts"  = "${prefix} ?"

# tmux windows are 0-indexed and Quil tabs are 1-indexed. ${prefix} 0 stays
# UNBOUND rather than aliasing tab 1 — that would double-bind tab 1 and hide
# tab 9 from anyone counting from zero.
"tab.switch_1"      = "${prefix} 1"
"tab.switch_2"      = "${prefix} 2"
"tab.switch_3"      = "${prefix} 3"
"tab.switch_4"      = "${prefix} 4"
"tab.switch_5"      = "${prefix} 5"
"tab.switch_6"      = "${prefix} 6"
"tab.switch_7"      = "${prefix} 7"
"tab.switch_8"      = "${prefix} 8"
"tab.switch_9"      = "${prefix} 9"
```

- [ ] **Step 4: Run the tests and verify they pass**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/presets/tmux.toml internal/keymap/preset_test.go
git commit -m "feat(keymap): add the tmux binding preset"
```

---

### Task 20: Docs, changelog fragment, and the follow-up debt entry

**Files:**
- Modify: `docs/keybindings.md`, `docs/configuration.md`, `.claude/rules/tui-rendering.md`
- Create: `changelog.d/added-keymap-sequences-presets.md`, `techdebt/3-1-strip-legacy-keybindings-table.md`

- [ ] **Step 1: Write the changelog fragment**

Create `changelog.d/added-keymap-sequences-presets.md`:

```markdown
### Added

- Multi-key binding sequences. A binding can now be several chords pressed in
  order — `new_tab = "ctrl+b c"` — with a status-bar indicator while a sequence
  is pending, `Esc` to cancel, and a doubled prefix to send one literal chord
  through to the pane.
- `~/.quil/bindings.toml` with selectable keymap presets, including a `tmux`
  preset. Existing `[keybindings]` tables migrate automatically on first launch;
  only settings that differ from the shipped defaults are carried over.
- Tab switching, next/previous tab, and the shortcuts dialog are now rebindable
  actions rather than fixed keys.
```

- [ ] **Step 2: Write the tech-debt entry**

Create `techdebt/3-1-strip-legacy-keybindings-table.md`:

```markdown
# Strip [keybindings] from config.toml on Save

**Criticality:** 3 (medium) — dead config, not a defect
**Complexity:** 1 (trivial) — a guard on one branch

## What

`config.Save` still writes the full `[keybindings]` table even though
`bindings.toml` is now the source of truth. Change it to strip the table **iff
`bindings.toml` exists on disk**.

## Why it was deferred

This is the second half of a deliberate two-release rollout, not an oversight.

`Mutate` is Load+fn+Save, so the first unrelated config mutation after upgrade
— a remote install writing `[remote.hosts]`, say — deletes whatever `Save`
stops emitting. Quil's auto-update has a real rollback path
(`internal/update/`, rename-aside swap). A rollback lands a binary that cannot
read `bindings.toml`; if the legacy table is already gone, that user's entire
keymap resets to defaults with no recovery.

Release N (the PR that added `bindings.toml`) keeps writing the legacy table so
the previous binary can still read it. This entry is release N+1.

## Guard

Strip only when `bindings.toml` exists. If it is missing — a failed write, or
the user deleted it to reset — the legacy table must survive as the migration
source.

`TestSave_StillEmitsLegacyKeybindings` pins the release-N behaviour; inverting
that test is the first step of this change.
```

- [ ] **Step 3: Update docs/keybindings.md**

Add three sections. Each answers a question the rules otherwise resolve only as a log warning:

- **Sequences and `${prefix}`** — the grammar, the pending indicator, `Esc` to cancel, and the doubled-prefix literal escape (with the ssh-into-tmux reason).
- **Presets replace, they do not add.** Selecting `tmux` removes `Ctrl+T` and `Ctrl+W`. Show the `[bindings]` override syntax for getting them back.
- **A prefix colliding with an inherited chord silently degrades.** `prefix = "ctrl+w"` under the tmux preset makes every preset sequence shadow whatever inherited default chord shares that head. Coherent under the cross-layer rule, invisible except in F1's warning rows. Say to check F1 after changing `prefix`.

Also update the **Windows paste** note: rebinding `pane.paste` to a single chord loses `F8`, and Windows Terminal never delivers `Ctrl+V`, so Windows users should keep an `F8` alternative.

- [ ] **Step 4: Update docs/configuration.md**

Document `bindings.toml`: `preset`, `prefix`, `sequence_timeout`, `[bindings]`. State that `[keybindings]` in `config.toml` is legacy, read once at migration, and no longer consulted.

- [ ] **Step 5: Update the scoped rule**

In `.claude/rules/tui-rendering.md`, extend the action-registry section: the two-map sequence tables, the tier-agnostic probe and why it must be, the one probe site plus the two named downstream inert modes, and the layering rule with its intra/cross asymmetry.

- [ ] **Step 6: Full verification**

```bash
./scripts/dev.sh vet
./scripts/dev.sh test ./internal/keymap/
./scripts/dev.sh test ./internal/tui/
./scripts/dev.sh test ./internal/config/
./scripts/dev.sh test-race ./internal/tui/
```

Expected: all PASS. Redirect `test-race` to a file and grep for `--- FAIL`, `DATA RACE` and `panic:` — the output is noisy enough to bury a failure line, and `tail` has hidden one before.

- [ ] **Step 7: Manual smoke test on the dev build**

```bash
./scripts/dev.sh build
```

Verify the binary mtimes actually moved — `build` refuses while a dev TUI holds an exe, and a refused build reads as "the feature is broken" on retest.

Then launch `./scripts/quil-dev.ps1`, confirm `[dev]` in the status bar, and check:

1. `Ctrl+B` then `c` opens a tab; the status bar shows `ctrl+b…` in between.
2. `Ctrl+B` twice sends a literal `^B` to the shell.
3. `Esc` mid-sequence cancels and the indicator clears.
4. F1 → Shortcuts lists the sequence bindings and any conflict rows.
5. Set `preset = "tmux"` in `.quil/bindings.toml`, relaunch, and confirm `Ctrl+B %` splits.

- [ ] **Step 8: Commit**

```bash
git add docs changelog.d techdebt .claude/rules/tui-rendering.md
git commit -m "docs(keymap): document key sequences and binding presets"
```

---

## Self-Review Notes

Checked against the spec:

- **Every spec section maps to a task.** Sequence tables → 1; shadowing → 2, 17; probe placement → 4; cancellation → 5; literal escape → 6; indicator → 7; timeout → 8; `kbMatches` retirement → 9; layering → 10, 18; `${prefix}` → 11; presets → 12, 19; 12 new actions → 13; paste aliases → 14; `bindings.toml` → 15; migration → 16; `Save` gating → 20 (the debt entry); docs → 20.
- **One addition the spec did not name:** Task 3, extracting `handleKey`'s two switches into methods. The sequence machine must *run* an action on an exact match, and the dispatch logic was inline in two `switch` blocks only `handleKey` could reach. Behaviour-preserving, and gated by the existing tier tests.
- **One spec correction:** the spec listed the reconnect/parked screen among the modes needing an explicit guard. It does not — `freezeInput` is called unconditionally at `model.go:1068` and returns `frozen`, so `handleKey` is never reached while parked. The inert set is two modes, not three.
