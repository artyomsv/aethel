---
headline: Agent-created panes start sized and restart cleanly
---

- New panes inherit a sibling's size or the attached terminal's dimensions before
  their first paint, including panes created through MCP in hidden tabs. An unusable
  1x1 client size falls back to 80x24 when no sibling size is available. Restarting
  a pane resets its terminal emulator before the replacement child's output arrives;
  late output and exit events from the previous child no longer corrupt its screen
  or mark the replacement as exited. Tasks assigned to the old process fail on restart.
