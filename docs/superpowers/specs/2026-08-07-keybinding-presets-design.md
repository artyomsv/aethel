# Keybinding Presets and `bindings.toml` — Design

**Date:** 2026-08-07
**Status:** Design agreed, not yet planned
**Scope:** `internal/keymap/` (new), `internal/config/`, `internal/tui/`
**Revision:** 3 — second review round folded in (tier-agnostic partial probe,
intra-layer shadowing, notes seeding, paste aliases into the registry)

## Problem

Quil ships its own keymap. Users arriving from tmux press `Ctrl+B` and nothing
happens — every binding is a single chord, so there is no grammar for the
prefix-then-key idiom that defines tmux. There is also no way to save, share, or
switch a whole keymap: bindings are 41 individual strings in `config.toml`.

Two asks, one dependency:

1. **Presets** — ship named keymaps (`default`, `tmux`), let users select one.
2. **User-defined keymaps** — store them as `bindings.toml`.

The dependency: a tmux preset that only remaps single chords is not tmux. Users
press `Ctrl+B` and get readline's backward-char. **Sequence support is a
prerequisite, not an enhancement.**

## Current state

| Concern | Location | Shape |
|---|---|---|
| Storage | `internal/config/config.go:188-290` | 41 `string` fields |
| Defaults | `internal/config/config.go:336-393` | literals in `Default()` |
| Matcher | `internal/tui/keymatch.go:9` | `kbMatches` = exact match, comma-split alternatives |
| Dispatch | `internal/tui/model.go:3313` | `handleKey`, 48 `kbMatches(` sites |
| Raw-key seam | `internal/tui/model.go:3562` | `tryPluginRawKey` |
| Help list | `internal/tui/dialog.go:361-440` | hand-maintained table |
| Palette | `internal/tui/palette.go` | separate `paletteAction` enum |
| Context menu | `internal/tui/ctxmenu.go:484` | third dispatcher |
| Notes exemptions | `internal/tui/model.go:3062` | `notesKeyExempt` |

### `handleKey` is two tiers, not one switch

This is the single most important fact about the rewrite. Order in
`handleKey` (`model.go:3313`):

```
dialogs → rename → ctxmenu → notes → lazygit overlay
  → EARLY switch  (~20 actions, model.go:3410-3525)
  → sidebar-focused → selection enter/esc
  → tryPluginRawKey (model.go:3562)          ← the seam
  → selection-extend keys
  → LATE switch   (model.go:3581-3734)
```

A plugin's `raw_keys` entry therefore **loses** to `MutePane` (`3431`, early)
and **wins** against `RestartPane` (`3591`, late). Any design that collapses
both switches into one trie lookup silently flips one half of that relationship.

Worse, the same action resolves differently by context: `kb.Quit` appears at
`3356` (notes path — flush then quit) and `3582` (main switch). `kb.PaneLeft` at
`3361` focuses a pane in notes mode rather than navigating. **Key → action is
context-dependent**, so the case rewrite is not mechanical.

### Three constraints that shape the design

**`config.Save` serializes the whole struct** (`config.go:449`). The
`quick_actions` migration comment at `config.go:404-418` documents this verbatim:
any config ever saved by an old build round-trips all 41 fields back to disk. A
preset resolved into `KeybindingsConfig` would be frozen on the next Settings
edit.

**There is no stable action identifier.** Bindings are addressable only as
`kb.SplitHorizontal`. A preset is data; data cannot name a Go struct field.

**Plugins can claim keys.** `sanitizeRawKeys` (`internal/plugin/registry.go:592`,
cap `maxRawKeys = 64` at `:585`) lets a plugin declare pass-through keys.

## Decisions

| Fork | Decision | Why |
|---|---|---|
| tmux fidelity | True prefix grammar | A chord-only "tmux" preset fails the first key a tmux user presses |
| Storage | `bindings.toml` sole home; one-way migration | Fixes the Save-freezes-everything trap |
| Prefix model | Pure sequence trie + `${prefix}` variable | Keeps the engine general — vim `space w s`, emacs `ctrl+x 2` — while giving a one-line prefix rebind |

## Architecture

New package `internal/keymap/`. Stdlib + `BurntSushi/toml` only, no TUI imports,
so it tests without `QUIL_HOME` and without a `Model`.

