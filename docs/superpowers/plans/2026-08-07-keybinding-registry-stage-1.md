# Keybinding Registry (Stage 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace Quil's 41 keybinding struct fields and 48 scattered `kbMatches` call sites with a single action registry and a two-tier dispatch, preserving what every key does.

**Architecture:** A new leaf package `internal/keymap` owns canonical chords, a binding parser, an action registry, chord lookup tables, and conflict detection. It takes a `map[ActionID]string` and knows nothing about `config` or `tui`. The TUI builds that map from `config.KeybindingsConfig`, holds one `*Keymap`, and dispatches through two tier-scoped lookups that straddle `tryPluginRawKey` where today's two switches sit.

**Tech Stack:** Go 1.25, `charm.land/bubbletea/v2` (key strings), stdlib `testing`.

**Source spec:** `docs/superpowers/specs/2026-08-07-keybinding-presets-design.md` (revision 3). **Stage 1 only.** Stages 2 (prefix machine) and 3 (`bindings.toml` + presets) get their own plans — their TDD steps depend on APIs this stage defines.

**Revision 2** — adversarial plan review folded in: per-action fallback instead of whole-config, dispatch-order tie-break, nil-safe keymap, super/meta modifiers, honest F1 scope.

## Global Constraints

- Go files use **tabs**; TOML/JSON/YAML use 2 spaces. `gofmt` is mandatory.
- `internal/keymap` imports **stdlib only**. No `internal/config`, no `internal/tui` — that keeps it testable without `QUIL_HOME`.
- Build and test via Docker: `./scripts/dev.sh test ./internal/keymap/`. Go is **not** installed on the host.
- **`dev.sh test` consumes only its first argument** (`scripts/dev.sh:126`). `-run` and `-v` are silently dropped — every command in this plan is therefore package-level. Do not add flags expecting them to work.
- **Never** run `./quil` without `--dev`; never touch `~/.quil/`. See `.claude/rules/dev-environment.md`.
- Exported identifiers use `MixedCaps`; acronyms stay uppercase (`ID`, `TUI`).
- Commit messages: imperative mood, ≤72 chars first line, no AI attribution.

### Scope of behaviour change

Two different promises, and conflating them is how this plan went wrong the first time:

- **Dispatch is behaviour-preserving.** Every key must resolve to the same action, in the same precedence order, as before. Tasks 1–8 are covered by tests asserting exactly that.
- **F1 → Shortcuts changes visibly, on purpose.** The spec says so directly: *"Stage 1 is not zero-visible-change… Budget for pseudo-entries."* Task 9 regroups the list, adds conflict warning rows, and renders bindings in canonical spelling. Its acceptance list is in the task, not a claim of no change.

One further deliberate change, called out because it is silent otherwise: a key bound to an action **beats** Quil's built-in `f1` / `ctrl+n` / `alt+1..9` interception where today the built-in wins for actions dispatched after `model.go:3702`. Conflict rule 3 warns on exactly these, so the user sees it. See Task 7 Step 6.

## File Structure

**Created:** `internal/keymap/{chord,parse,action,keymap,conflict}.go` + tests; `internal/tui/keyspecs.go` + test; `internal/tui/keydispatch_test.go`; `internal/tui/keyreaders_test.go`.

**Modified:** `internal/tui/{keymatch,model,dialog,ctxmenu,overlay,palette,reconnect}.go`, `internal/tui/shortcuts_scroll_test.go`, `CHANGELOG.md`.

---

### Task 1: Canonical chords

**Files:** Create `internal/keymap/chord.go`, `internal/keymap/chord_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type Chord struct{ Mods Mod; Key string }`, `ParseChord(string) (Chord, error)`, `(Chord) String() string`, `ModCtrl/ModAlt/ModShift/ModSuper Mod`.

Matching today is exact-string against Bubble Tea's `key.String()`. Without a canonical form, `"ctrl+shift+a"` and `"shift+ctrl+a"` are different keys and conflict detection misses the collision.

**`ModSuper` is not optional.** `kbMatches` is a raw string compare, so a macOS user with `quit = "super+q"` works today. A parser that rejects `super` turns a working binding into a parse error.

- [ ] **Step 1: Write the failing test**

```go
package keymap

import "testing"

func TestParseChord_Canonicalizes(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"plain key", "a", "a"},
		{"single mod", "ctrl+a", "ctrl+a"},
		{"mod order normalized", "shift+ctrl+a", "ctrl+shift+a"},
		{"all mods", "shift+alt+ctrl+f2", "ctrl+alt+shift+f2"},
		{"super preserved", "super+q", "super+q"},
		{"meta folds to super", "meta+q", "super+q"},
		{"uppercase folded", "Ctrl+A", "ctrl+a"},
		{"escape alias", "escape", "esc"},
		{"pageup alias", "pageup", "pgup"},
		{"pgdn alias", "pgdn", "pgdown"},
		{"return alias", "return", "enter"},
		{"space key", "space", "space"},
		{"tab key", "tab", "tab"},
		{"function key", "f12", "f12"},
		{"literal plus", "alt++", "alt++"},
		{"whitespace trimmed", "  ctrl+a  ", "ctrl+a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseChord(tt.input)
			if err != nil {
				t.Fatalf("ParseChord(%q) error: %v", tt.input, err)
			}
			if got := c.String(); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseChord_Rejects(t *testing.T) {
	for _, in := range []string{"", "   ", "ctrl+", "ctrl+alt"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseChord(in); err == nil {
				t.Errorf("ParseChord(%q) = nil error, want error", in)
			}
		})
	}
}

func TestChord_RoundTripsBubbleTea(t *testing.T) {
	// Every chord shipped in config.Default().Keybindings. If the canonical
	// modifier order disagrees with bubbletea's rendering, these stop matching
	// real key presses and the affected bindings go silently dead.
	shipped := []string{
		"ctrl+q", "ctrl+t", "ctrl+w", "alt+w", "alt+shift+h", "alt+shift+v",
		"alt+left", "alt+right", "alt+up", "alt+down", "f2", "alt+f2",
		"alt+shift+r", "alt+c", "alt+pgup", "alt+pgdown", "ctrl+v", "ctrl+j",
		"alt+a", "ctrl+e", "alt+n", "f3", "alt+m", "alt+r", "alt+backspace",
		"alt+e", "alt+shift+l", "alt+shift+e", "alt+shift+i", "alt+g",
		"alt+shift+w", "alt+shift+p", "alt+shift+s", "alt+p", "alt+o",
		"alt+shift+right", "alt+shift+left", "alt+shift+a", "alt+shift+n",
		"alt+shift+x",
	}
	for _, s := range shipped {
		t.Run(s, func(t *testing.T) {
			c, err := ParseChord(s)
			if err != nil {
				t.Fatalf("ParseChord(%q) error: %v", s, err)
			}
			if got := c.String(); got != s {
				t.Errorf("round trip = %q — canonical form disagrees with the shipped default, so this binding would never match", got)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: ParseChord`

- [ ] **Step 3: Write the implementation**

```go
// Package keymap owns Quil's key-to-action mapping: canonical chords, the
// action registry, and the lookup tables the TUI dispatches through.
//
// It imports stdlib only. Keeping config and tui out means the whole package
// is testable without QUIL_HOME and without building a Model.
package keymap

import (
	"fmt"
	"strings"
)

// Mod is a bitmask of chord modifiers.
type Mod uint8

const (
	ModCtrl Mod = 1 << iota
	ModAlt
	ModShift
	// ModSuper is Cmd on macOS, Win on Windows. kbMatches was an exact string
	// compare, so a config saying "super+q" works today — rejecting it here
	// would break a working binding.
	ModSuper
)

// Chord is one key press: zero or more modifiers plus a base key.
type Chord struct {
	Mods Mod
	Key  string
}

// modNames folds modifier spellings onto one bit. hyper and meta both appear
// in terminal documentation for what bubbletea reports as super.
var modNames = map[string]Mod{
	"ctrl": ModCtrl, "alt": ModAlt, "shift": ModShift,
	"super": ModSuper, "meta": ModSuper, "hyper": ModSuper,
}

// keyAliases folds spellings that mean the same key. The codebase already
// carries both "escape" (rename path) and "esc" (selection path).
var keyAliases = map[string]string{
	"escape": "esc", "pageup": "pgup", "pagedown": "pgdown",
	"pgdn": "pgdown", "return": "enter",
}

// ParseChord parses a chord in any accepted spelling and returns its canonical
// form. Input modifier order is irrelevant; String renders a fixed order.
func ParseChord(s string) (Chord, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return Chord{}, fmt.Errorf("empty chord")
	}
	var c Chord
	rest := s
	for {
		plus := strings.Index(rest, "+")
		// No separator left, or the separator IS the base key ("alt++"):
		// whatever remains is the base key.
		if plus < 0 || plus == len(rest)-1 {
			break
		}
		name := rest[:plus]
		mod, ok := modNames[name]
		if !ok {
			// A non-modifier before a "+" means the base key sits in a
			// modifier position, e.g. "a+ctrl".
			return Chord{}, fmt.Errorf("chord %q: %q is not a modifier", s, name)
		}
		c.Mods |= mod
		rest = rest[plus+1:]
	}
	if rest == "" {
		return Chord{}, fmt.Errorf("chord %q ends with a modifier", s)
	}
	if _, isMod := modNames[rest]; isMod {
		return Chord{}, fmt.Errorf("chord %q has no base key", s)
	}
	if alias, ok := keyAliases[rest]; ok {
		rest = alias
	}
	c.Key = rest
	return c, nil
}

// String renders the canonical form: modifiers in fixed ctrl/alt/shift/super
// order, lowercase. This must match bubbletea's KeyPressMsg.String(), or a
// configured chord can never match a real press — TestChord_RoundTripsBubbleTea
// pins that against every shipped default.
func (c Chord) String() string {
	var b strings.Builder
	for _, m := range []struct {
		bit  Mod
		name string
	}{{ModCtrl, "ctrl+"}, {ModAlt, "alt+"}, {ModShift, "shift+"}, {ModSuper, "super+"}} {
		if c.Mods&m.bit != 0 {
			b.WriteString(m.name)
		}
	}
	b.WriteString(c.Key)
	return b.String()
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS, including all 40 round-trip subtests. If a round-trip subtest fails, the modifier order in `String()` is wrong — fix `String()`, never the test data.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/chord.go internal/keymap/chord_test.go
git commit -m "feat(keymap): add canonical chord parsing"
```

