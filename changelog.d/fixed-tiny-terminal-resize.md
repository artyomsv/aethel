---
headline: A console-less launch no longer squashes every pane
---
- **A quil client started with no terminal attached no longer reflows the whole
  workspace to one column.** Launching `quil.exe` from a script or a
  non-interactive shell — an unrecognised flag such as `--version` is the usual
  way in — gives the TUI a 1x1 geometry. That size was pushed straight out to
  every pane on every daemon, so Claude Code, lazygit and every shell re-wrapped
  their entire transcript to a single column, permanently.

  The TUI already refuses to *paint* below 40x10 ("Terminal too small"). It now
  refuses to *send* pane sizes below the same threshold, so the panes keep the
  last size a usable terminal produced and are resized normally once one is
  reported again. The daemon additionally declines a resize that collapses a
  pane to 1x1 in both dimensions, which no genuine split can ask for.