| File | Responsibility |
|---|---|
| `action.go` | Registry: `Action{ID, Label, Group, Tier}` |
| `chord.go` | Canonical chord form + validation |
| `parse.go` | `"${prefix} %, alt+shift+h"` → `[][]Chord` |
| `trie.go` | `Match(seq) → (ActionID, Exact \| Partial \| None)` |
| `resolve.go` | Layer merge |
| `preset.go` | `//go:embed presets/*.toml` + `~/.quil/presets/*.toml` |
| `conflict.go` | Load-time validation |
| `migrate.go` | Legacy `[keybindings]` → overrides |

### `Action.Tier` preserves today's ordering

Each action carries `Tier ∈ {TierEarly, TierLate}`, assigned to match its
current switch. Dispatch is **four** steps:

```
step 0 — partial probe, TIER-AGNOSTIC, against the FULL trie
           Partial → arm the prefix machine, swallow the key, return
step 1 — early-tier trie  → run if Exact
           (sidebar-focused routing, selection enter/esc — unchanged)
step 2 — tryPluginRawKey  → forward to PTY if claimed
           (isSelectionExtendKey — unchanged)
step 3 — late-tier trie   → run if Exact
```

**Step 0 must be tier-agnostic and must precede everything else.** A late-tier
sequence — `pane.close = "${prefix} x"`, and `pane.close` is late
(`model.go:3591`) — opens with a chord that returns `None` from the early trie.
A per-tier probe would let that first chord fall through to `tryPluginRawKey`,
or to the PTY default, before any tier ever reported `Partial`, and the sequence
could never complete. **The tier split governs `Exact` resolution only; partial
detection is global.**

A pending sequence therefore outranks plugin `raw_keys`. This is the one place
the design deliberately changes today's precedence, and it is scoped to
multi-step bindings — a single-chord binding never yields `Partial`, so no
existing behaviour moves.

The three interleaved interceptors keep their exact positions: sidebar-focused
routing (`model.go:3528`) and selection enter/esc (`3533-3556`) stay between the
early switch and the raw seam; `isSelectionExtendKey` (`3577`) stays between the
raw seam and the late switch. Placing the late-tier lookup before
selection-extend would make a binding on `shift+left` beat selection.

### Context-dependent actions

Actions whose meaning differs by mode (`Quit`, `PaneLeft`…`PaneDown` in notes
mode) resolve to the **same action ID**; the mode-specific behaviour stays in the
handler, exactly where it lives today. The registry maps keys to IDs; it does not
map IDs to behaviour.

## File format

```toml
# ~/.quil/bindings.toml
preset = "tmux"
prefix = "ctrl+a"
sequence_timeout = "0"   # 0 = off, matching tmux

[bindings]
pane.rename    = "alt+shift+r"   # replaces the preset's binding
project.picker = ""              # explicit unbind
```

**Resolution order**, lowest to highest: embedded `default` → selected preset →
`[bindings]` overrides. Only the override layer is written back.

### Merge semantics — replace, not union

Per action, a higher layer **replaces** the lower layer's binding entirely.
Absence inherits; presence replaces; `""` unbinds.

Consequences, stated so no implementer has to guess:

- **The tmux preset replaces cleanly.** Every action it names loses its default
  chord: `tab.new = "${prefix} c"` means `Ctrl+T` no longer opens a tab, and
  `pane.close = "${prefix} x"` means `Ctrl+W` no longer closes a pane. This is a
  deliberate product decision — a preset that keeps both key sets is neither
  keymap, and it doubles the conflict surface. A user who wants both writes the
  alternative in their own `[bindings]` override.
- **Unnamed actions inherit.** The registry holds ~53 actions (41 existing plus
  the 12 promoted below). The tmux preset names 26 of them; the other ~27 keep
  `default`'s bindings — `Alt+Shift+P` still opens the command palette, `Alt+F2`
  still renames a pane, `Alt+G` still toggles lazygit.
- **An action bound in `default`, `""` in a preset, and overridden by the user
  resolves to the user's binding.** Each layer replaces wholesale; `""` is a
  value, not a deletion that propagates upward.
- `TestEveryPreset_CoversEveryAction` asserts every action resolves to *either*
  a binding or an explicit `""` **after the full merge** — not that each preset
  file names all ~53.

## Grammar

- **Space** separates sequence steps: `"ctrl+b c"`.
- **Comma** separates alternatives: `"ctrl+b c, ctrl+t"`. Whitespace after the
  comma is optional — the shipped default `"alt+f2,alt+shift+r"`
  (`config.go:355`) has none, and the parser must accept it verbatim.
- `${prefix}` expands at parse time, **before** trie insertion, so conflict
  detection sees real key sequences rather than templates.
