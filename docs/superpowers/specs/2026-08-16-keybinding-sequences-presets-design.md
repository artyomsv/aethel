# Key Sequences and Binding Presets — Design

**Date:** 2026-08-16
**Status:** Design agreed, not yet planned
**Scope:** `internal/keymap/`, `internal/config/`, `internal/tui/`
**Stages covered:** 2 (prefix sequence machine) and 3 (`bindings.toml`, presets, migration)
**Shipping:** both stages in **one PR**, off a fresh branch from `origin/master`

Stage 2 is designed to stand alone — it is useful, testable, and behaviour-safe
without any part of Stage 3 — but the decision is to ship them together. The
stage boundary therefore survives as a build order and a review structure, not
as two merges.

Two process facts that changed on master after Stage 1 and that this work must
follow:

- **`CHANGELOG.md` is no longer edited by hand.** Add one fragment per PR in
  `changelog.d/<type>-<slug>.md`; `scripts/promote-changelog.sh` collects them
  at release time. Anything in that directory that is not a valid fragment is
  *rejected*, not skipped.
- Stage 1's plan document still instructs the old `CHANGELOG.md` edit. That
  instruction is stale — do not copy it forward.

## Relationship to the 2026-08-07 design

`2026-08-07-keybinding-presets-design.md` (revision 3) designed all three stages
against the **pre-Stage-1** codebase. Stage 1 shipped on 2026-08-15 as
`0486578` / v1.60.0, which invalidated the parts of that document a reader most
needs to trust: its "Current state" table cites `model.go:3313` for `handleKey`
(now `3827`), `model.go:3562` for `tryPluginRawKey` (now `4089`), and describes
48 `kbMatches` call sites where 3 remain.

This document **supersedes revision 3 for stages 2 and 3** and is written
against the shipped API. Revision 3 is left in place unedited: it is the record
of what was decided on 2026-08-07 and why, and overwriting it would destroy that
history to fix line numbers.

Where this design departs from revision 3 — and it does, in three places — the
departure is called out with its reasoning rather than made silently.

## What Stage 1 actually shipped

The facts stages 2 and 3 build on, all verified against the tree:

| Thing | Where | Shape |
|---|---|---|
| Registry | `internal/keymap/action.go` | 42 `Action{ID, Label, Group, Tier, Order, Default, Hidden}` — 18 early, 24 late |
| Chords | `chord.go` | `ParseChord`, canonical `String()`, `Mod` bitmask incl. `ModSuper` |
| Specs | `parse.go` | `ParseSpec` → `[]Sequence`; **already parses multi-step** `"ctrl+b c"` |
| Build | `keymap.go` | `Build(map[ActionID]string) (*Keymap, []Conflict)` — never fails |
| Lookup | `keymap.go` | `chords map[Tier]map[string]ActionID` — **single chords only** |
| Sequences | `keymap.go` | `bindings map[ActionID][]Sequence` — multi-step already stored, never dispatched |
| Conflicts | `conflict.go` | 6 kinds; `hardcodedKeys` table with `afterBothTiers` / `betweenTiers` |
| Config seam | `internal/tui/keyspecs.go` | `keySpecsFromConfig` — the ONE map from legacy field names to IDs |

Two of those are load-bearing and easy to miss:

- **`ParseSpec` already handles sequences.** `new_tab = "ctrl+b c"` in today's
  `config.toml` parses, canonicalizes, and lands in `bindings`. What is missing
  is only dispatch. Stage 2 therefore needs no new config surface to be useful.
- **`Keymap.Bindings(id)` exists for exactly this.** Its doc comment names Stage
  2's prefix machine as the caller it was kept for. It returns unflattened
  `Sequence` values — chord count and identity — where every Stage 1 reader
  wanted a display string.

### Current dispatch order

Verified in `internal/tui/model.go`. Stage 2 inserts one step into this list and
changes nothing else about it.

```
3827  handleKey(msg)
3839    dialog != dialogNone      → handleDialogKey        [returns]
3844    renaming                  → handleRenameKey        [returns]
3847    renamingPane              → handlePaneRenameKey    [returns]
3853    ctxMenu.open()            → handleCtxMenuKey       [returns]
3864    notesMode                 → notes split            [returns OR falls through]
3920    overlayVisible            → handleOverlayKey       [returns]
                                  ←── Stage 2 probe goes here
3928    EARLY tier lookup         (18 actions)
4055    sidebarFocused
        selection enter/esc
4089    tryPluginRawKey                                    ← the seam
4107    isSelectionExtendKey
4114    LATE tier lookup          (24 actions)
4249    paste aliases (ctrl+alt+v, f8)
4254    reserved switch (ctrl+n, f1, alt+1..9)
        default                   → keyToBytes → PTY
```

