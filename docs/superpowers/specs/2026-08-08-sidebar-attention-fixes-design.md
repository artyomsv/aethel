# Sidebar and attention-model fixes — design

**Date:** 2026-08-08
**Base:** `origin/master` @ `71535ce` (v1.52.2)
**Branch:** `worktree-sidebar-attention-fixes`

Five defects reported from a live workspace. Item 3 of the original six (same-tab
pane switching via the sidebar) was retested by the project owner and works — it
is dropped, not deferred.

Every line reference below was re-derived against `71535ce`. The original research
was done against `feat/keybinding-registry` and its `model.go` numbers are ~50
lines high; do not trust a stale citation.

---

## Item 1 — a working pane reads as "blocked on you"

### Root cause

`ClassifyWorkEvent` (`internal/hookevents/workstate.go:77`) maps
`Notification` / `PermissionRequest` to `WorkEventPark` with a **match-all**
trigger. The resume edge is `PostToolUse` (`workstate.go:50`), which Claude only
fires for `AskUserQuestion|ExitPlanMode` — narrowed **twice**, at registration
(`promptToolMatcher`) and again defensively in `runhook.go:145` (`isPromptTool`).

Approving a `Bash`/`Edit`/`Write` permission prompt therefore produces **no
resume edge at all**. `workPark` (`internal/tui/workstate.go:286`) has already
set `turnActive = false`, and the code says so itself at `workstate.go:268-271`:

> *"Approving a permission prompt fires no hook of its own: the pane's next event
> is the turn's Stop, so this is the only edge that un-blocks it."*

So for the entire time the agent is actually working:

- the pane row paints `▲` amber — blocked outranks working in `paneRow`
  (`sidebar.go:621`)
- the project badge counts it blocked, not working — same precedence in
  `counts()` (`sidebar.go:77`)
- the tab bar shows no spinner — `tabHasWorkingPane` is false

### Fix — park stops meaning "not working"

Delete `pane.turnActive = false` from the `workPark` arm
(`internal/tui/workstate.go:286`). Park then sets `blockedSince` and nothing else.

This is sound because `Notification` covers **two** situations:

| Situation | `turnActive` when the park arrives | Effect of the change |
|---|---|---|
| Permission prompt | `true` — turn still running | Spinner correctly stays on |
| Idle-wait nudge | `false` — `Stop` already fired | No-op |

The deleted assignment is a no-op exactly when it was right and wrong exactly
when it wasn't.

### Consequence that must ship with it

`working` no longer falls on a park, so the falling edge that sets `unseen`
(`workstate.go:305`) does not fire — which is what currently turns the tab
label green for a parked background pane. **Item 6.1 (`tabBlocked`) is therefore
not optional**; without it a parked pane in a background tab has no tab-level
mark at all. Items 1 and 6 are one change.

### Rejected alternatives

- **Widen the `PostToolUse` matcher.** Precise, but one hook process per matched
  tool call — the exact overhead the narrow matcher was introduced to avoid — and
  it resumes at command *completion*, not at approval.
- **Output-driven unpark** (sustained PTY output = resume edge). Recommended by
  the earlier research, rejected here: Claude Code's permission dialog *animates
  while parked*, so a byte-threshold cannot separate "dialog redrawing" from
  "agent working".

---

## Item 2 — sidebar tab name unreadable, no ordinal

### Root cause

`sidebarTabHeading` (`sidebar.go:549`) paints an inactive tab with
`sidebarDimStyle`. An idle pane row (`paneRow`, `sidebar.go:642`) paints with
`sidebarDimStyle`. Identical by construction — the tab heading and the rows under
it are the same colour.

### Fix

`sidebarTabHeading(name string, active bool, w int)` →
`sidebarTabHeading(name string, idx int, active bool, color string, w int)`,
with its one call site at `sidebar.go:186`.