- A single chord is a length-1 sequence — byte-identical to today.

### Chord canonicalization

Matching today is exact-string against Bubble Tea's `key.String()`
(`keymatch.go:9`). Without a canonical form, `"ctrl+shift+a"` and
`"shift+ctrl+a"` are different keys, conflict detection misses the collision,
and a preset chord spelled unlike BT's rendering is **silently dead with the
conflict checker green**. The codebase already shows the fragility: rename
handlers match `"escape"` while selection matches `"esc"`.

`chord.go` therefore defines a canonical form — modifiers in fixed order,
lowercased, aliases folded (`esc`/`escape`, `pgup`/`pageup`) — and every layer
is normalized through it before comparison. Presets are validated against the
canonical set at build time via `TestPresets_AllChordsCanonical`.

### `${prefix}` validation

`prefix` must be **exactly one canonical chord**. Rejected at load:

- **Unset while `${prefix}` is referenced** — a load error, not an empty
  expansion. Expanding to `""` turns `"${prefix} c"` into the bare chord `c`,
  which then eats every `c` before the PTY sees it.
- **Contains a comma** (`"ctrl+a, ctrl+b"` — a tmux `prefix2` instinct). This
  expands into the *alternatives* grammar: `"${prefix} c"` becomes
  `chord(ctrl+a)` OR `seq(ctrl+b, c)`. Silently wrong.
- **Contains a space** — turns every `${prefix} c` into a three-step sequence.

An unmodified printable prefix (`"a"`) is a **warning**, not an error — it eats
a letter globally. Mirrors `sanitizeRawKeys`'s existing warning
(`registry.go:608-613`).

Setting `prefix` under a preset that never references `${prefix}` is inert.

## Prefix state machine

`Model.pendingKeys []Chord`, evaluated in `handleKey` ahead of the tier lookups.

| Match | Behaviour |
|---|---|
| Exact | run action, clear pending |
| Partial | swallow key, keep pending, show indicator |
| None, `len(pending) > 1` | drop sequence, flash status bar, clear |
| None, `len(pending) == 1` | fall through to existing dispatch, unchanged |

Required behaviours:

- **`prefix prefix` sends one literal key to the PTY.** Quil panes routinely
  `ssh` into a host running tmux; without this the remote tmux is unreachable.
- **Visible pending indicator** in the status bar (e.g. `^A…`).
- **Esc always cancels.**
- **`sequence_timeout` defaults to off.** When set, the `tea.Tick` arm needs a
  **generation guard** so a cancelled sequence's late tick cannot clear a newly
  started one — the `paletteSearchTimeout` compare-on-key precedent. Like every
  local timer in this codebase, its Update arm must **not** re-arm
  `listenForMessages`.
- **`tea.PasteMsg` cancels pending.** Paste bypasses `handleKey` entirely and
  lands in the PTY; leaving a prefix armed across it means the next keystroke is
  read as a sequence step.
- **Mouse click cancels pending.** A click can change the active pane
  mid-sequence, so the completed action would target a different pane than the
  one the prefix was pressed in.

### Where the machine is inert

Every mode that owns the keyboard, with its guard site:

| Mode | Site |
|---|---|
| Any dialog / palette / confirm | `m.dialog != dialogNone` |
| Context menu | `ctxmenu.go` |
| Inline tab / pane rename | `model.go:3330-3335` |
| Lazygit overlay | `model.go:3405` |
| Sidebar-focused notification center | `model.go:3528` |
| Active text selection (enter/esc) | `model.go:3533-3556` |
| Reconnect / parked screen | `model.go:870`, `reconnect.go:403` |

Each omission is a leak in one of two directions: a pending prefix steals a key
from that mode, or the mode steals the sequence's second key. Rename is the
sharpest — typing a rename that contains the prefix character must not arm the
machine.

### Notes mode

Verified: editor-focused non-exempt keys are consumed as text
(`model.go:3390-3396`), and **every exempt key calls `exitNotesModeInPlace()`**
(`model.go:3386-3389`). So the prefix chord cannot simply join
`notesKeyExempt` — that would tear down notes mode on prefix *press*.

Design:

1. The prefix machine runs **before** the notes branch and **without**
   teardown. A `Partial` match swallows the key and leaves notes mode intact.