---

# Stage 2 — the prefix sequence machine

**Goal:** `Ctrl+B` followed by `c` opens a tab. Today `Ctrl+B` is unbound, falls
through to the PTY as `\x02`, and the shell echoes `^B`. `Build` already reports
this honestly as `ConflictUnsupportedSequence` ("sequence not supported yet:
`ctrl+b c` → tab.new never fires"). Stage 2 makes that conflict kind obsolete
and deletes it.

**Self-contained by design, even though it ships with Stage 3.** With Stage 2's
code in place and nothing from Stage 3, a user writes `new_tab = "ctrl+b c"`
into `config.toml` and it works. That independence is not academic — it is what
lets Stage 2 be built and fully tested first, and it means a late problem in the
migration can be cut from the PR without unpicking the sequence machine.

`${prefix}`, presets and `bindings.toml` are Stage 3 deliberately: `prefix` is a
`bindings.toml` field, and inventing a temporary home for it in `config.toml`
would mean migrating it out again within the same PR.

## Matching: two maps, not a trie

Revision 3 called for `trie.go`. With 42 actions and a handful of alternatives
each, a trie is the wrong shape — it earns its keep on ordered traversal and
range queries, and this workload needs neither. `Build` instead populates two
structures alongside the existing `chords` map:

```go
type Keymap struct {
    chords   map[Tier]map[string]ActionID   // unchanged: single chords, per tier
    bindings map[ActionID][]Sequence        // unchanged
    seqs     map[string]ActionID            // NEW: full canonical sequence -> action
    partial  map[string]bool                // NEW: every PROPER prefix of every sequence
}
```

Both keys are the canonical space-joined form `Sequence.String()` already
produces. `"ctrl+b c"` inserts `seqs["ctrl+b c"] = "tab.new"` and
`partial["ctrl+b"] = true`.

Lookup is then two map hits and no traversal:

```go
// MatchSeq resolves a pending sequence. Tier-agnostic by design — see below.
func (k *Keymap) MatchSeq(pending []Chord) (id ActionID, exact bool, partial bool)
```

The `partial` set is what runs on the hot path for **every** keypress, bound or
not, so O(1) is not a micro-optimisation — it is the difference between the
sequence machine being free and it being a per-keystroke cost in the input path
this project has already had two incidents in.

**Single-chord bindings do not enter `seqs`.** They stay in `chords`, dispatched
by the two tier lookups exactly as today. A length-1 sequence never yields a
partial match, so no existing binding's behaviour can move. This is the property
that makes Stage 2 safe to merge.

## Probe placement

The probe goes at **one** site: after the overlay guard (`3922`) and before the
early-tier lookup (`3928`).

Every mode that fully owns the keyboard — dialog, rename, pane-rename, context
menu, overlay — `return`s before that point. Ordering alone makes the machine
inert in all five; no predicate, no per-mode call site, nothing to keep in sync.

Three modes need naming explicitly, because they sit *downstream* of the probe
and would otherwise have their keys stolen:

| Mode | Site | Why it must be inert |
|---|---|---|
| Sidebar focused | `model.go:4055` | Arrow keys navigate the notification list |
| Active text selection | enter/esc block, `~4060` | Enter copies; Esc clears |
| Reconnect / parked | `model.go:1053`, `reconnect.go:427` | The screen owns every key; `reconnect.go` already ignores mouse and paste |

The probe is guarded on those three conditions. Everything else is free.

### The probe is tier-agnostic, and must be

This is the single subtlety that a reasonable implementation gets wrong.

`pane.close` is a **late**-tier action. Bind it to `"ctrl+b x"` and the opening
chord `ctrl+b` is not in the early tier's chord map — an early-tier probe
returns nothing, the key falls through to `tryPluginRawKey` (`4089`) or to the
PTY default, and the sequence can never complete no matter how many times the
user presses `x`.

So: **the tier split governs `Exact` resolution of single chords only. Partial
detection is global.** The probe consults `partial` across all actions
regardless of tier, which is precisely why it sits before the early lookup
rather than inside it.

**Consequence, stated so nobody rediscovers it as a bug:** a pending sequence
outranks a plugin's `raw_keys` claim. If a plugin claims `x` and the user binds
`"ctrl+b x"`, the sequence wins. This is the one place the design deliberately
changes today's precedence, it is scoped to multi-step bindings, and no
single-chord binding is affected.

## Pending state and cancellation

```go
// on Model
pendingSeq []keymap.Chord  // empty = machine idle
pendingGen int             // generation guard for the timeout tick
```

Probe outcomes:

| Result | Behaviour |
|---|---|
| `partial` | Append chord, swallow the key, show the indicator, return |
| `exact` | Run the action, clear pending |
| none, `len(pendingSeq) > 1` | Drop the sequence, flash the status bar, clear |
| none, `len(pendingSeq) == 1` | **Clear and fall through to existing dispatch, unchanged** |

The last row is what preserves today's behaviour. A chord that opens no sequence
is not pending in the first place, so the fall-through is byte-identical to the
current code path.

Cancellation, each with its reason:

- **Esc** always cancels. The universal escape hatch; no mode may swallow it.
- **Mouse click** cancels. A click can change the active pane mid-sequence, so a
  completed action would target a different pane than the one the prefix was
  pressed in.
- **`tea.PasteMsg`** cancels (`model.go:1825`). Paste bypasses `handleKey`
  entirely and lands in the PTY; leaving a prefix armed across it means the next
  keystroke is read as a sequence step.
- **`sequence_timeout`** — **off by default**, matching tmux. When set, a
  `tea.Tick` arm compares `pendingGen` before clearing, so a cancelled
  sequence's late tick cannot clear a newly-started one (the
  `paletteSearchTimeout` compare-on-key precedent). Like every local timer in
  this codebase, **that Update arm must not re-arm `listenForMessages`.**