- Default foreground **white (255)**; the active tab keeps `sidebarActiveStyle`.
- When `tab.Color != ""`, use it as **foreground**. The field exists (`tab.go`);
  the tab *bar* uses the same value as a *background* — reuse that conversion,
  do not invent a second one.
- Prefix the 1-based ordinal, matching the tab bar's `fmt.Sprintf("%d:%s", idx+1, …)`.

The `N:` prefix costs 2–3 cells of a 22-column row, so the name **elides** rather
than hard-truncating. All cutting goes through `truncateCells` — lipgloss stays
the sole width authority.

---

## Item 4 — right-click on a sidebar pane row does nothing

### Root cause

`model.go:1185`, inside the sidebar swallow: the `tea.MouseRight` branch tests
`kind == sidebarRowProject` and nothing else. Pane rows fall through to
`return m, nil` at `model.go:1189`.

### The wrinkle

`executeCtxMenuItem` resolves its target with `tab := m.activeTabModel()` and
bails when the pane is not in it. Simply opening the menu for any pane row would
produce a menu that **silently does nothing** for a pane in a background tab.

### Fix

Two parts, both required:

1. `model.go:1185` — accept `sidebarRowPane` and open the pane context menu for
   the row's `paneID`.
2. `executeCtxMenuItem` — resolve the owning tab via `findPaneAndTab` instead of
   `activeTabModel()`.

**Decision (revised during implementation, ruled by the project owner):**
right-click **focuses the pane first**, mirroring left-click, and then opens the
menu.

The original assumption here — that right-click should not move focus, and that
resolving the owning tab in `executeCtxMenuItem` was the same fix without the
side effect — was **wrong, and dangerously so**. That resolution only covers the
two items that act on the pane `findPaneAndTab` returned
(`ctxActAttention` / `ctxActClearAttention`). The other eight — Rename,
**Restart**, **Close**, Mute, Notes, Focus, Lazygit, History — re-resolve their
target internally through `m.activeTabModel().ActivePaneModel()`, the tab being
*viewed*. Since this task creates the first entry point that can open the menu
on a pane in a background tab, "Close pane…" there would have armed the confirm
for whatever pane was on screen.

Focusing first makes all ten correct by construction and needs no change to
those eight handlers — which are shared with the keybinding and command-palette
paths, so widening their signatures was the larger risk.

The `findPaneAndTab` / `proj.tabs[tabIdx]` resolution is **kept** as
belt-and-braces. Note it inverted the failure mode: the old
`tab.Root.FindLeaf(paneID) == nil` guard was fail-safe (a menu targeting a
non-active tab did nothing), while this one passes and falls through. Any new
entry point that opens this menu on a pane outside the active tab **must focus
it first** — the comment above that block says so, and that sentence is the
guardrail, not the resolution itself.

---

## Item 5 — the sidebar list overflows with no way to see the rest

### Root cause

`sidebarVisibleRows` (`sidebar.go:258`) hard-caps to `rows[:height]` and
overwrites the last visible row with a dim `" …"`. There is a marker but no
scrolling, and no marker for content above.

### Constraint that shapes the fix

From that function's own comment (`sidebar.go:256-257`): paint and hit-test
**both** call it, with the same height, deliberately — *"a cap applied in only
one of them is the row-drift bug in another form"*. The scroll offset must live
**inside** `sidebarVisibleRows`, never at the render site.

### Fix — the PANES section scrolls, PROJECTS stays pinned

`sidebarRows(w)` returns `([]sidebarRow, int)`, the int being the index of the
first row *after* the `PANES` heading. Deriving that boundary by scanning for the
heading text would be a second source of truth; the builder already knows it.

`sidebarVisibleRows(w, height)` then:

1. splits at the boundary into a pinned head (`PROJECTS` block + blank + `PANES`
   heading) and a scrollable body;
2. if everything fits, returns it unchanged — no markers;
3. otherwise reserves the head, and if the head alone would leave fewer than
   `minPaneRows` for the body, truncates the head with the **existing** `" …"`
   marker (the current behaviour, kept as the degenerate-terminal fallback);