2. On `Exact`, the resolved action consults a notes policy on the registry:
   `Action.NotesBehaviour ∈ {Exempt, ExemptAfterFlush, Blocked}`.

   **Every seed is `ExemptAfterFlush`.** Today there is exactly *one* exempt
   behaviour: both the structural branch (`model.go:3379-3380`) and the exempt
   branch (`3386-3389`) call `exitNotesModeInPlace()` before falling through.
   That includes `Redraw` and `SidebarToggle`, whose comments inside
   `notesKeyExempt` say "harmless while the editor is open" — the comment's
   intent was run-in-place, the implementation exits notes anyway. Seeding
   either as plain `Exempt` to match the comments would change behaviour while
   looking like preservation. **`Exempt` starts empty**, reserved for future
   improvements, each a deliberate reviewed change.
3. Actions **not** on today's exempt list become newly reachable in notes mode
   via sequences (`ScrollPageUp`, `Paste`, `CommandPalette` — which has its own
   explicit notes no-op guard). Each needs an explicit policy value rather than
   inheriting a default, or the sequence silently does nothing.
4. **`Blocked` flashes the status bar**, it does not silently no-op. The same
   principle that puts conflict warnings in F1 applies to a dropped *sequence*:
   the user typed two keys and deserves to know why nothing happened.

## Conflict detection

At load, after expansion and canonicalization:

1. **Duplicate sequence** — two actions claim the same keys. **Higher layer
   wins.** Within one layer, tie-break is pinned to today's `handleKey` case
   order (early tier before late tier, then source order) so a config with an
   accidental duplicate keeps its current winner. `NotificationToggle` (`3411`)
   must still beat `Quit` (`3582`).
2. **Prefix shadowing** — sequence A is a strict prefix of sequence B. A can
   never fire without a timeout. **Across layers, resolved by layer, not by
   length**: the higher-layer binding survives, the loser is dropped with a
   warning. This matters — a user override `pane.rename = "ctrl+b"` under the
   tmux preset must be able to reclaim the prefix key as a chord. A length-based
   rule would let a preset veto an explicit user override, inverting the whole
   layering model.

   **Within one layer, the shorter sequence is refused.** A user can write
   `pane.rename = "ctrl+b"` and `tab.new = "ctrl+b c"` in the same `[bindings]`
   table; the layers tie, so the cross-layer rule gives no answer. Length is
   safe as the intra-layer tie-break precisely because no cross-layer inversion
   is possible there. Warn on the refusal.

   **Drop granularity is per-alternative.** If
   `tab.new = "${prefix} c, ctrl+t"` collides on `ctrl+t` only, just that
   alternative is dropped — the binding keeps its surviving alternatives.
3. **Hardcoded-key collision** — binding head collides with `f1`, `ctrl+n`, or
   `alt+1`…`alt+9` (`model.go:3119-3126`, handled at `3713-3718`). Warn. Also
   warn on `esc` / `enter` as binding heads — both carry selection and dialog
   semantics.

   **The paste aliases are NOT on this list**, deliberately. `ctrl+alt+v` and
   `f8` (`model.go:3684`) become alternatives on `pane.paste` in the `default`
   preset — `pane.paste = "ctrl+v, ctrl+alt+v, f8"` — rather than staying
   hardcoded. That makes them visible in F1, user-removable, and removes a
   compound case from the late switch. Caveat for `docs/keybindings.md`: a user
   who rebinds `pane.paste` to a single chord loses `F8`, and Windows Terminal
   never delivers `Ctrl+V` — so the docs must say plainly that Windows users
   should keep an `F8` alternative.
4. **Plugin `raw_keys` collision** — warn only; per-pane, not statically
   resolvable. Runtime precedence follows the tier rules above: a single chord
   keeps today's early/late relationship, a pending sequence outranks raw keys.

Surfaced as warning rows in F1 → Shortcuts, not only the log. A silently dropped
binding is worse than a loud one.

## The tmux preset

| tmux | Action | Notes |
|---|---|---|
| `c` | `tab.new` | new-window |
| `,` | `tab.rename` | rename-window |
| `&` | `tab.close` | kill-window |
| `%` | `pane.split_h` | side by side |
| `"` | `pane.split_v` | top/bottom |
| `x` | `pane.close` | kill-pane |
| `z` | `pane.focus_toggle` | zoom — Quil's focus mode is an exact match |
| `←↑↓→` | `pane.left/up/down/right` | spatial nav already matches tmux |
| `o` | `pane.next` | `NextPane` exists but ships unbound |
| `1`–`9` | `tab.switch_1..9` | **new actions** |
| `n` / `p` | `tab.next` / `tab.prev` | **new actions — Quil has neither** |
| `d` | `app.quit` | quit leaves the daemon running: detach semantics exactly |
| `[` | `pane.scroll_page_up` | approximates copy-mode entry |
| `?` | `system.shortcuts` | **new action** — today reachable only via F1 → About |

