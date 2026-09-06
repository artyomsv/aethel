---
headline: The notification sidebar is now a timeline you can click
---
- **The notification sidebar shows work, not telemetry.** `Output idle` and
  `Command completed` fired for every quiet pane and every shell command, and
  because the queue merges repeats and moves the merged card back to the top,
  the two of them permanently occupied the handful of rows the sidebar could
  draw. Both are now off by default. What is left is the timeline: turns
  starting and finishing, permission prompts, processes exiting, panes closed
  or pinned, MCP agents taking a pane, and worktrees finishing their checkout.
- **Click a card to jump to its pane, and scroll the list with the wheel.**
  Both gestures were previously swallowed and discarded. Cards now name the
  project and tab they will take you to, shrink when they carry no excerpt — so
  about twice as many fit — and mark themselves `(closed)` when their pane is
  gone, instead of offering a jump that silently does nothing.
- **Choose what appears in `F1 → Settings → Notifications`.** Ten event groups
  plus the desktop-toast switches, two of which (`blocked` and `done`) were
  previously reachable only by hand-editing `config.toml`. Changes apply
  immediately. Press `a` in the focused sidebar to reveal everything for a
  moment without changing the setting.
- **Hiding a group never hides it from MCP agents.** `get_notifications` and
  `watch_notifications` still receive every event: an agent polling for "has
  this pane gone quiet" wants exactly what a human does not.