4. windows the body at the clamped offset, spending one row on a top marker when
   scrolled down and one on a bottom marker when more remains.

**`sidebarVisibleRows` stays pure — it clamps a local copy and never writes
`m.sidebarScroll`.** It runs on the render path; a render that mutates model
state is how a hit test and a paint come to disagree. The wheel handler owns the
write, clamping via a shared `maxSidebarScroll(w, height)` so both use one rule.

Wheel events over the strip are already swallowed at `model.go:1466`, so the
handler has its hook point and needs no new dispatch.

Marker rows carry `kind: ""` so they are inert to `sidebarHit`.

**Marker glyph:** `⋯` (U+22EF), already used by `paneRow` for the subagent count,
so it is proven against the sidebar glyph rule. Explicitly **not** `▲`/`▼` — `▲`
is `glyphBlocked`, and a scroll marker wearing the blocked glyph is the same
class of confusion as item 1.

**Auto-scroll into view** on pane focus is included, so a pane reached from the
palette or a hook jump is not left off-screen. It is a separate task and can be
dropped without affecting the rest.

---

## Item 6 — the attention model

### Three defects

1. **Blocked never reaches the tab.** `tabStyle` (`model.go:4624`) keys on
   `tabUnseen(idx) || tabPinnedAttention(idx)`. There is no `tabBlocked` anywhere.
   A parked pane shows `▲` in the sidebar and the tab bar shows nothing.
2. **Focus doesn't clear it.** `ackFocusedPane` clears `unseen` only;
   `blockedSince` survives, so the amber `▲` stays on the pane being looked at
   until the next hook edge.
3. **Permanent attention is implemented but invisible.** `pinnedAttention` is
   toggled by `ctxActAttention` and cleared with the other two marks by
   `ctxActClearAttention` (`ctxmenu.go:569`, `585-588`), and it drives the pane
   *border* (`pane.go:879`) — but `paneRow` never renders it in the sidebar.

### Fix

**6.1 — `tabBlocked(idx)`**, mirroring `tabUnseen`, plus an amber tab style.
`tabStyle` precedence becomes **blocked > unseen > pinned > custom colour >
active/inactive**. It stays the single function shared by `renderTabBar` and
`hitTestTab`, so rendered widths and click hit-testing cannot diverge.

**6.2 — `ackFocusedPane` keeps `blockedSince` + `blockedReason`; `paneRow`
suppresses the GLYPH for the focused pane; REAL USER INPUT to a pane clears the
mark.** The ack clears `unseen` and nothing else. `pinnedAttention` deliberately
**survives** focus — that is what makes it the permanent mark.

**A glance is not an answer, but a keystroke is.** `PaneModel.answerBlockedByInput`
(workstate.go) drops `blockedSince`/`blockedReason` when typed keys or a paste
are routed to the pane. This closes the consequence the review raised after the
reversal below: approving a Bash/Edit/Write permission prompt fires **no hook at
all** (`promptToolMatcher` is `AskUserQuestion|ExitPlanMode`, so `PostToolUse`
does not cover it — the same root cause as item 1), and the pane's next event is
the turn's `Stop`. So an *answered* prompt otherwise kept its tab amber, kept
`counts()` reporting blocked rather than working, kept being offered by
`Alt+Shift+A`, and put the `▲` back the instant the user switched away.

It is wired at the producers representing a HUMAN acting on the pane — the two
`handleKey` forward paths and both paste paths — and deliberately **not** at
`enqueueInput` (the ordering choke point, which also carries forwarded wheel
notches) nor at `forwardInputBytes` (which the selection handler also uses to
walk the shell cursor during a mouse DRAG, emitting arrow-key escapes a
permission prompt would consume as a choice). Scrolling a parked pane or
dragging a selection across it is a glance with a mouse. The trigger is input
REACHING a pane, not focus, so the asynchronous paste path answers a pane that
is no longer active.

