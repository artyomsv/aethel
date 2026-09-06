---
name: plugin-toml-capability-derivation
description: Deriving a daemon capability from a user-editable plugin TOML field turns a documented user option into a silent kill switch — check the field's documented meaning before accepting such a refactor
metadata:
  type: project
---

When a refactor in this repo replaces `p.Name == "<plugin>"` dispatch with a
predicate derived from a `[command]` / `[persistence]` TOML field, check what
that field is DOCUMENTED to mean in `internal/plugin/plugin.go` and in
`internal/plugin/defaults/<plugin>.toml` before accepting it.

**Why:** plugin TOML under `PluginsDir()` is user-editable by design (the
shipped files literally say "Edit this file to customize the plugin"), and
`EnsureDefaultPlugins` NEVER overwrites an existing file — a stale one only
raises a dismissable migration dialog. So any field a user is invited to change
becomes a kill switch for whatever capability got derived from it. Seen on
PR #174 (`fix/claude-session-seam`): `UsesClaudeSessions()` was derived from
`Command.Sessions`, a field documented as a *setup-dialog affordance* whose own
default TOML says `Set to "" to always start a fresh session` — setting it
disabled hook injection, all Claude notification/work-state/history hooks, and
the per-pane `--resume` promotion.

**How to apply:** for each such derivation ask (1) is the source field
documented as a UI affordance or as a protocol capability, (2) is it optional /
absent in older on-disk copies, (3) does `registry.go`'s validation constrain it
to values consistent with the new meaning, and (4) does a `switch` that used to
be disjoint by construction (`switch p.Name`) stay disjoint. A registry MISS
answering false is usually fine — `spawnPane` (`daemon.go:3672-3674`) already
degrades an unresolvable type to `terminal` — but a plugin that loads with the
field cleared is a different, silent case. Related: [[verify-claims-in-container]].