The config field for the timeout lands in Stage 3 with the rest of
`bindings.toml`. Stage 2 implements the machinery with the value hardcoded off,
which is the shipped default either way.

## The literal escape

**`prefix prefix` sends one literal chord to the PTY.** Pressing `Ctrl+B`
twice with `Ctrl+B` as a sequence head emits a single `\x02` downstream.

Not a nicety. Quil panes routinely `ssh` into hosts running real tmux, and
without this the remote tmux has no reachable prefix — the outer Quil eats every
`Ctrl+B` forever. tmux itself solves it the same way, so the behaviour is
already in the muscle memory of the users the tmux preset exists for.

Implementation: when the probe is armed with exactly `[c]` and the incoming
chord is also `c`, clear pending and fall through to the PTY default with the
original `tea.KeyPressMsg`, so `keyToBytes` (`model.go:~7220`) does the
encoding. No special-case byte table.

## Status indicator

`renderStatusBar` (`model.go:5630`) renders the pending chords while armed —
`^B…` after `Ctrl+B`. A silent armed state is indistinguishable from a wedged
TUI, which is the support ticket this prevents.

The dropped-sequence flash reuses the same slot. **`Blocked` flashes rather than
silently no-ops** on the same principle that puts conflict warnings in F1: the
user pressed two keys deliberately and deserves to know why nothing happened.

## Retiring the last three `kbMatches` call sites

`internal/tui/keymatch.go` is the pre-registry string comparison, deprecated for
dispatch. `keyspecs.go`'s own comment names this as Stage 2's job **and Stage
3's hard prerequisite** — a Stage 3 that empties the legacy table while these
readers remain leaves them comparing against empty strings, at which point
`Alt+E` stops exiting notes mode and every structural key stops flushing the
editor.

| Site | Action | Fate |
|---|---|---|
| `model.go:3868-3892` | Notes-mode key split — 8 `kbMatches` calls | → `m.isAction(key, "pane.notes_toggle")` etc. |
| `model.go:3624` | `notesKeyExempt`'s loop over configured bindings | → registry lookup |
| `model.go:1053` | `reconnectResumeKey` | **Stays.** A hardcoded `"r"` const (`reconnect.go:527`), not a config value — nothing to migrate |

These are mechanical swaps that preserve behaviour exactly. Once done,
`keySpecsFromConfig` is genuinely the last thing between config and dispatch,
which is what lets Stage 3 replace its body and touch nothing else.