---

### Task 2: Binding spec parser

**Files:** Create `internal/keymap/parse.go`, `internal/keymap/parse_test.go`

**Interfaces:**
- Consumes: `Chord`, `ParseChord` (Task 1).
- Produces: `type Sequence []Chord`, `ParseSpec(string) ([]Sequence, error)`, `(Sequence) String() string`.

Grammar: **comma** separates alternatives, **space** separates sequence steps. Whitespace around an alternative is cosmetic and tolerated — the shipped default `"alt+f2,alt+shift+r"` (`config.go:355`) has no space after the comma, and a hand-edited TOML line can easily have one before it. Only an *internal* empty step (`"ctrl+b  c"`, double space) is malformed.

- [ ] **Step 1: Write the failing test**

```go
package keymap

import "testing"

func TestParseSpec(t *testing.T) {
	tests := []struct {
		name, input string
		want        []string
	}{
		{"single chord", "ctrl+q", []string{"ctrl+q"}},
		{"two alternatives", "alt+f2,alt+shift+r", []string{"alt+f2", "alt+shift+r"}},
		{"alternatives spaced", "alt+f2, alt+shift+r", []string{"alt+f2", "alt+shift+r"}},
		{"trailing space tolerated", "ctrl+b ", []string{"ctrl+b"}},
		{"sequence", "ctrl+b c", []string{"ctrl+b c"}},
		{"sequence and chord", "ctrl+b c, ctrl+t", []string{"ctrl+b c", "ctrl+t"}},
		{"three steps", "ctrl+b x y", []string{"ctrl+b x y"}},
		{"canonicalizes members", "shift+ctrl+a", []string{"ctrl+shift+a"}},
		{"empty means unbound", "", nil},
		{"whitespace only unbound", "   ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSpec(tt.input)
			if err != nil {
				t.Fatalf("ParseSpec(%q) error: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("= %d sequences, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if s := got[i].String(); s != tt.want[i] {
					t.Errorf("sequence %d = %q, want %q", i, s, tt.want[i])
				}
			}
		})
	}
}

func TestParseSpec_Rejects(t *testing.T) {
	for _, in := range []string{"ctrl+", "a,,b", ",a", "ctrl+b  c"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseSpec(in); err == nil {
				t.Errorf("ParseSpec(%q) = nil error, want error", in)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: ParseSpec`

- [ ] **Step 3: Write the implementation**

```go
package keymap

import (
	"fmt"
	"strings"
)

// Sequence is one binding: a chord, or several chords pressed in order.
type Sequence []Chord

// String renders the sequence space-separated, each chord canonical.
func (s Sequence) String() string {
	parts := make([]string, len(s))
	for i, c := range s {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

// ParseSpec parses a binding spec into its alternatives.
//
//	"ctrl+b c, ctrl+t"  ->  [seq(ctrl+b, c), seq(ctrl+t)]
//
// An empty or whitespace-only spec means deliberately unbound and returns
// (nil, nil) — next_pane and prev_pane ship that way.
func ParseSpec(spec string) ([]Sequence, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	alts := strings.Split(spec, ",")
	out := make([]Sequence, 0, len(alts))
	for _, alt := range alts {
		// Space AROUND an alternative is cosmetic; space INSIDE separates
		// sequence steps. An internal empty step (double space) is malformed
		// and is caught by the loop below.
		alt = strings.Trim(alt, " \t")
		if alt == "" {
			return nil, fmt.Errorf("spec %q has an empty alternative", spec)
		}
		steps := strings.Split(alt, " ")
		seq := make(Sequence, 0, len(steps))
		for _, step := range steps {
			if step == "" {
				return nil, fmt.Errorf("spec %q has an empty sequence step", spec)
			}
			c, err := ParseChord(step)
			if err != nil {
				return nil, fmt.Errorf("spec %q: %w", spec, err)
			}
			seq = append(seq, c)
		}
		out = append(out, seq)
	}
	return out, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/parse.go internal/keymap/parse_test.go
git commit -m "feat(keymap): parse binding specs into sequences"
```

---

### Task 3: Action registry

**Files:** Create `internal/keymap/action.go`, `internal/keymap/action_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type ActionID string`, `Tier` (`TierEarly`/`TierLate`), `type Action struct{ ID ActionID; Label, Group string; Tier Tier; Order int; Default string; Hidden bool }`, `Actions() []Action`, `Lookup(ActionID) (Action, bool)`, `ActionsByGroup()`, `GroupOrder`.

Field-by-field, because every one is load-bearing:

- **`Tier`** — copied from today's `handleKey`. `TierEarly` = the switch at `model.go:3410-3525`; `TierLate` = `3581-3734`. `tryPluginRawKey` sits between them at `3562`, so a plugin `raw_keys` entry loses to `pane.mute` (early) and wins against `pane.restart` (late).
- **`Order`** — the action's rank in today's case order. Used as the duplicate-conflict tie-break so an existing config with an accidental duplicate keeps firing the same action it fires now. Also orders F1 rows within a group.
- **`Default`** — the shipped spec. A malformed user spec falls back to *this action's* default rather than discarding the whole config (see Task 4).
- **`Hidden`** — kept out of F1. Only `json.transform` sets it: `ctrl+j` is a config field from M5's unfinished JSON transformer with **no dispatch site anywhere**. It stays registered so a user's value survives and conflict detection sees the key it claims, but advertising a key that does nothing is worse than omitting it.

- [ ] **Step 1: Write the failing test**

```go
package keymap

import "testing"

func TestActions_RegistryIntegrity(t *testing.T) {
	acts := Actions()
	if len(acts) != 41 {
		t.Fatalf("registry has %d actions, want 41 (one per KeybindingsConfig field)", len(acts))
	}
	seen := make(map[ActionID]bool, len(acts))
	orders := make(map[int]ActionID, len(acts))
	for _, a := range acts {
		if a.ID == "" || a.Label == "" || a.Group == "" {
			t.Errorf("action %+v has an empty ID, Label or Group", a)
		}
		if seen[a.ID] {
			t.Errorf("duplicate action ID %q", a.ID)
		}
		seen[a.ID] = true
		if prev, dup := orders[a.Order]; dup {
			t.Errorf("actions %q and %q share Order %d — the duplicate-conflict tie-break would be nondeterministic", prev, a.ID, a.Order)
		}
		orders[a.Order] = a.ID
		if _, err := ParseSpec(a.Default); err != nil {
			t.Errorf("action %q has an unparseable Default %q: %v", a.ID, a.Default, err)
		}
	}
}

func TestActions_TierSplitMatchesLegacySwitches(t *testing.T) {
	// Pinned against handleKey before the rewrite: the early switch
	// (model.go:3410-3525) ran before tryPluginRawKey (3562), the late switch
	// (3581-3734) after. Moving an action between tiers changes whether a
	// plugin's raw_keys entry beats it.
	early := map[ActionID]bool{
		"notification.toggle": true, "notification.focus": true,
		"sidebar.toggle": true, "pane.go_back": true, "pane.mute": true,
		"pane.toggle_eager": true, "pane.toggle_wrap": true,
		"pane.toggle_lazygit": true, "pane.command_history": true,
		"pane.quick_actions": true, "project.new": true,
		"project.destroy": true, "project.picker": true,
		"project.next": true, "project.prev": true,
		"project.toggle": true, "project.attention_queue": true,
	}
	if len(early) != 17 {
		t.Fatalf("expected-early table has %d entries, want 17", len(early))
	}
	for _, a := range Actions() {
		want := TierLate
		if early[a.ID] {
			want = TierEarly
		}
		if a.Tier != want {
			t.Errorf("action %q tier = %v, want %v", a.ID, a.Tier, want)
		}
	}
}

func TestActionsByGroup_IsDeterministic(t *testing.T) {
	groups, byGroup := ActionsByGroup()
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	var total int
	for _, g := range groups {
		bucket := byGroup[g]
		total += len(bucket)
		for i := 1; i < len(bucket); i++ {
			if bucket[i-1].Order > bucket[i].Order {
				t.Errorf("group %q is not sorted by Order", g)
			}
		}
	}
	if total != len(Actions()) {
		t.Errorf("grouping covers %d actions, want %d", total, len(Actions()))
	}
}

func TestLookup(t *testing.T) {
	a, ok := Lookup("pane.split_h")
	if !ok || a.Tier != TierLate {
		t.Errorf("Lookup(pane.split_h) = (%+v, %v)", a, ok)
	}
	if _, ok := Lookup("nope.nope"); ok {
		t.Error("Lookup(nope.nope) found something")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: Actions`