**Gap:** `tab.next`, `tab.prev`, `tab.switch_1..9`, and `system.shortcuts` do not
exist as bindable actions. Tab switching is the hardcoded `alt+1`…`alt+9` block;
the shortcuts dialog is a submenu item. The preset requires promoting all four
groups into the registry.

`tab.switch_N` is **nine discrete actions**, mirroring `alt+1`…`alt+9` rather
than a parameterized action the registry has no other use for. Note the
off-by-one: tmux windows are 0-indexed, Quil tabs are 1-indexed and render a
1-based prefix. The preset binds `${prefix} 1`…`${prefix} 9` and leaves
`${prefix} 0` **unbound** — mapping tmux's `0` to Quil's tab 1 would double-bind
tab 1 and hide tab 9 from anyone counting from zero.

Out of scope for the preset: tmux's `:` command prompt, `t` clock, `w` window
chooser (the command palette covers the last under its own key).

## Migration

`config.go:404-418` documents why the `quick_actions` patch is in-memory only:
*"a startup write from every process that loads config (TUI, daemon, MCP bridge)
would race the same file."* The migration must not repeat that mistake.

Rules:

- **TUI-only.** Verified: `Keybindings` is read exclusively by `internal/tui` and
  `internal/config`. The daemon and MCP bridge never touch it.
- **Atomic create with `O_EXCL`.** Two TUI clients can attach concurrently.
- **Abort if `Load` returned an error.** A transiently malformed `config.toml`
  otherwise migrates as pure defaults and *permanently discards* the user's
  customizations — the migration is one-way.

Steps, on first launch with no `bindings.toml`:

1. Load `config.toml`.
2. Apply existing legacy patches **first**, notably the `quick_actions` rewrite
   at `config.go:419`. Running before it would faithfully preserve a binding the
   project deliberately killed.
3. Diff each field against `config.Default().Keybindings`.
4. Write `preset = "default"` plus **only the differing fields**.
5. Log each override adopted.

The diff is load-bearing. Most installs carry all 41 fields on disk from a past
`Save`; copying rather than diffing produces 41 overrides and pins every user to
today's defaults permanently — the exact bug this move exists to fix.

Accepted limitation: a user who deliberately set a value equal to the default
loses nothing functionally, only the explicitness of the entry.

### `Save` must not strip `[keybindings]` unconditionally

`Mutate` is Load+fn+Save (`config.go:440`), so the first unrelated mutation after
upgrade — a remote install writing `[remote.hosts]`, say — would delete the
legacy table from disk. If `bindings.toml` was never successfully written, or the
user deleted it to reset, the migration source is gone. An auto-update rollback
compounds it: the old binary re-emits defaults while customizations live only in
a file it cannot read.

Two-release rollout, stated as a sequence rather than a pair of rules:

- **Release N** — `Save` always emits `[keybindings]`, exactly as today.
  `bindings.toml` is written and read, but the legacy table stays on disk as a
  fallback the previous binary can still use.
- **Release N+1** — `Save` strips `[keybindings]` **iff `bindings.toml` exists
  on disk**. If it is missing (write error, or the user deleted it to reset),
  the legacy table survives and remains the migration source.

## Rewrite scope beyond `model.go`

Every site reading `cfg.Keybindings` must move to the registry, or a user who
rebinds a key finds it dead in exactly those modes:

| Site | Reads |
|---|---|
| `ctxmenu.go:484` | full `kb` — Quit while menu open |
| `dialog.go:362` | full `kb` — `shortcutsList` |
| `dialog.go:668`, `dialog.go:2033` | `Paste`, via **raw `==`** |
| `overlay.go:251` | full `kb` — lazygit overlay |
| `palette.go:286` | full `kb` — palette shortcut column |
| `reconnect.go:403` | `Quit`, via `isFreezeEscape` |
| `model.go:870` | `reconnectResumeKey` |

The two `dialog.go` sites are a **live bug**: `key == m.cfg.Keybindings.Paste` is
a raw string compare, so the shipped multi-binding syntax never matches there.
`paste = "ctrl+v,f8"` is silently dead in those dialogs today. The registry
migration fixes it as a side effect; worth a changelog line.

## Testing

Table-driven per the repo's Go testing conventions.

