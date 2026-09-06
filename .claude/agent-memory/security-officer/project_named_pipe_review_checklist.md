---
name: named-pipe-review-checklist
description: Windows named pipes in this repo need three independent controls — FILE_FLAG_FIRST_PIPE_INSTANCE, an explicit DACL, and SECURITY_SQOS_PRESENT on the CLIENT dial; the third is the one that keeps getting missed
metadata:
  type: project
---

`\\.\pipe\` is a machine-global, cross-session namespace and every quil pipe
name is predictable (`quil-activate-<pid>`; PIDs are small and enumerable).
Three separate controls are needed and they protect different sides:

| Control | Side | Missing it means |
|---|---|---|
| `FILE_FLAG_FIRST_PIPE_INSTANCE` | server (`CreateNamedPipe`) | you silently JOIN a squatter's pipe object and their SD governs |
| explicit DACL (`D:P(A;;GA;;;<user SID>)`) | server | default SD lets more than the current user in |
| `SECURITY_SQOS_PRESENT \| SECURITY_ANONYMOUS` | **client** (`CreateFile`) | a squatting pipe server can `ImpersonateNamedPipeClient` and wear your token |

**Why the third is the trap:** the first two are visible in the server code and
this repo comments them at length, so a reviewer reading `Listen` concludes the
pipe is locked down. The client dial is a different function
(`dialActivatePipe`), looks like an ordinary `CreateFile`, and its default is
`SecurityImpersonation` — the *most* permissive level, chosen by omission. The
two controls also interact backwards: `FIRST_PIPE_INSTANCE` makes the victim's
listener FAIL when squatted, so the feature degrades quietly while the client
keeps dialling into the attacker's pipe.

`golang.org/x/sys/windows` v0.46.0 exports both constants
(`types_windows.go:3491`, `:3499`) — no local declaration needed. `go-winio`
passes `SECURITY_SQOS_PRESENT|SECURITY_ANONYMOUS` on every dial for exactly
this reason.

**How to apply:** any new `\\.\pipe\` producer/consumer in `internal/notify`,
`internal/ipc`, `internal/pty` or `cmd/quil*` — grep the CLIENT `CreateFile`
for `SQOS` first, not the server. Also check the client verifies nothing about
the server's identity (it usually cannot, which is why the flag is the fix).
Related: [[uintptrescapes-com-helper]] — same failure mode, a Windows-only
file CI never compiles, so no test or vet run reaches it.
