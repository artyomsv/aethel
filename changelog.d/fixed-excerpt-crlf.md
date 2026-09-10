---
headline: Task results and event excerpts show real terminal output again
---
- **`delegate_task` results and notification excerpts were empty or a stray fragment
  for real terminal output.** The excerpt helper treated the carriage return that ends
  every PTY line (`\r\n`) as an overwrite and dropped the line, so `get_task` /
  `wait_task` returned no `result` for a shell command and `task_done`, `agent_idle`
  and `output_idle` events carried a blank or meaningless `excerpt`. A trailing carriage
  return is now trimmed before the overwrite reset; a carriage return with text after
  it still collapses to what the terminal shows.
