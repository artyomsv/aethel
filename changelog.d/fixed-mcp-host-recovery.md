---
headline: MCP host lists reconnect recovered remote daemons
---

- Unscoped MCP tools and `list_hosts` now retry disconnected remote hosts in the
  background after the retry backoff, without waiting for SSH. A recovered host
  reappears automatically and its stale connection and request errors are cleared.