- [ ] **Step 3: Write the implementation**

`Order` values mirror the legacy case order: early actions 100–1700 in switch order, late actions 2000–4300 in switch order. Gaps of 100 leave room to insert without renumbering.

```go
package keymap

import "sort"

// ActionID is the stable, data-addressable name of one bindable action.
// Presets and bindings.toml key off these strings — never rename a shipped one.
type ActionID string

// Tier says which of handleKey's two switches an action dispatches from.
// The split matters because tryPluginRawKey runs between them: an early action
// beats a plugin's raw_keys claim, a late action loses to it.
type Tier uint8

const (
	TierEarly Tier = iota
	TierLate
)

// Action is one registry entry.
type Action struct {
	ID      ActionID
	Label   string // shown in F1 and the command palette
	Group   string // F1 section heading
	Tier    Tier
	Order   int    // legacy case rank: duplicate tie-break + F1 row order
	Default string // shipped spec; the per-action fallback for a bad config
	Hidden  bool   // omitted from F1 (registered but not dispatched)
}

var registry = []Action{
	// --- Early tier: model.go:3410-3525, before tryPluginRawKey ---
	{ID: "notification.toggle", Label: "Toggle notification sidebar", Group: "Notifications", Tier: TierEarly, Order: 100, Default: "alt+n"},
	{ID: "sidebar.toggle", Label: "Toggle project sidebar", Group: "Projects", Tier: TierEarly, Order: 200, Default: "alt+shift+s"},
	{ID: "notification.focus", Label: "Focus notification sidebar", Group: "Notifications", Tier: TierEarly, Order: 300, Default: "f3"},
	{ID: "pane.go_back", Label: "Pane history back", Group: "Panes", Tier: TierEarly, Order: 400, Default: "alt+backspace"},
	{ID: "pane.mute", Label: "Mute / unmute pane notifications", Group: "Panes", Tier: TierEarly, Order: 500, Default: "alt+m"},
	{ID: "pane.toggle_eager", Label: "Toggle eager restore (active pane)", Group: "Panes", Tier: TierEarly, Order: 600, Default: "alt+shift+e"},
	{ID: "pane.toggle_wrap", Label: "Toggle preview soft-wrap (AI pane)", Group: "Panes", Tier: TierEarly, Order: 700, Default: "alt+shift+w"},
	{ID: "pane.toggle_lazygit", Label: "Toggle lazygit overlay for current repo", Group: "Panes", Tier: TierEarly, Order: 800, Default: "alt+g"},
	{ID: "pane.command_history", Label: "Open pane input history", Group: "Panes", Tier: TierEarly, Order: 900, Default: "alt+shift+i"},
	{ID: "pane.quick_actions", Label: "Pane context menu (also mouse right-click)", Group: "Panes", Tier: TierEarly, Order: 1000, Default: "alt+a"},
	{ID: "project.new", Label: "New project", Group: "Projects", Tier: TierEarly, Order: 1100, Default: "alt+shift+n"},
	{ID: "project.destroy", Label: "Remove active project (destroy / disconnect)", Group: "Projects", Tier: TierEarly, Order: 1200, Default: "alt+shift+x"},
	{ID: "project.picker", Label: "Project picker (fuzzy-find by name)", Group: "Projects", Tier: TierEarly, Order: 1300, Default: "alt+p"},
	{ID: "project.next", Label: "Next project", Group: "Projects", Tier: TierEarly, Order: 1400, Default: "alt+shift+right"},
	{ID: "project.prev", Label: "Previous project", Group: "Projects", Tier: TierEarly, Order: 1500, Default: "alt+shift+left"},
	{ID: "project.toggle", Label: "Bounce to the previous project", Group: "Projects", Tier: TierEarly, Order: 1600, Default: "alt+o"},
	{ID: "project.attention_queue", Label: "Jump to the agent blocked longest", Group: "Projects", Tier: TierEarly, Order: 1700, Default: "alt+shift+a"},

	// --- Late tier: model.go:3581-3734, after tryPluginRawKey ---
	{ID: "app.quit", Label: "Quit", Group: "System", Tier: TierLate, Order: 2000, Default: "ctrl+q"},
	{ID: "tab.new", Label: "New tab", Group: "Tabs", Tier: TierLate, Order: 2100, Default: "ctrl+t"},
	{ID: "pane.close", Label: "Close pane", Group: "Panes", Tier: TierLate, Order: 2200, Default: "ctrl+w"},
	{ID: "pane.restart", Label: "Restart pane process (sessions resume)", Group: "Panes", Tier: TierLate, Order: 2300, Default: "alt+r"},
	{ID: "tab.close", Label: "Close tab", Group: "Tabs", Tier: TierLate, Order: 2400, Default: "alt+w"},
	{ID: "pane.split_h", Label: "Split side-by-side", Group: "Panes", Tier: TierLate, Order: 2500, Default: "alt+shift+h"},
	{ID: "pane.split_v", Label: "Split top/bottom", Group: "Panes", Tier: TierLate, Order: 2600, Default: "alt+shift+v"},
	{ID: "tab.rename", Label: "Rename tab", Group: "Tabs", Tier: TierLate, Order: 2700, Default: "f2"},
	{ID: "pane.rename", Label: "Rename pane", Group: "Panes", Tier: TierLate, Order: 2800, Default: "alt+f2,alt+shift+r"},
	{ID: "tab.cycle_color", Label: "Cycle tab color", Group: "Tabs", Tier: TierLate, Order: 2900, Default: "alt+c"},
	{ID: "app.redraw", Label: "Force screen redraw", Group: "System", Tier: TierLate, Order: 3000, Default: "alt+shift+l"},
	{ID: "pane.scroll_page_up", Label: "Scroll page up", Group: "Panes", Tier: TierLate, Order: 3100, Default: "alt+pgup"},
	{ID: "pane.scroll_page_down", Label: "Scroll page down", Group: "Panes", Tier: TierLate, Order: 3200, Default: "alt+pgdown"},
	{ID: "pane.next", Label: "Next pane", Group: "Pane navigation", Tier: TierLate, Order: 3300, Default: ""},
	{ID: "pane.prev", Label: "Previous pane", Group: "Pane navigation", Tier: TierLate, Order: 3400, Default: ""},
	{ID: "pane.left", Label: "Focus pane left", Group: "Pane navigation", Tier: TierLate, Order: 3500, Default: "alt+left"},
	{ID: "pane.right", Label: "Focus pane right", Group: "Pane navigation", Tier: TierLate, Order: 3600, Default: "alt+right"},
	{ID: "pane.up", Label: "Focus pane up", Group: "Pane navigation", Tier: TierLate, Order: 3700, Default: "alt+up"},
	{ID: "pane.down", Label: "Focus pane down", Group: "Pane navigation", Tier: TierLate, Order: 3800, Default: "alt+down"},
	{ID: "pane.paste", Label: "Paste clipboard", Group: "Panes", Tier: TierLate, Order: 3900, Default: "ctrl+v"},
	{ID: "pane.focus_toggle", Label: "Toggle focus mode", Group: "Panes", Tier: TierLate, Order: 4000, Default: "ctrl+e"},
	{ID: "pane.notes_toggle", Label: "Toggle pane notes", Group: "Panes", Tier: TierLate, Order: 4100, Default: "alt+e"},
	{ID: "app.command_palette", Label: "Command palette (fuzzy-find any action)", Group: "System", Tier: TierLate, Order: 4200, Default: "alt+shift+p"},

	// No dispatch site anywhere: ctrl+j is a config field left over from M5's
	// unfinished JSON transformer. Registered so a configured value survives
	// and conflict detection sees the key; Hidden so F1 does not advertise a
	// shortcut that does nothing.
	{ID: "json.transform", Label: "Transform selection as JSON", Group: "System", Tier: TierLate, Order: 4300, Default: "ctrl+j", Hidden: true},
}

// Actions returns every registered action. The slice is a copy.
func Actions() []Action {
	out := make([]Action, len(registry))
	copy(out, registry)
	return out
}

var byID = func() map[ActionID]Action {
	m := make(map[ActionID]Action, len(registry))
	for _, a := range registry {
		m[a.ID] = a
	}
	return m
}()

// Lookup returns the action with the given ID.
func Lookup(id ActionID) (Action, bool) {
	a, ok := byID[id]
	return a, ok
}

// GroupOrder is the order F1 renders groups in. A group missing here sorts
// last, alphabetically — a new group shows up rather than vanishing.
var GroupOrder = []string{
	"System", "Projects", "Tabs", "Panes", "Pane navigation", "Notifications",
}

// ActionsByGroup buckets actions by Group, each bucket sorted by Order, with
// groups in GroupOrder. Hidden actions are included; callers filter.
func ActionsByGroup() (groups []string, byGroup map[string][]Action) {
	byGroup = make(map[string][]Action)
	for _, a := range registry {
		byGroup[a.Group] = append(byGroup[a.Group], a)
	}
	for g := range byGroup {
		bucket := byGroup[g]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Order < bucket[j].Order })
		byGroup[g] = bucket
	}
	seen := make(map[string]bool, len(GroupOrder))
	for _, g := range GroupOrder {
		if _, ok := byGroup[g]; ok {
			groups = append(groups, g)
			seen[g] = true
		}
	}
	var rest []string
	for g := range byGroup {
		if !seen[g] {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	return append(groups, rest...), byGroup
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS. `TestActions_RegistryIntegrity` also validates every `Default` parses — if one fails, the shipped default is malformed.

- [ ] **Step 5: Commit**

```bash
git add internal/keymap/action.go internal/keymap/action_test.go
git commit -m "feat(keymap): add the action registry with dispatch tiers"
```

---

### Task 4: Keymap build and tier-scoped matching

**Files:** Create `internal/keymap/keymap.go`, `internal/keymap/conflict.go`, `internal/keymap/keymap_test.go`

**Interfaces:**
- Consumes: `ParseSpec` (Task 2); `ActionID`, `Tier`, `Lookup`, `Actions` (Task 3).
- Produces: `type Keymap`, `Build(map[ActionID]string) (*Keymap, []Conflict)`, `(*Keymap) MatchTier(Tier, string) (ActionID, bool)`, `Bindings`, `Display`, `Keys`; `type Conflict`, `ConflictKind`.

Three properties this task must get right, each of which was a bug in the first draft:

1. **`Build` never returns an error.** A malformed spec for one action falls back to **that action's `Default`** and reports a `ConflictMalformed`. One bad line in `config.toml` must not discard the other 40 bindings.
2. **The duplicate tie-break is `Order`, not map iteration or ID sort.** Today's winner is decided by case order; `Order` mirrors it. An ID sort would flip real pairs — `tab.rename` (2700) vs `app.redraw` (3000) on one key resolves to `tab.rename` today, but `app.redraw` under an alphabetical sort.
3. **Every method is nil-receiver-safe.** ~26 TUI test files build `Model{...}` literals with no `cfg` and no keymap, then drive `handleKey`. A nil deref there turns the whole suite red.

- [ ] **Step 1: Write the failing test**

```go
package keymap