## Departure from revision 3: no `Action.NotesBehaviour`

Revision 3 specified a three-value notes policy
(`Exempt | ExemptAfterFlush | Blocked`) as a new registry field, requiring an
explicit per-action decision across all 42 actions.

**Dropped.** Reading the notes block as it actually stands (`model.go:3864-3912`),
the existing control flow already produces the desired behaviour:

- **Editor focused, ordinary key** → the editor consumes it as text (`3906`) and
  returns. The probe never sees it. Correct: you are typing notes, and a prefix
  chord in prose must stay prose.
- **Pane focused** → falls through (`3895`) to the probe. Sequences work — and
  the comment already sitting at that line promises exactly that ("Global
  shortcuts (dialogs, rename, ...) work as usual").
- **Exempt key, editor focused** → `exitNotesModeInPlace()` then falls through.
  Every exempt key is a single chord, so nothing changes.

A new enum plus 42 audited decisions to reproduce behaviour the fall-through
already gives is cost without benefit. If a concrete need for per-action notes
policy appears later it can be added then, against a real case rather than a
speculative one.

## Conflict changes in Stage 2

**Removed:** `ConflictUnsupportedSequence`. Its entire purpose was to keep
`Build` honest while sequences parsed but did not dispatch. Once they dispatch
it is a lie, and leaving it would make F1 warn about working bindings.

**Added:** `ConflictShadowed` — sequence A is a strict prefix of sequence B, so
A can never fire without a timeout. Stage 2 has exactly one layer (`config.toml`),
so only the intra-layer rule applies: **the shorter sequence is refused**, with a
warning. Stage 3 adds the cross-layer rule, where the answer is different.

**Drop granularity is per-alternative.** If `new_tab = "ctrl+b c, ctrl+t"`
collides on `ctrl+t` only, that alternative alone is dropped and the binding
keeps `ctrl+b c`. Dropping the whole binding would punish the user for the half
that was fine.

---

# Stage 3 — `bindings.toml`, presets, migration

**Goal:** ship named keymaps (`default`, `tmux`), let users select one, and move
bindings out of `config.toml` into a file that a `Save` cannot freeze.

## Why bindings must leave `config.toml`

`config.Save` (`config.go:555`) serializes the whole struct. `Mutate`
(`config.go:546`) is Load-then-fn-then-Save. So any config ever written by any
build round-trips all 42 keybinding fields back to disk with their current
values.

The consequence: a preset resolved into `KeybindingsConfig` is frozen on the
next unrelated Settings edit. The user picks `tmux`, later toggles a UI option,
and their keymap is now 42 literal strings that no future preset change can
reach. `config.go`'s own `quick_actions` comment documents this trap verbatim.

## Layering

Three layers, lowest to highest: **embedded `default` → selected preset → user
`[bindings]` overrides**. Only the override layer is ever written back.

```toml
# ~/.quil/bindings.toml
preset = "tmux"
prefix = "ctrl+a"
sequence_timeout = "0"   # 0 = off, matching tmux

[bindings]
pane.rename    = "alt+shift+r"   # replaces the preset's binding
project.picker = ""              # explicit unbind
```

**Merge semantics: replace, not union.** Per action, a higher layer replaces the
lower entirely. Absence inherits; presence replaces; `""` unbinds.

Consequences, spelled out so no implementer has to guess:

- **The tmux preset replaces cleanly.** `tab.new = "${prefix} c"` means `Ctrl+T`
  no longer opens a tab. Deliberate: a preset that keeps both key sets is
  neither keymap, and it doubles the conflict surface. A user who wants both
  writes the alternative in their own `[bindings]`.
- **Unnamed actions inherit.** The tmux preset names 26 of ~54 actions; the rest
  keep `default`'s bindings. `Alt+Shift+P` still opens the palette.
- **`default` → `""` in a preset → user override resolves to the user's
  binding.** Each layer replaces wholesale; `""` is a value, not a deletion that
  propagates upward.

### Where the layering code lives

`internal/keymap` imports stdlib only, which is what makes it testable without
`QUIL_HOME` and without a `Model`. Stage 3 must not break that.

| Concern | Home | Why |
|---|---|---|
| `Resolve(layers ...map[ActionID]string) map[ActionID]string` | `keymap/resolve.go` | Pure function, no I/O |
| Embedded presets | `keymap/preset.go`, `//go:embed presets/*.toml` | Compile-time, no runtime path |
| Reading `bindings.toml` | `internal/config` | Already owns every `QuilDir()`-derived path |
| User presets in `$QUIL_HOME/presets/` | `internal/config` | Same |

`keymap` gains one dependency (`BurntSushi/toml`, already in the module) and no
knowledge of where files live. `internal/config` keeps all path derivation,
alongside `ConfigPath`, `PluginsDir`, `PasteDir`, `RecentCWDsPath`.

**Keymap load stays out of `Model` construction.** ~46 tests build a `Model`
directly without setting `QUIL_HOME`; a disk read in the constructor points
every one of them at the real `~/.quil`. Follow the `SetRecentCWDs` precedent —
a pure setter on the `Model`, loaded in `cmd/quil/main.go`.

## `${prefix}`

`${prefix}` expands **at parse time, before insertion**, so conflict detection
and the `partial` set see real chord sequences rather than templates.

`prefix` must be exactly one canonical chord. Rejected at load, each for a
concrete reason:

- **Unset while `${prefix}` is referenced** — a load error, not an empty
  expansion. Expanding to `""` turns `"${prefix} c"` into the bare chord `c`,
  which then eats every `c` before the PTY sees it.
- **Contains a comma** (`"ctrl+a, ctrl+b"` — a tmux `prefix2` instinct). This
  expands into the *alternatives* grammar: `"${prefix} c"` becomes `chord(ctrl+a)`
  OR `seq(ctrl+b, c)`. Silently wrong.
- **Contains a space** — turns every `"${prefix} c"` into a three-step sequence.

An unmodified printable prefix (`"a"`) is a **warning**, not an error — it eats
a letter globally, but it is the user's letter to eat. Mirrors the existing
`sanitizeRawKeys` warning in `internal/plugin/registry.go`.

Setting `prefix` under a preset that never references `${prefix}` is inert, not
an error.

## The tmux preset

| tmux | Action | Note |
|---|---|---|
| `c` | `tab.new` | new-window |
| `,` | `tab.rename` | rename-window |
| `&` | `tab.close` | kill-window |
| `%` | `pane.split_h` | side by side |
| `"` | `pane.split_v` | top/bottom |
| `x` | `pane.close` | kill-pane |
| `z` | `pane.focus_toggle` | zoom — Quil's focus mode is an exact match |
| `←↑↓→` | `pane.left/up/down/right` | spatial nav already matches tmux |
| `o` | `pane.next` | exists, ships unbound |
| `1`–`9` | `tab.switch_1..9` | **new actions** |
| `n` / `p` | `tab.next` / `tab.prev` | **new actions** |
| `d` | `app.quit` | quit leaves the daemon running — detach semantics exactly |
| `[` | `pane.scroll_page_up` | approximates copy-mode entry |
| `?` | `system.shortcuts` | **new action** — today reachable only via F1 → About |

Out of scope for the preset: tmux's `:` command prompt, `t` clock, and `w`
window chooser (the command palette covers the last under its own key).

### 12 new actions

| ID | Tier | Handler |
|---|---|---|
| `tab.next` | Late | **New.** `switchTab(idx+1)` with wrap |
| `tab.prev` | Late | **New.** `switchTab(idx-1)` with wrap |
| `tab.switch_1` … `tab.switch_9` | Late | `m.switchTab(n-1)` — already exists |
| `system.shortcuts` | Late | Opens F1 → Shortcuts directly |

`tab.switch_N` is **nine discrete actions**, mirroring `alt+1`…`alt+9`, not a
parameterized action the registry has no other use for.

**Note the off-by-one:** tmux windows are 0-indexed, Quil tabs are 1-indexed and
render a 1-based prefix. The preset binds `${prefix} 1`…`${prefix} 9` and leaves
`${prefix} 0` **unbound** — mapping tmux's `0` to Quil's tab 1 would double-bind
tab 1 and hide tab 9 from anyone counting from zero.

`Order` values sit above the existing maximum (`json.transform`, 4300), because
`alt+1..9` is checked last in `handleKey` today and `Order` is what reproduces
that rank for the duplicate tie-break.

### Promoting `alt+1..9` changes conflict messages

Those nine keys are currently in `keymap.hardcodedKeys` with position
`afterBothTiers`. Promoting them to registry actions **removes nine entries from
that table**, which changes the `ConflictHardcoded` messages a user with a
colliding binding sees today.

This is correct — they are no longer hardcoded — but it is a visible change to
F1 rows and to log lines, and `hardcodedString` derives its direction from that
very table. Flagged because it looks like an unrelated regression otherwise.

## Migration

`config.go:508-527` documents why the `quick_actions` patch is in-memory only:
a startup write from every process that loads config (TUI, daemon, MCP bridge)
would race the same file. The migration must not repeat that mistake.

**TUI-only.** Verified: `Keybindings` is read exclusively by `internal/tui` and
`internal/config`. The daemon and the MCP bridge never touch it.

On first launch with no `bindings.toml`:

1. Load `config.toml`.
2. **Abort if `Load` returned an error.** A transiently malformed `config.toml`
   otherwise migrates as pure defaults and *permanently discards* the user's
   customizations — the migration is one-way.
3. Apply existing legacy patches **first**, notably the `quick_actions` rewrite
   at `config.go:525`. Running before it would faithfully preserve a binding the
   project deliberately killed.
4. Diff each field against `config.Default().Keybindings`.
5. Write `preset = "default"` plus **only the differing fields**, atomically
   with `O_EXCL` — two TUI clients can attach concurrently.
6. Log each override adopted.

**The diff is load-bearing.** Most installs carry all 42 fields on disk from a
past `Save`; copying rather than diffing produces 42 overrides and pins every
user to today's defaults permanently — the exact bug this move exists to fix.

Accepted limitation: a user who deliberately set a value equal to the default
loses the explicitness of the entry, not the behaviour.

## `Save` gating — a two-release sequence, in one PR

`Mutate` is Load+fn+Save, so the first unrelated mutation after upgrade — a
remote install writing `[remote.hosts]`, say — would delete `[keybindings]` from
disk if `Save` stopped emitting it. If `bindings.toml` was never successfully
written, or the user deleted it to reset, the migration source is then gone.

Quil's auto-update has a real rollback path (`internal/update/`, rename-aside
swap). A rollback lands a binary that cannot read `bindings.toml`, and if the
legacy table is already stripped, that user's entire keymap resets to defaults.

