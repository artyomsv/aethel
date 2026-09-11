---
headline: Run an epic through analyst, developer and reviewer panes
---
- Start an agent flow from the command palette. Quil creates a worktree and three role panes, hands off planning, implementation and review, and shows the current stage and PR in the sidebar. Blocked or interrupted steps pause for an explicit resume; F1 → Settings → Flows edits agents, prompts, toggles and limits.
- The New flow dialog picks the repository (←/→ through the daemon's git discovery, or a typed path; a missing directory is refused), role panes open as analyst | developer / reviewer, each role can pin a model id that reaches the agent's own `--model` / `-m` flag, and the shipped prompts are full role briefs for planning with `gh` issues, implementing with tests and one PR, reviewing with ranked comments, and fixing on the same PR.
- Flow agents report structured results with the new `report_step` MCP tool. Flows survive daemon restarts with running steps paused, and each flow pane gets a restricted Quil MCP server exposing only `report_step` and its own local `get_task`, without changing global agent settings.