import "testing"

func TestBuild_MatchesByTier(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{
		"pane.mute": "alt+m", "pane.close": "ctrl+w",
	})
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if id, ok := km.MatchTier(TierEarly, "alt+m"); !ok || id != "pane.mute" {
		t.Errorf("early alt+m = (%q,%v)", id, ok)
	}
	// The early tier must NOT see a late action, or a plugin raw_keys entry
	// that should beat it gets bypassed.
	if _, ok := km.MatchTier(TierEarly, "ctrl+w"); ok {
		t.Error("early tier matched a late action")
	}
	if id, ok := km.MatchTier(TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Errorf("late ctrl+w = (%q,%v)", id, ok)
	}
}

func TestMatchTier_CanonicalizesInput(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.close": "ctrl+shift+w"})
	if _, ok := km.MatchTier(TierLate, "shift+ctrl+w"); !ok {
		t.Error("MatchTier did not canonicalize its input")
	}
}

func TestBuild_MultiBindingAlternatives(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.rename": "alt+f2,alt+shift+r"})
	for _, key := range []string{"alt+f2", "alt+shift+r"} {
		if id, ok := km.MatchTier(TierLate, key); !ok || id != "pane.rename" {
			t.Errorf("late %q = (%q,%v)", key, id, ok)
		}
	}
}

func TestBuild_MultiStepSequenceNeverMatchesItsFirstChord(t *testing.T) {
	// Stage 1 has no prefix machine. A multi-step binding parses and stores
	// but must never fire off its opening chord alone.
	km, _ := Build(map[ActionID]string{"tab.new": "ctrl+b c"})
	if _, ok := km.MatchTier(TierLate, "ctrl+b"); ok {
		t.Error("a multi-step sequence matched on its first chord")
	}
}

func TestBuild_MalformedSpecFallsBackPerAction(t *testing.T) {
	km, conflicts := Build(map[ActionID]string{
		"app.quit":   "super+", // malformed
		"pane.close": "ctrl+w", // must survive untouched
	})
	var malformed bool
	for _, c := range conflicts {
		if c.Kind == ConflictMalformed && c.Loser == "app.quit" {
			malformed = true
		}
	}
	if !malformed {
		t.Errorf("no ConflictMalformed for app.quit: %+v", conflicts)
	}
	// app.quit falls back to ITS default, not to nothing.
	if id, ok := km.MatchTier(TierLate, "ctrl+q"); !ok || id != "app.quit" {
		t.Error("app.quit did not fall back to its default binding")
	}
	// The unrelated binding is untouched — this is the whole point.
	if id, ok := km.MatchTier(TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Error("a malformed spec discarded an unrelated binding")
	}
}

func TestBuild_DuplicateWinnerFollowsLegacyCaseOrder(t *testing.T) {
	// tab.rename (Order 2700) precedes app.redraw (Order 3000) in the legacy
	// switch, so tab.rename must win. An alphabetical ID sort flips this.
	km, conflicts := Build(map[ActionID]string{
		"tab.rename": "ctrl+y", "app.redraw": "ctrl+y",
	})
	if id, _ := km.MatchTier(TierLate, "ctrl+y"); id != "tab.rename" {
		t.Errorf("ctrl+y = %q, want tab.rename (lower Order wins)", id)
	}
	if len(conflicts) != 1 || conflicts[0].Winner != "tab.rename" || conflicts[0].Loser != "app.redraw" {
		t.Errorf("conflicts = %+v", conflicts)
	}
}

func TestKeymap_NilReceiverIsSafe(t *testing.T) {
	// ~26 TUI test files build Model literals with no cfg and drive handleKey.
	// Every accessor must answer "nothing" rather than panic.
	var km *Keymap
	if _, ok := km.MatchTier(TierLate, "ctrl+q"); ok {
		t.Error("nil MatchTier reported a match")
	}
	if km.Bindings("app.quit") != nil {
		t.Error("nil Bindings returned non-nil")
	}
	if km.Display("app.quit") != "" {
		t.Error("nil Display returned non-empty")
	}
	if km.Keys("app.quit") != nil {
		t.Error("nil Keys returned non-nil")
	}
}

func TestDisplayAndKeys(t *testing.T) {
	km, _ := Build(map[ActionID]string{"pane.rename": "alt+f2,alt+shift+r"})
	if got, want := km.Display("pane.rename"), "alt+f2 / alt+shift+r"; got != want {
		t.Errorf("Display = %q, want %q", got, want)
	}
	if got := km.Keys("pane.rename"); len(got) != 2 || got[0] != "alt+f2" {
		t.Errorf("Keys = %v", got)
	}
	if km.Display("pane.next") != "" {
		t.Error("unbound action has a display string")
	}
}

