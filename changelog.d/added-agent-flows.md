---
headline: Run an epic through analyst, developer and reviewer panes
---
- Start an agent flow from the command palette. Quil creates a worktree and three role panes, hands off planning, implementation and review, and shows the current stage and PR in the sidebar. Blocked or interrupted steps pause for an explicit resume; F1 → Settings → Flows edits agents, prompts, toggles and limits.
- Flow agents report structured results with the new `report_step` MCP tool. Flows survive daemon restarts with running steps paused, and each flow pane gets a restricted Quil MCP server exposing only `report_step` and its own local `get_task`, without changing global agent settings.
