---
headline: The inert [security] config section is gone
---

- **The `[security]` section is removed from `config.toml`.** It carried
  `encrypt_tokens` and `redact_secrets`, both defaulting to `true` and **neither
  ever read by any code**: token encryption was never built, and MCP log
  redaction is unconditional and stays that way — an off switch for secret
  redaction is not a feature worth having. The file was advertising two
  guarantees nothing provided, which is worse than saying nothing.

  Nothing changes in behaviour. A `config.toml` that still carries the section
  loads exactly as before — the table is ignored — and it disappears the next
  time Quil saves the file. If encrypted token storage lands later, it
  introduces its own key then.