So:

- **Release N — this PR.** `Save` always emits `[keybindings]`, exactly as
  today. `bindings.toml` is written and read; the legacy table stays on disk as
  a fallback the previous binary can still use.
- **Release N+1 — a separate, later PR.** `Save` strips `[keybindings]` **iff
  `bindings.toml` exists on disk**. If it is missing, the legacy table survives
  and remains the migration source.

**This is a tracked follow-up, not a hope.** Stages 2 and 3 ship as one PR, so
nothing in that PR's own diff will ever remind anyone the second half exists.
The plan must create a `techdebt/` entry for the strip at the same time it
implements release N, or "two-release gating" quietly becomes "we never
stripped it" and the legacy table lives forever.

## Conflict changes in Stage 3

**Added:** `ConflictPrefixInvalid` — `${prefix}` referenced while unset, or a
`prefix` containing a comma or a space.

**Extended:** `ConflictShadowed` gains the cross-layer rule. Within one layer,
the shorter sequence is refused (Stage 2's rule). **Across layers, resolved by
layer, not by length**: the higher-layer binding survives.

That asymmetry matters and is not arbitrary. A user override
`pane.rename = "ctrl+b"` under the tmux preset must be able to reclaim the
prefix key as a plain chord. A length-based rule would let a preset veto an
explicit user override, inverting the whole layering model. Length is safe as
the *intra*-layer tie-break precisely because no cross-layer inversion is
possible there.

## The paste aliases move into the registry

`ctrl+alt+v` and `f8` (`model.go:4249`) become alternatives on `pane.paste` in
the `default` preset:

```toml
pane.paste = "ctrl+v, ctrl+alt+v, f8"
```

That makes them visible in F1, user-removable, and removes a compound case from
`handleKey`. They leave `hardcodedKeys` along with `alt+1..9`.

**Caveat that must reach `docs/keybindings.md`:** a user who rebinds
`pane.paste` to a single chord loses `F8`, and Windows Terminal never delivers
`Ctrl+V`. The docs must say plainly that Windows users should keep an `F8`
alternative.

---

# Testing

Table-driven, per the repo's Go conventions. `internal/keymap` tests need no
`QUIL_HOME` and no `Model`; the `internal/tui` ones drive `Update`.

## Stage 2

- **Sequences complete through `Update`, never through `MatchSeq` directly.** A
  direct-call test and its mutation can both pass against a decision the call
  site makes unreachable — this repo has hit that trap before
  (`unit-test-bypassing-call-site`).
- **`TestDispatch_SequenceBeatsRawKeys`** — a late-tier sequence
  (`pane.close = "ctrl+b x"`) completes end to end on a pane whose plugin
  declares `raw_keys` claiming `x`. This is the test that fails if the probe is
  made tier-scoped.
- **`TestDispatch_RawKeysPrecedence`** (Stage 1, keep) — single-chord early
  beats `raw_keys`, single-chord late loses. Proves Stage 2 moved nothing.
- **One test per inert mode**, each arming a sequence head and asserting the
  mode consumed it: dialog, rename, pane-rename, ctxmenu, overlay, sidebar
  focused, active selection, parked screen.
- **Notes mode, both halves**: editor-focused consumes the prefix chord as text;
  pane-focused completes the sequence.
- **Cancellation**: Esc, mouse click, `tea.PasteMsg`, non-match at `len > 1`.
- **`TestSequence_LiteralEscape`** — `prefix prefix` emits exactly one chord's
  bytes to the PTY.
- **`TestSequence_UnmatchedSingleChordFallsThrough`** — the property that makes
  Stage 2 safe: an armed-then-unmatched length-1 pending state dispatches
  identically to no machine at all.
- **Timeout generation guard** — a cancelled sequence's late tick must not clear
  a newly started one.

## Stage 3

- **`TestDefaultPreset_MatchesLegacyDefaults`** — pins `default` against
  `config.Default().Keybindings`. Without it, a preset edit silently changes
  every existing user's keymap.
- **`TestPresets_AllChordsCanonical`** — every chord in every shipped preset
  round-trips through `ParseChord`.
- **`TestKeymap_EveryShippedDefaultMatchesARealKeyPress`** (Stage 1, extend to
  presets) — the only test in the tree that builds a `tea.KeyPressMsg` and
  compares, and therefore the only one that can catch a preset chord spelled
  unlike Bubble Tea's rendering. `internal/keymap` cannot do this itself.
- **`TestEveryPreset_NoConflicts`** and **`TestEveryPreset_CoversEveryAction`** —
  the latter asserts every action resolves to a binding *or* an explicit `""`
  **after the full merge**, not that each preset file names all ~54.
- **Layer merge**: absence inherits, presence replaces, `""` unbinds; and
  `default` → `""` → user override resolves to the user's binding.
- **Cross-layer shadowing**: a user override reclaiming the prefix key as a
  chord must beat the preset's sequences.
- **Migration**: a 42-field legacy config yields **0** overrides; one
  customization yields **1**; a `Load` error yields **no file**; a second launch
  with `bindings.toml` present is a no-op.
- **`Save` still emits `[keybindings]`** — the release-N guarantee, asserted
  explicitly so the follow-up PR has a test to invert.

---

# Out of scope

- **Binding scopes** as a general mechanism. `Tier` is a targeted attribute, not
  a scope system.
- **`Action.NotesBehaviour`** — see the Stage 2 departure above.
- **tmux command prompt** (`:`) and tmux command names.
- **Per-plugin keymaps.** `raw_keys` already covers pass-through.
- **A GUI binding editor.** The F1 viewer stays read-only.
- **Stripping `[keybindings]` from `Save`** — release N+1, tracked separately.

# Docs to update

`docs/keybindings.md`, `docs/configuration.md`, and a new `.claude/rules/` entry
scoped to `internal/keymap/`.

Three things the docs must answer, because each otherwise generates a support
question that the rules resolve only as a log warning:

- **A prefix colliding with an inherited chord silently degrades.**
  `prefix = "ctrl+w"` under the tmux preset makes every preset sequence shadow
  whatever inherited default chord shares that head. Coherent under the
  cross-layer rule, invisible except in F1's warning rows. Say so, and say to
  check F1 after changing `prefix`.
- **Windows paste.** Rebinding `pane.paste` to a single chord loses `F8`, and
  Windows Terminal never delivers `Ctrl+V`. Keep an `F8` alternative.
- **Presets replace, they do not add.** Selecting `tmux` removes `Ctrl+T` and
  `Ctrl+W`. Show the override syntax for getting them back.
