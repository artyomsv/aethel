---
headline: Restarting a Codex pane keeps its conversation
---
- **Restarting a Codex or opencode pane no longer throws the conversation away.**
  `Alt+R` — and the MCP `restart_pane` tool — respawned the agent with no resume
  argument, so it came back as a brand-new session with the live one abandoned and
  no way to reach it again. Claude Code panes already reattached correctly; the
  restart path simply never asked for the other agents' recorded session, even
  though the daemon had it on disk the whole time.

  A restart now rejoins the session the pane's own hook recorded, and keeps the
  toggles the pane was created with. A pane with nothing recorded still starts
  clean rather than guessing at the most recent session in the folder, which on a
  multi-pane workspace would pick up a sibling pane's conversation.