- Parser: sequences, alternatives with and without space, expansion, malformed
  input.
- Chord canonicalization: modifier order, alias folding, round-trip against
  Bubble Tea's `key.String()` for every chord any shipped preset uses.
- Trie: exact / partial / none, and the `prefix prefix` literal escape.
- Conflicts: one case per rule, including the layer-aware resolution in rule 2
  (user override reclaiming the prefix key must win).
- **Tier preservation needs a behavioural test, not just a table.**
  `TestActionTiers_MatchLegacySwitchOrder` can only compare tier assignments
  against a hand-written expected table once the pre-rewrite order no longer
  exists in code — that is a change-detector, a second copy of the same data.
  The real net is `TestDispatch_RawKeysPrecedence`: drive a pane whose plugin
  declares `raw_keys` through `Update` with a key bound to an early action (the
  plugin must lose) and one bound to a late action (the plugin must win). Keep
  both tests; only the second can fail for the right reason.
- Step-0 probe: a late-tier sequence completes end to end through `Update`
  (`pane.close = "${prefix} x"` on a pane with `raw_keys` claiming `x`).
- `TestDefaultPreset_MatchesLegacyDefaults` — pins `default` against
  `config.Default().Keybindings`. Without it, a preset edit silently changes
  every existing user's keymap.
- `TestEveryPreset_NoConflicts` and `TestEveryPreset_CoversEveryAction`
  (resolves to a binding or an explicit `""` after merge).
- Migration: a 41-field legacy config yields **0** overrides; one customization
  yields **1**; a `Load` error yields **no file**.
- **Prefix sequences driven through `Update`, not through `Match` directly.** A
  direct-call test and its mutation can both pass against a decision the call
  site makes unreachable — this repo has hit that trap before.
- Notes mode: a sequence completing while the editor is focused runs the action
  without tearing down notes for `Partial` steps.

## Staging

| Stage | Content | Est. |
|---|---|---|
| 1 | `internal/keymap`: registry + chords + parser + trie + conflicts. Two-tier dispatch replacing both switches. All non-`model.go` readers migrated. Single-chord only. | ~2d |
| 2 | Prefix state machine, inert-mode guards, notes policy, status indicator, literal escape | ~2-3d |
| 3 | `bindings.toml`, presets, migration, `Save` gating, F1 viewer, docs | ~2d |

**Stage 1 is not zero-visible-change**, contrary to the first revision of this
spec. `shortcutsList` (`dialog.go:361-440`) contains rows a 41-action registry
cannot produce: selection keys, an Editor section, section headers, the
"Tab/Shift+Tab → PTY" note, and conditional `NextPane`/`PrevPane` rows. Deriving
it needs pseudo-entries in the registry, or the F1 content changes. Same for the
palette's stateful gates and per-pane go-to rows. Budget for pseudo-entries.

## Out of scope

- **Binding scopes** as a general mechanism. `Tier` and `NotesBehaviour` are
  targeted attributes, not a scope system.
- **tmux command prompt** (`:`) and tmux command names.
- **Per-plugin keymaps.** `raw_keys` already covers pass-through.
- **A GUI binding editor.** The F1 viewer is read-only.

## Implementation notes

- All paths derive from `config.QuilDir()`, never a literal `~/.quil` — dev-mode
  isolation depends on it.
- Keymap load must stay **out of `Model` construction**. ~46 tests build a
  `Model` directly without `QUIL_HOME`; a disk read in the constructor points
  every one of them at the real `~/.quil`. Follow the `SetRecentCWDs` precedent —
  pure setter on the Model, load in `cmd/quil/main.go`.

## Docs to update

`docs/keybindings.md`, `docs/configuration.md`, and a new `.claude/rules/` entry
scoped to `internal/keymap/` for the engine's invariants.

Three things the docs must answer, because each generates a support question the
rules resolve only as a log warning:

- **A prefix that collides with an inherited chord silently degrades.**
  `prefix = "ctrl+w"` under the tmux preset makes every preset sequence shadow
  whatever inherited default chord shares that head. Coherent per conflict rule
  2, invisible except in F1's warning rows. Say so, and say to check F1 after
  changing `prefix`.
- **Windows paste.** Rebinding `pane.paste` to a single chord loses `F8`, and
  Windows Terminal never delivers `Ctrl+V`. Keep an `F8` alternative.
- **Presets replace, they do not add.** Selecting `tmux` removes `Ctrl+T` and
  `Ctrl+W`. Show the override syntax for getting them back.