> **Revised after whole-branch review.** This item originally said the opposite
> — clear `blockedSince`/`blockedReason` alongside `unseen`, on the reasoning
> that "mark both levels, clear the pane on visit, clear the tab only when it
> held the last mark" is exactly how `unseen` already behaves, so the same
> three-level treatment should extend to `blockedSince`. It was reversed on
> **2026-08-08**, and the parenthetical above is where the analogy breaks.
>
> `ackFocusedPane` runs at the top of **every** `Update`, for every message
> type — including the shared 100 ms `workSpinnerTickMsg`, which with item 1
> fixed is *guaranteed* to be ticking because the pane is working. So a pane
> that was the focused pane of the active tab when it parked had `blockedSince`
> set and cleared roughly 100 ms later: the `▲`, the amber tab from 6.1, the
> project badge's blocked count and the attention-queue entry were **none of
> them ever observable**. That is not an edge case — it is the commonest park
> there is, the agent asking for permission while you sit in its pane.
>
> The stated mitigation made it worse rather than better. "With item 1 fixed
> the spinner keeps running, so the pane does not read as idle" is true, and
> the consequence is that the pane reads as **busy**: `◐` in the sidebar, a
> counted `working` in the project badge, a spinner on the tab and the pane
> border. Walk away without answering and there is no "needs you" signal
> anywhere in the product, plus a positive claim of progress that is false.
>
> The correction the two flags need is that they are different kinds of thing.
> `unseen` is a "you missed something" flag, which looking genuinely answers.
> `blockedSince` is a fact about the **agent** — it is still waiting whether or
> not anyone is looking — so clearing it on a spinner tick destroys information
> rather than acknowledging a notification.
>
> Keeping the state and suppressing the presentation preserves everything the
> original item asked for at the level the user is looking at: `paneRow`
> already takes `focused`, so a blocked+focused pane renders as it would with
> no blocked mark at all (falling through to working, then pinned, then unseen,
> then idle), and the blocked reason goes with the glyph. `tabBlocked`,
> `ProjectModel.counts()` and `blockedPanes()` all keep reading the same live
> flag, so every derived level stays truthful, and leaving the pane restores
> the `▲` with **no hook edge required** — which the clear-on-focus version
> could not do at all.



**6.3 — render `pinnedAttention` in `paneRow`.** A new `glyphPinned = "◆"`
(U+25C6, not emoji-capable — add it to
`TestSidebarGlyphs_OneCellAndNotEmojiCapable`) as its own case below
blocked/working, **plus** a trailing pin marker when a higher-urgency state wins,
so a pin is never invisible.

---

## Testing

Unit tests, table-driven, in the existing files (`workstate_test.go`,
`attention_test.go`, `sidebar_test.go`, `ctxmenu_dispatch_test.go`).

Two rules for this change specifically:

- **Test through `Update`, not the decision function.** A direct-call test can
  pass against code the call site makes unreachable — item 4's whole bug is a
  branch that is never entered.
- Item 5's assertions must exercise **paint and hit-test together**: window the
  rows, then assert `sidebarHit` at a screen row resolves the pane that is
  actually painted there. Testing one alone cannot catch row drift.

Existing fixtures in `sidebar_test.go` pin row layout and **will shift** — that
is the expected cost of adding rows, per the sidebar's own documented convention.

Baseline before any change: 28/28 packages green at `71535ce`.

---

## Risks

| Risk | Mitigation |
|---|---|
| Park keeping `turnActive` wedges a spinner if a turn never `Stop`s | Same exposure as today for any turn without `Stop`; `process_exit` and `SessionEnd` still clear it |
| Sidebar row fixtures break | Expected; update them in the same task that adds the rows |
| `model.go` / `ctxmenu.go` conflict with `feat/keybinding-registry` when it merges | Known and accepted — base was chosen for independent reviewability. Resolve with `git rebase --onto origin/master 71535ce` after that branch lands |