func TestBuild_UnknownActionIDIsIgnoredWithAConflict(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{"nope.nope": "ctrl+z"})
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictUnknownAction {
		t.Errorf("conflicts = %+v, want one ConflictUnknownAction", conflicts)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: FAIL — `undefined: Build`

- [ ] **Step 3: Write `conflict.go`**

```go
package keymap

import "fmt"

// ConflictKind classifies a binding problem found at build time.
type ConflictKind uint8

const (
	// ConflictDuplicate: two actions claim one chord in one tier.
	ConflictDuplicate ConflictKind = iota
	// ConflictCrossTier: one chord is claimed in both tiers. The early action
	// always wins at runtime, so the late one can never fire.
	ConflictCrossTier
	// ConflictHardcoded: the chord collides with a key handleKey intercepts
	// outside the registry.
	ConflictHardcoded
	// ConflictMalformed: the spec did not parse; the action fell back to its
	// shipped default.
	ConflictMalformed
	// ConflictUnknownAction: a spec named an ID that is not registered.
	ConflictUnknownAction
)

func (k ConflictKind) String() string {
	switch k {
	case ConflictDuplicate:
		return "duplicate binding"
	case ConflictCrossTier:
		return "cross-tier shadowing"
	case ConflictHardcoded:
		return "collides with a built-in key"
	case ConflictMalformed:
		return "unreadable binding"
	case ConflictUnknownAction:
		return "unknown action"
	}
	return "unknown conflict"
}

// Conflict is one binding problem. Conflicts never block startup; they are
// surfaced in F1 -> Shortcuts and the log, because a silently dropped binding
// is worse than a loud one.
type Conflict struct {
	Kind   ConflictKind
	Key    string
	Winner ActionID
	Loser  ActionID
	Detail string
}

func (c Conflict) String() string {
	switch c.Kind {
	case ConflictHardcoded:
		return fmt.Sprintf("%s: %q is bound to %s but Quil intercepts it first", c.Kind, c.Key, c.Loser)
	case ConflictMalformed:
		return fmt.Sprintf("%s: %s — %s; using the default %q", c.Kind, c.Loser, c.Detail, c.Key)
	case ConflictUnknownAction:
		return fmt.Sprintf("%s: %s is not a known action; ignored", c.Kind, c.Loser)
	}
	return fmt.Sprintf("%s: %q resolves to %s, so %s will never fire", c.Kind, c.Key, c.Winner, c.Loser)
}

// hardcodedKeys are intercepted by handleKey outside the registry.
// f1 / ctrl+n: model.go:3702-3708. alt+1..alt+9: model.go:3713-3718.
// ctrl+alt+v / f8: paste aliases, model.go:3684.
var hardcodedKeys = func() map[string]bool {
	m := map[string]bool{"f1": true, "ctrl+n": true, "ctrl+alt+v": true, "f8": true}
	for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		m["alt+"+d] = true
	}
	return m
}()
```

- [ ] **Step 4: Write `keymap.go`**

```go
package keymap

import (
	"sort"
	"strings"
)

// Keymap is a resolved, immutable key-to-action mapping.
type Keymap struct {
	// chords maps tier -> canonical chord -> action. Only single-chord
	// bindings land here; multi-step sequences live in bindings and are
	// matched by the prefix machine (Stage 2), never as a bare chord.
	chords   map[Tier]map[string]ActionID
	bindings map[ActionID][]Sequence
}

// Build resolves a spec map into a Keymap.
//
// It never fails. A malformed spec falls back to that action's shipped default
// and reports a ConflictMalformed; an unknown ID is ignored with a conflict.
// One bad line in config.toml must not cost the user their other 40 bindings.
func Build(specs map[ActionID]string) (*Keymap, []Conflict) {
	km := &Keymap{
		chords:   map[Tier]map[string]ActionID{TierEarly: {}, TierLate: {}},
		bindings: make(map[ActionID][]Sequence, len(specs)),
	}
	var conflicts []Conflict

	// Resolve in registry Order so the duplicate tie-break reproduces today's
	// case order. Map iteration is randomized and an ID sort flips real pairs.
	resolved := make([]Action, 0, len(specs))
	for id := range specs {
		a, ok := Lookup(id)
		if !ok {
			conflicts = append(conflicts, Conflict{Kind: ConflictUnknownAction, Loser: id})
			continue
		}
		resolved = append(resolved, a)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Order < resolved[j].Order })

	for _, a := range resolved {
		seqs, err := ParseSpec(specs[a.ID])
		if err != nil {
			seqs, _ = ParseSpec(a.Default) // validated by TestActions_RegistryIntegrity
			conflicts = append(conflicts, Conflict{
				Kind: ConflictMalformed, Key: a.Default, Loser: a.ID, Detail: err.Error(),
			})
		}
		if len(seqs) == 0 {
			continue
		}
		km.bindings[a.ID] = seqs
		for _, seq := range seqs {
			if len(seq) != 1 {
				continue // multi-step: Stage 2 owns these
			}
			key := seq[0].String()
			if prev, taken := km.chords[a.Tier][key]; taken {
				conflicts = append(conflicts, Conflict{
					Kind: ConflictDuplicate, Key: key, Winner: prev, Loser: a.ID,
				})
				continue // lower Order already claimed it: legacy case order
			}
			km.chords[a.Tier][key] = a.ID
		}
	}
	conflicts = append(conflicts, km.detectShadowing()...)
	return km, conflicts
}

// detectShadowing reports chords claimed in both tiers, and chords colliding
// with Quil's hardcoded keys.
func (k *Keymap) detectShadowing() []Conflict {
	var out []Conflict
	for key, lateID := range k.chords[TierLate] {
		if earlyID, ok := k.chords[TierEarly][key]; ok {
			out = append(out, Conflict{Kind: ConflictCrossTier, Key: key, Winner: earlyID, Loser: lateID})
		}
	}
	for _, tier := range []Tier{TierEarly, TierLate} {
		for key, id := range k.chords[tier] {
			if hardcodedKeys[key] {
				out = append(out, Conflict{Kind: ConflictHardcoded, Key: key, Loser: id})
			}
		}
	}
	// Deterministic order: these render in F1, and a list that reshuffles
	// between launches is unreadable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Loser < out[j].Loser
	})
	return out
}

// MatchTier resolves a raw key string against one tier. The key is
// canonicalized here, so callers pass tea.KeyPressMsg.String() directly.
//
// Nil-safe: TUI tests build Model literals with no keymap and drive handleKey.
func (k *Keymap) MatchTier(t Tier, key string) (ActionID, bool) {
	if k == nil {
		return "", false
	}
	c, err := ParseChord(key)
	if err != nil {
		return "", false
	}
	id, ok := k.chords[t][c.String()]
	return id, ok
}

// Bindings returns the sequences bound to an action, or nil.
func (k *Keymap) Bindings(id ActionID) []Sequence {
	if k == nil {
		return nil
	}
	return k.bindings[id]
}

// Keys returns each binding as a canonical string, for callers that need the
// individual keys rather than one display line.
func (k *Keymap) Keys(id ActionID) []string {
	if k == nil {
		return nil
	}
	seqs := k.bindings[id]
	if len(seqs) == 0 {
		return nil
	}
	out := make([]string, len(seqs))
	for i, s := range seqs {
		out[i] = s.String()
	}
	return out
}

// Display renders an action's bindings for help text, joined with " / " —
// the format kbDisplay produced before the registry existed.
func (k *Keymap) Display(id ActionID) string {
	return strings.Join(k.Keys(id), " / ")
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/keymap/keymap.go internal/keymap/conflict.go internal/keymap/keymap_test.go
git commit -m "feat(keymap): resolve specs into tier-scoped chord lookup"
```

---

### Task 5: Conflict detection tests

**Files:** Create `internal/keymap/conflict_test.go`

**Interfaces:** Consumes everything from Task 4. Produces nothing.

Detection already ships in Task 4 — `Build` and `detectShadowing` implement it. This task is the test suite that proves each rule, kept separate so a reviewer can reject the rules without rejecting the resolver.

Prefix-shadowing (spec conflict rule 2) needs multi-step bindings and lands in Stage 2.

- [ ] **Step 1: Write the tests**

```go
package keymap

import (
	"strings"
	"testing"
)

func TestBuild_CrossTierIsReported(t *testing.T) {
	_, conflicts := Build(map[ActionID]string{
		"pane.mute":  "alt+z", // early
		"pane.close": "alt+z", // late — unreachable, early always wins
	})
	var found *Conflict
	for i := range conflicts {
		if conflicts[i].Kind == ConflictCrossTier {
			found = &conflicts[i]
		}
	}
	if found == nil {
		t.Fatalf("no cross-tier conflict: %+v", conflicts)
	}
	if found.Winner != "pane.mute" || found.Loser != "pane.close" {
		t.Errorf("winner/loser = %q/%q", found.Winner, found.Loser)
	}
}

func TestBuild_HardcodedCollision(t *testing.T) {
	// f8 and ctrl+alt+v are included: they are paste aliases handled outside
	// the registry, so binding another action to them silently loses.
	for _, key := range []string{"f1", "ctrl+n", "alt+1", "alt+9", "f8", "ctrl+alt+v"} {
		t.Run(key, func(t *testing.T) {
			_, conflicts := Build(map[ActionID]string{"pane.close": key})
			var found bool
			for _, c := range conflicts {
				if c.Kind == ConflictHardcoded && c.Key == key {
					found = true
				}
			}
			if !found {
				t.Errorf("binding %q produced no hardcoded conflict: %+v", key, conflicts)
			}
		})
	}
}

func TestBuild_NoFalsePositives(t *testing.T) {
	// alt+0 is NOT intercepted — only alt+1..alt+9 are.
	_, conflicts := Build(map[ActionID]string{"pane.close": "alt+0"})
	if len(conflicts) != 0 {
		t.Errorf("alt+0 reported a conflict: %+v", conflicts)
	}
}

func TestBuild_ShippedDefaultsAreClean(t *testing.T) {
	specs := make(map[ActionID]string)
	for _, a := range Actions() {
		specs[a.ID] = a.Default
	}
	if _, conflicts := Build(specs); len(conflicts) != 0 {
		t.Errorf("shipped defaults conflict: %+v", conflicts)
	}
}

func TestConflict_StringIsActionable(t *testing.T) {
	c := Conflict{Kind: ConflictDuplicate, Key: "ctrl+w", Winner: "pane.close", Loser: "tab.close"}
	got := c.String()
	for _, want := range []string{"ctrl+w", "pane.close", "tab.close"} {
		if !strings.Contains(got, want) {
			t.Errorf("Conflict.String() = %q, missing %q", got, want)
		}
	}
}
```

- [ ] **Step 2: Run**

Run: `./scripts/dev.sh test ./internal/keymap/`
Expected: PASS. If `TestBuild_ShippedDefaultsAreClean` fails, a `Default` in the registry collides with another — fix the registry, not the test.

- [ ] **Step 3: Commit**

```bash
git add internal/keymap/conflict_test.go
git commit -m "test(keymap): cover every conflict rule"
```

---

### Task 6: Config-to-registry adapter

**Files:** Create `internal/tui/keyspecs.go`, `internal/tui/keyspecs_test.go`

**Interfaces:**
- Consumes: `keymap.Build`, `keymap.ActionID` (Tasks 3-4).
- Produces: `keySpecsFromConfig(config.KeybindingsConfig) map[keymap.ActionID]string`, `buildKeymap(config.KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict)`, `(*Model) isAction(string, keymap.ActionID) bool`.

This file is the **only** place the struct-field ↔ action-ID correspondence lives. Stage 3 replaces its body with a `bindings.toml` read and nothing else in the TUI changes.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

func TestKeySpecsFromConfig_MapsEveryAction(t *testing.T) {
	specs := keySpecsFromConfig(config.Default().Keybindings)
	if len(specs) != len(keymap.Actions()) {
		t.Fatalf("mapped %d specs, want %d", len(specs), len(keymap.Actions()))
	}
	for _, a := range keymap.Actions() {
		if _, ok := specs[a.ID]; !ok {
			t.Errorf("action %q has no config field mapping", a.ID)
		}
	}
}

func TestKeySpecsFromConfig_MatchesRegistryDefaults(t *testing.T) {
	// The registry's Default column and config.Default() must agree, or the
	// per-action fallback restores a binding the user never had.
	specs := keySpecsFromConfig(config.Default().Keybindings)
	for _, a := range keymap.Actions() {
		if specs[a.ID] != a.Default {
			t.Errorf("action %q: config default %q != registry Default %q",
				a.ID, specs[a.ID], a.Default)
		}
	}
}

func TestBuildKeymap_DefaultsAreConflictFree(t *testing.T) {
	km, conflicts := buildKeymap(config.Default().Keybindings)
	if km == nil {
		t.Fatal("buildKeymap returned nil")
	}
	if len(conflicts) != 0 {
		t.Errorf("shipped defaults have conflicts: %+v", conflicts)
	}
}

func TestBuildKeymap_DefaultsResolveToLegacyActions(t *testing.T) {
	km, _ := buildKeymap(config.Default().Keybindings)
	cases := []struct {
		key  string
		tier keymap.Tier
		want keymap.ActionID
	}{
		{"ctrl+q", keymap.TierLate, "app.quit"},
		{"ctrl+t", keymap.TierLate, "tab.new"},
		{"ctrl+w", keymap.TierLate, "pane.close"},
		{"alt+w", keymap.TierLate, "tab.close"},
		{"alt+m", keymap.TierEarly, "pane.mute"},
		{"alt+n", keymap.TierEarly, "notification.toggle"},
		{"alt+p", keymap.TierEarly, "project.picker"},
		{"alt+shift+h", keymap.TierLate, "pane.split_h"},
		{"alt+f2", keymap.TierLate, "pane.rename"},
		{"alt+shift+r", keymap.TierLate, "pane.rename"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got, ok := km.MatchTier(c.tier, c.key)
			if !ok || got != c.want {
				t.Errorf("= (%q,%v), want (%q,true)", got, ok, c.want)
			}
		})
	}
}

