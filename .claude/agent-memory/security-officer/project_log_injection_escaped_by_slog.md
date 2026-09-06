---
name: project-log-injection-escaped-by-slog
description: log.Printf("%s", attackerString) is NOT log-injectable in quil — logger.Init routes stdlib log through slog.NewTextHandler, which quotes the whole message; the residual is the initLogging-returns-nil path
metadata:
  type: project
---

`log.Printf` with an unsanitized attacker-controlled string (`payload.PaneID`, pane names,
CWDs, hook values) does **not** yield log injection in quil's normal runtime. `logger.Init`
(`internal/logger/logger.go:63`) builds a `slog.NewTextHandler` and bridges stdlib `log`
through `slog.NewLogLogger(handler, LevelInfo).Writer()`. The bridge makes the ENTIRE
formatted `log.Printf` string the slog record's `msg`, and TextHandler runs `needsQuoting`
on it — so newlines, `"` and `\x1b` come out as `\n`, `\"`, `\x1b` inside one quoted field.

Verified by probe (2026-09-06), not from docs. A `PaneID` of
`"p1\nlevel=ERROR msg=\"...\"\n\x1b[31m...\x1b]0;pwned\a"` rendered as a single line:
`time=... level=INFO msg="pane p1\nlevel=ERROR msg=\"...\"\n\x1b[31m...\a: refusing ..."`.
So no forged log line, and no ANSI/OSC reaching an operator's terminal on `cat`/`tail`.

**Residual, and the only place to look:** `initLogging` (`cmd/quild/main.go:177-190`)
returns `nil` — leaving `logger.Init` **never called** — when `config.QuilDir()` is empty or
`NewRotatingWriter` fails to open. Stdlib `log` then writes to raw `os.Stderr`, which
`startDaemon` points at `$QUIL_HOME/quild.stderr.log`, with no escaping at all. That is the
only path where control characters survive.

**How to apply:** do not raise `%s` vs `%q` on a payload string as a log-injection finding —
it is cosmetic here. Raise instead: (a) whether the value is length-bounded, since a payload
string can be up to `maxFrameSize` = 10 MB (`internal/ipc/protocol.go:1435`) and quild.log's
default budget is only 5 MB x 10 files, so a few frames evict the whole history; and (b)
whether the log site is rate-limited, since the IPC socket is chmod 0600
(`internal/ipc/server.go:605`) — same-UID, so the impact is anti-forensics / log eviction,
never privilege crossing. Compare `pane.LastInputBlockedAt` (daemon.go) for the repo's own
per-pane cooldown idiom. See [[project-remote-daemon-taint]] and [[project-hookevents-taint]]
for where such strings originate.