func TestBuildKeymap_MalformedFieldKeepsOtherBindings(t *testing.T) {
	kb := config.Default().Keybindings
	kb.Quit = "ctrl+" // malformed
	km, conflicts := buildKeymap(kb)
	if len(conflicts) == 0 {
		t.Error("malformed spec produced no conflict")
	}
	if id, ok := km.MatchTier(keymap.TierLate, "ctrl+q"); !ok || id != "app.quit" {
		t.Error("app.quit did not fall back to its default")
	}
	if id, ok := km.MatchTier(keymap.TierLate, "ctrl+w"); !ok || id != "pane.close" {
		t.Error("an unrelated binding was discarded")
	}
}

func TestIsAction_SearchesBothTiersAndIsNilSafe(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.initKeymap()
	if !m.isAction("alt+m", "pane.mute") {
		t.Error("isAction missed an early action")
	}
	if !m.isAction("ctrl+w", "pane.close") {
		t.Error("isAction missed a late action")
	}
	if m.isAction("ctrl+x", "pane.close") {
		t.Error("isAction matched an unrelated key")
	}
	var nilM Model
	if nilM.isAction("ctrl+w", "pane.close") {
		t.Error("isAction on a keymap-less Model reported a match")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `undefined: keySpecsFromConfig`

- [ ] **Step 3: Write the implementation**

```go
package tui

import (
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

// keySpecsFromConfig is the ONE place legacy KeybindingsConfig field names
// correspond to registry action IDs. Stage 3 replaces this body with a
// bindings.toml read; no other TUI code changes when it does.
func keySpecsFromConfig(kb config.KeybindingsConfig) map[keymap.ActionID]string {
	return map[keymap.ActionID]string{
		"app.quit":                kb.Quit,
		"app.redraw":              kb.Redraw,
		"app.command_palette":     kb.CommandPalette,
		"tab.new":                 kb.NewTab,
		"tab.close":               kb.CloseTab,
		"tab.rename":              kb.RenameTab,
		"tab.cycle_color":         kb.CycleTabColor,
		"pane.close":              kb.ClosePane,
		"pane.restart":            kb.RestartPane,
		"pane.rename":             kb.RenamePane,
		"pane.split_h":            kb.SplitHorizontal,
		"pane.split_v":            kb.SplitVertical,
		"pane.focus_toggle":       kb.FocusPane,
		"pane.notes_toggle":       kb.NotesToggle,
		"pane.paste":              kb.Paste,
		"pane.scroll_page_up":     kb.ScrollPageUp,
		"pane.scroll_page_down":   kb.ScrollPageDown,
		"pane.left":               kb.PaneLeft,
		"pane.right":              kb.PaneRight,
		"pane.up":                 kb.PaneUp,
		"pane.down":               kb.PaneDown,
		"pane.next":               kb.NextPane,
		"pane.prev":               kb.PrevPane,
		"pane.go_back":            kb.GoBack,
		"pane.mute":               kb.MutePane,
		"pane.toggle_eager":       kb.ToggleEager,
		"pane.toggle_wrap":        kb.ToggleWrap,
		"pane.toggle_lazygit":     kb.ToggleLazygit,
		"pane.command_history":    kb.CommandHistory,
		"pane.quick_actions":      kb.QuickActions,
		"notification.toggle":     kb.NotificationToggle,
		"notification.focus":      kb.NotificationFocus,
		"sidebar.toggle":          kb.SidebarToggle,
		"project.new":             kb.NewProject,
		"project.destroy":         kb.DestroyProject,
		"project.picker":          kb.ProjectPicker,
		"project.next":            kb.ProjectNext,
		"project.prev":            kb.ProjectPrev,
		"project.toggle":          kb.ProjectToggle,
		"project.attention_queue": kb.AttentionQueue,
		"json.transform":          kb.JSONTransform,
	}
}

// buildKeymap resolves a config into the dispatch keymap. Build handles a
// malformed spec per-action, so there is no whole-config fallback to do here.
func buildKeymap(kb config.KeybindingsConfig) (*keymap.Keymap, []keymap.Conflict) {
	km, conflicts := keymap.Build(keySpecsFromConfig(kb))
	for _, c := range conflicts {
		logger.Warnf("keybindings: %s", c)
	}
	return km, conflicts
}

// isAction reports whether key is bound to the given action, searching both
// tiers. Used by the modal surfaces (dialogs, context menu, lazygit overlay,
// reconnect screen) that sit outside handleKey's tier split.
func (m *Model) isAction(key string, id keymap.ActionID) bool {
	for _, tier := range []keymap.Tier{keymap.TierEarly, keymap.TierLate} {
		if got, ok := m.keymap.MatchTier(tier, key); ok && got == id {
			return true
		}
	}
	return false
}
```

`logger` is the package's leveled logger — check its exact name and method set at `internal/tui/model.go:3321` before writing this line; if the package uses `log.Printf` in the surrounding code, match that instead. Do not mix the two.

- [ ] **Step 4: Run to verify it passes**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL on `m.initKeymap` / `m.keymap` being undefined — those land in Task 7. Everything else in this file must compile. If you prefer a green commit here, add the `Model` fields and `initKeymap` from Task 7 Step 3 now and move that step's checkbox.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/keyspecs.go internal/tui/keyspecs_test.go
git commit -m "feat(tui): map keybinding config fields onto registry actions"
```

---

### Task 7: Two-tier dispatch in `handleKey`

**Files:** Modify `internal/tui/model.go`, `internal/tui/keymatch.go`; create `internal/tui/keydispatch_test.go`

**Interfaces:**
- Consumes: `buildKeymap` (Task 6).
- Produces: `Model.keymap *keymap.Keymap`, `Model.keyConflicts []keymap.Conflict`, `(*Model) initKeymap()`.

The two switches become two lookups in the **same positions**. Nothing crosses `tryPluginRawKey` (`model.go:3562`), and the three interleaved interceptors stay put: sidebar-focused routing (`3528`), selection enter/esc (`3533-3556`), `isSelectionExtendKey` (`3577`).

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/keymap"
)

func TestModel_KeymapIsBuiltFromConfig(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.initKeymap()
	if m.keymap == nil {
		t.Fatal("initKeymap left m.keymap nil")
	}
	if id, ok := m.keymap.MatchTier(keymap.TierLate, "ctrl+q"); !ok || id != "app.quit" {
		t.Errorf("= (%q,%v), want (app.quit,true)", id, ok)
	}
}

func TestModel_TierLookupRespectsRawKeySeam(t *testing.T) {
	m := Model{cfg: config.Default()}
	m.initKeymap()
	// alt+m is pane.mute, EARLY: visible before tryPluginRawKey, so a plugin
	// declaring raw_keys = ["alt+m"] cannot steal it.
	if _, ok := m.keymap.MatchTier(keymap.TierEarly, "alt+m"); !ok {
		t.Error("pane.mute is not visible to the early tier")
	}
	// alt+r is pane.restart, LATE: must NOT be visible early, so a plugin
	// declaring raw_keys = ["alt+r"] still wins, as it does today.
	if _, ok := m.keymap.MatchTier(keymap.TierEarly, "alt+r"); ok {
		t.Error("pane.restart leaked into the early tier")
	}
	if _, ok := m.keymap.MatchTier(keymap.TierLate, "alt+r"); !ok {
		t.Error("pane.restart is not visible to the late tier")
	}
}

func TestKbMatches_StillWorksForNotesPath(t *testing.T) {
	if !kbMatches("alt+f2", "alt+f2,alt+shift+r") {
		t.Error("kbMatches lost multi-binding support")
	}
	if kbMatches("alt+x", "alt+f2,alt+shift+r") || kbMatches("", "alt+f2") || kbMatches("alt+f2", "") {
		t.Error("kbMatches matched something it should not")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — `m.keymap undefined`

- [ ] **Step 3: Add the Model fields and initializer**

In the `Model` struct in `internal/tui/model.go`:

```go
	// keymap resolves key presses to registry actions; keyConflicts is
	// surfaced in F1 -> Shortcuts.
	keymap       *keymap.Keymap
	keyConflicts []keymap.Conflict
```

Beside it:

```go
// initKeymap resolves the configured bindings into the dispatch keymap. Safe
// to call repeatedly; the last call wins.
func (m *Model) initKeymap() {
	m.keymap, m.keyConflicts = buildKeymap(m.cfg.Keybindings)
}
```

Call it from `NewModel` (`model.go:723`) immediately after `cfg` is assigned. Add the `keymap` import.

- [ ] **Step 4: Run the three tests**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS for the three new tests. Existing tests unaffected — `Keymap`'s methods are nil-safe, so Model literals without `cfg` still work.

- [ ] **Step 5: Replace the early switch**

At `model.go:3410`, the switch head `switch { case kbMatches(key, kb.NotificationToggle): ... }` becomes:

```go
	if actionID, ok := m.keymap.MatchTier(keymap.TierEarly, key); ok {
		switch actionID {
		case "notification.toggle":
			// ... existing body, verbatim ...
		case "sidebar.toggle":
			// ... existing body, verbatim ...
		// ... one case per early action ...
		}
	}
```

**Copy every body verbatim; only the case expression changes.** One body needs surgery: `project.next` and `project.prev` shared a case arm at `3471` with an inner `if kbMatches(key, kb.ProjectPrev)` at `3482`. Split into two cases — the inner `if` becomes the case distinction. Check for bare `break` and preserve any fallthrough.

- [ ] **Step 6: Replace the late switch and pin the post-lookup order**

The late switch (`3581-3734`) is **not** a clean block: `f1`/`ctrl+n` sit at `3702-3708`, `app.command_palette` at `3710`, `alt+1..9` at `3713-3718`, and the PTY-forward default at `3720-3733`. Lay it out explicitly:

```go
	if actionID, ok := m.keymap.MatchTier(keymap.TierLate, key); ok {
		switch actionID {
		case "app.quit":
			// ... existing body, verbatim ...
		// ... one case per late action, EXCLUDING json.transform ...
		}
	}

	// Paste aliases. These sit AFTER the tier lookup so every action defined
	// before the old paste case (model.go:3684) keeps beating them — notably
	// pane.restart at 3591, so `restart_pane = "f8"` still restarts.
	if key == "ctrl+alt+v" || key == "f8" {
		// ... body copied from the old paste case at 3684 ...
	}

	// Built-ins: f1 / ctrl+n (3702-3708), then alt+1..9 (3713-3718).
	// ... existing bodies, verbatim ...

	// PTY forward default (3720-3733) — unchanged, still last.
```

**This reorders two things, deliberately.** `app.command_palette` (`3710`) used to sit between `f1`/`ctrl+n` and `alt+1..9`; it now resolves in the tier lookup, ahead of both. And a late action bound to `f8`/`ctrl+alt+v` now beats the paste alias where actions after `3684` did not. Both cases are reported by `ConflictHardcoded` — that is why `f8`, `ctrl+alt+v`, `f1`, `ctrl+n`, and `alt+1..9` are all in `hardcodedKeys`.

`json.transform` gets **no case** — it has no handler today and adding one would be new behaviour.

- [ ] **Step 7: Mark `kbMatches` deprecated and vet**

`internal/tui/keymatch.go` keeps its implementation — `notesKeyExempt` still uses it and Stage 2 rewrites that. Add:

```go
// DEPRECATED for dispatch: handleKey resolves actions through Model.keymap.
// This remains only for notesKeyExempt, which Stage 2 replaces with the prefix
// state machine. Do not add new call sites.
```

Run: `./scripts/dev.sh vet`
Expected: clean.

- [ ] **Step 8: Add the behavioural raw-keys precedence test**

`TestModel_TierLookupRespectsRawKeySeam` calls `MatchTier` directly, so it can pass against a tier split `handleKey` never consults. The tier design is a *dispatch* property and needs a test that drives `Update`.

`internal/tui/input_order_test.go` already has the fixtures: `inputOrderTestModel(t, paneID, withChannel)` returns `(*Model, *fakeSender)` and wires `m.inputCh` when `withChannel` is true; `drainQueued(t, m, n)` pops enqueued input without blocking. **`withChannel` must be true** — `enqueueInput` blocks forever on a nil channel.

Two things that fixture does not give you, and both must be built here: a plugin registry entry whose `RawKeys` contains the test keys, wired onto the Model, and an assertion for "the action ran". Read `tryPluginRawKey` (`model.go:5671`) for how it resolves the active pane's plugin, and `internal/plugin/registry.go:592` for the `RawKeys` shape. No built-in plugin declares `raw_keys`, so the entry is hand-built.

```go
func TestUpdate_RawKeysLoseToEarlyActionsAndBeatLateOnes(t *testing.T) {
	// pane.mute (alt+m) is TierEarly: the action must win, nothing reaches
	// the PTY. pane.restart (alt+r) is TierLate: the plugin must win, bytes
	// reach the PTY and no confirm dialog opens. If a refactor collapses the
	// two switches, exactly one of these flips.
	tests := []struct {
		name       string
		key        string
		pluginWins bool
	}{
		{"early action beats raw_keys", "alt+m", false},
		{"late action loses to raw_keys", "alt+r", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := inputOrderTestModel(t, "pane-1", true)
			m.cfg = config.Default()
			m.initKeymap()
			givePaneRawKeys(t, m, "pane-1", []string{"alt+m", "alt+r"})

			_, _ = m.Update(keyPress(tt.key))

			var forwarded bool
			select {
			case <-m.inputCh:
				forwarded = true
			default:
			}
			if forwarded != tt.pluginWins {
				t.Errorf("key %q forwarded to PTY = %v, want %v", tt.key, forwarded, tt.pluginWins)
			}
		})
	}
}
```

`givePaneRawKeys` and `keyPress` are the two helpers to write. For `keyPress`, copy the `tea.KeyPressMsg` construction another test in the package already uses — the v2 `Code`/`Mod` field shape is easy to get wrong from memory.

- [ ] **Step 9: Run the full TUI suite**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS. Every pre-existing keybinding test must pass untouched — that is the dispatch-preservation evidence.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/model.go internal/tui/keymatch.go internal/tui/keydispatch_test.go
git commit -m "refactor(tui): dispatch keys through the action registry"
```

---

### Task 8: Migrate the remaining config readers

**Files:** Modify `internal/tui/dialog.go` (362, 668, 2033), `ctxmenu.go:484`, `overlay.go:251`, `palette.go:286`, `reconnect.go:403`; create `internal/tui/keyreaders_test.go`

**Interfaces:** Consumes `isAction` (Task 6), `Keymap.Keys`/`Display` (Task 4).

Any site still reading `m.cfg.Keybindings` keeps seeing config values after Stage 3 moves the source — so a user who rebinds quit finds it dead precisely in the overlay and reconnect states.

**This also fixes a live bug.** `dialog.go:668` and `:2033` are `case key == m.cfg.Keybindings.Paste:` — a raw string compare. With the shipped multi-binding syntax (`paste = "ctrl+v,f8"`) the whole configured string is compared against one key, never matches, and paste is silently dead in the Settings field editor and the instance form.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

func TestDialogPaste_HonoursMultiBinding(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.Paste = "ctrl+v,f8"
	m := Model{cfg: cfg}
	m.initKeymap()
	for _, key := range []string{"ctrl+v", "f8"} {
		t.Run(key, func(t *testing.T) {
			if !m.isAction(key, "pane.paste") {
				t.Errorf("isAction(%q, pane.paste) = false — the raw == compare is still there", key)
			}
		})
	}
	if m.isAction("ctrl+x", "pane.paste") {
		t.Error("isAction matched an unrelated key")
	}
}

func TestKeymapKeys_ForReconnectScreen(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.Quit = "ctrl+q,super+q"
	m := Model{cfg: cfg}
	m.initKeymap()
	keys := m.keymap.Keys("app.quit")
	if len(keys) != 2 {
		t.Fatalf("Keys(app.quit) = %v, want 2 entries", keys)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: FAIL — the `==` compare means `isAction` is not yet consulted by those dialogs, and `Keys` may not be reachable.

- [ ] **Step 3: Fix the two paste bugs**

At `dialog.go:668` and `dialog.go:2033`, replace `case key == m.cfg.Keybindings.Paste:` with `case m.isAction(key, "pane.paste"):`.

- [ ] **Step 4: Migrate the remaining readers**

- `ctxmenu.go:484` — `kbMatches(key, kb.Quit)` at `:500` becomes `m.isAction(key, "app.quit")`; drop the `kb` local.
- `overlay.go:251` — the checks at `:256/261/266` become `m.isAction(key, "pane.toggle_lazygit")`, `m.isAction(key, "app.quit")`, `m.isAction(key, "app.redraw")`; drop `kb`.
- `palette.go:286` — each `kbDisplay(kb.X)` becomes `m.keymap.Display("<action.id>")`; drop `kb`.
- `reconnect.go:403` — `isFreezeEscape(msg.String(), m.cfg.Keybindings.Quit)` passes a raw **spec string** it parses internally. Do not hand it `Display`'s output (`" / "`-joined; no parser expects that). Change `isFreezeEscape` to take `[]string` and call it as `isFreezeEscape(msg.String(), m.keymap.Keys("app.quit"))`; its body becomes a slice scan instead of a comma split.

Leave `reconnectResumeKey` (`reconnect.go:504`) alone — it is the hardcoded const `"r"`, not a config spec.

- [ ] **Step 5: Verify no stragglers**

```bash
grep -rn "cfg.Keybindings" internal/tui/ --include=*.go | grep -v _test.go | grep -v keyspecs.go
```

Expected exactly three sites, all deliberate and all removed in Stage 2:
- `model.go` — the `initKeymap` call
- `model.go:3315` — `kb := m.cfg.Keybindings`, feeding the notes block at `3354-3378`
- `model.go:3066` — `notesKeyExempt`

Anything else is a missed reader.

- [ ] **Step 6: Run the full TUI suite**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS

- [ ] **Step 7: Add the changelog entry**

Under `## [Unreleased]` in `CHANGELOG.md`:

```markdown
### Fixed

- Paste now works in the Settings field editor and the instance form when
  `paste` is configured with multiple keys (e.g. `paste = "ctrl+v,f8"`). Those
  two dialogs compared the whole configured string against a single key, so any
  multi-key paste binding was silently dead there.
```

- [ ] **Step 8: Commit**

```bash
git add internal/tui/dialog.go internal/tui/ctxmenu.go internal/tui/overlay.go \
        internal/tui/palette.go internal/tui/reconnect.go \
        internal/tui/keyreaders_test.go CHANGELOG.md
git commit -m "fix(tui): honour multi-key paste bindings in dialogs"
```

---

### Task 9: Derive the shortcuts list from the registry

**Files:** Modify `internal/tui/dialog.go:361-440`, `internal/tui/shortcuts_scroll_test.go`

**Interfaces:** Consumes `keymap.ActionsByGroup`, `Keymap.Display`, `Model.keyConflicts`.

**This task changes F1 visibly, on purpose.** Accept these four:

1. Rows regroup under `GroupOrder` headings and sort by `Order` within a group.
2. Conflict warning rows appear at the top when any exist.
3. Bindings render canonically — a user's `"Ctrl+V"` shows as `ctrl+v`.
4. `json.transform` does **not** appear (it is `Hidden`; `ctrl+j` does nothing).

What must **not** change: every non-action row the current list carries. The current function has rows for `Ctrl+N`, `Alt+1..9`, `F1`, right-click copy, `Esc` clear, `Ctrl+←/→` and `Ctrl+Alt+←/→` word jumps, and the editor's `Ctrl+X/V/A/Enter` (`dialog.go:416-427, 430-437`). Those are not registry actions and must be carried into the literal tail verbatim.

- [ ] **Step 1: Read the current function and inventory its non-action rows**

```bash
sed -n '361,440p' internal/tui/dialog.go
```

Write down every row that is **not** a `kbDisplay(kb.X)` line. That list is the literal tail for Step 4 — the code block there is a skeleton, and dropping a row an existing user relies on is the failure mode of this task.

- [ ] **Step 2: Write the failing test**

Add to `internal/tui/shortcuts_scroll_test.go`:

```go
func TestShortcutsList_CoversEveryBoundVisibleAction(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	descs := make(map[string]bool)
	for _, r := range shortcutsList(m) {
		descs[r.desc] = true
	}
	for _, a := range keymap.Actions() {
		if a.Hidden || m.keymap.Display(a.ID) == "" {
			continue // hidden, or deliberately unbound (pane.next/prev)
		}
		if !descs[a.Label] {
			t.Errorf("action %q (%s) is bound but missing from F1", a.ID, a.Label)
		}
	}
}

func TestShortcutsList_OmitsHiddenActions(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	for _, r := range shortcutsList(m) {
		if strings.Contains(r.desc, "Transform selection as JSON") {
			t.Error("F1 advertises json.transform, which has no handler")
		}
	}
}

func TestShortcutsList_RendersConflictsFirst(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.CloseTab = cfg.Keybindings.ClosePane // force a duplicate
	m := &Model{cfg: cfg, width: 120, height: 40}
	m.initKeymap()
	if len(m.keyConflicts) == 0 {
		t.Fatal("test setup produced no conflict")
	}
	rows := shortcutsList(m)
	if len(rows) == 0 || rows[0].key != "!" {
		t.Errorf("first row = %+v, want a conflict warning", rows[0])
	}
}

func TestShortcutsList_KeepsNonActionRows(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 40}
	m.initKeymap()
	joined := ""
	for _, r := range shortcutsList(m) {
		joined += r.key + "|" + r.desc + "\n"
	}
	// Rows with no registry action behind them. Each exists in the pre-registry
	// list; dropping one silently removes documentation users rely on.
	for _, want := range []string{"ctrl+n", "alt+1", "f1", "right-click"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Errorf("F1 lost the %q row", want)
		}
	}
}
```

Ensure `strings` and `keymap` are imported in that file.

- [ ] **Step 3: Make the three geometry tests build a real keymap**

`TestShortcutsDialog_FitsTheTerminal` (`:20`), `_FitsANarrowTerminal` (`:42`), and `_ScrollReachesTheEnd` (`:56`) build `Model{width: 100, height: height, dialog: dialogShortcuts}` with **no `cfg`**. With a nil keymap every action row is skipped and they would measure a near-empty list — passing for the wrong reason.

Add `cfg: config.Default()` to each literal and call `m.initKeymap()` before `m.renderDialog()`. These are `Model` values, not pointers, so use `(&m).initKeymap()` or change the local to a pointer.

- [ ] **Step 4: Rewrite `shortcutsList`**

```go
func shortcutsList(m *Model) []struct{ key, desc string } {
	type row = struct{ key, desc string }
	var list []row

	// Conflicts first: a dropped binding is the one thing a user cannot
	// discover any other way.
	for _, c := range m.keyConflicts {
		list = append(list, row{"!", c.String()})
	}

	groups, byGroup := keymap.ActionsByGroup()
	for _, g := range groups {
		var bucket []row
		for _, a := range byGroup[g] {
			if a.Hidden {
				continue
			}
			if keys := m.keymap.Display(a.ID); keys != "" {
				bucket = append(bucket, row{keys, a.Label})
			}
		}
		if len(bucket) == 0 {
			continue // never render an empty heading
		}
		list = append(list, row{"", g})
		list = append(list, bucket...)
	}

	// Non-action rows: behaviour with no registry action behind it. Carried
	// verbatim from the pre-registry list — see Step 1's inventory and paste
	// EVERY row it found, not just these.
	list = append(list,
		row{"", "Built-in keys"},
		row{"f1", "About menu"},
		row{"ctrl+n", "New typed pane (plugin picker)"},
		row{"alt+1 … alt+9", "Switch directly to tab 1-9"},
		// ... the remaining rows from Step 1's inventory ...
	)
	return list
}
```

Add the `keymap` import to `dialog.go`.

- [ ] **Step 5: Run the shortcuts tests**

Run: `./scripts/dev.sh test ./internal/tui/`
Expected: PASS, including the three geometry tests. If `_FitsANarrowTerminal` now fails, the derived list is genuinely longer than the old one — fix by trimming labels, not by dropping the scroll assertion.

- [ ] **Step 6: Delete the superseded drift test**

`TestShortcutsList_CoversEveryProjectBinding` existed to catch hand-maintenance drift, now structurally impossible. Remove it; `TestShortcutsList_CoversEveryBoundVisibleAction` supersedes it.

- [ ] **Step 7: Run everything with the race detector**

Run: `./scripts/dev.sh test-race`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/tui/dialog.go internal/tui/shortcuts_scroll_test.go
git commit -m "refactor(tui): derive the shortcuts dialog from the action registry"
```

---

## Stage 1 exit criteria

- [ ] `./scripts/dev.sh test` passes.
- [ ] `./scripts/dev.sh test-race` passes.
- [ ] `./scripts/dev.sh vet` is clean.
- [ ] The Task 8 Step 5 grep returns exactly the three whitelisted sites.
- [ ] `./scripts/dev.sh build` succeeds; `./quil-dev.exe` launches with `[dev]` visible.
- [ ] Dev-instance smoke: `Ctrl+T`, `Ctrl+W`, `Alt+M`, `Alt+Shift+H`, `Alt+F2` **and** `Alt+Shift+R`, `Alt+P`, `Alt+Shift+A`. `F1 → Shortcuts` lists every group and scrolls to the end. `Ctrl+V` **and** `F8` both paste in a Settings text field. `Alt+1`/`Alt+2` still switch tabs.
- [ ] No `bindings.toml` exists and no config file was written — Stage 1 changes no on-disk format.

## What Stage 1 deliberately does not do

- No `bindings.toml`, presets, or migration — Stage 3.
- No prefix state machine; multi-step sequences parse and store but never fire — Stage 2.
- No new actions. `tab.next`, `tab.prev`, `tab.switch_1..9`, `system.shortcuts` are promoted in Stage 3 where the tmux preset needs them.
- No prefix-shadowing conflict rule — needs multi-step bindings, Stage 2.
- `notesKeyExempt` still uses `kbMatches` — Stage 2 rewrites it with the prefix machine.
