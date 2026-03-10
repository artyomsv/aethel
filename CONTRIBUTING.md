# Contributing to Aethel

## Prerequisites

- Go 1.24+ (or Docker — see below)
- Git

## Building

### With Go installed

```bash
make build
```

### With Docker (no local Go required)

```bash
docker run --rm -v "$(pwd):/workspace" -w /workspace golang:1.24-alpine \
  sh -c "go build -o /dev/null ./cmd/aethel && go build -o /dev/null ./cmd/aetheld"
```

## Testing

```bash
# Run all tests
make test

# With race detector (requires CGo)
make test-race

# Single package
go test -v ./internal/config/...
```

## Project Structure

```
cmd/
├── aethel/          # TUI client entry point
└── aetheld/         # Daemon entry point
internal/
├── config/          # TOML configuration loading
├── daemon/          # Session manager, message routing, daemon lifecycle
├── ipc/             # IPC protocol, client, server
├── pty/             # Cross-platform PTY (Unix via creack/pty, Windows via ConPTY)
└── tui/             # Bubble Tea model, tabs, panes, styles
```

## Code Conventions

- **Formatting:** `gofmt` — enforced by build. Use tabs for Go files.
- **Naming:** Follow Go conventions — exported names are PascalCase, unexported are camelCase.
- **Build tags:** Platform-specific code uses `//go:build` tags (not `// +build`).
- **Error handling:** Return errors, don't panic. Wrap errors with context using `fmt.Errorf("context: %w", err)`.
- **Tests:** Place tests in the same package (`_test.go` suffix). Use table-driven tests where appropriate.

## Platform-Specific Code

PTY and IPC layers have platform-specific implementations:

| File | Platforms |
|---|---|
| `internal/pty/session_unix.go` | Linux, macOS, FreeBSD |
| `internal/pty/session_windows.go` | Windows |

When modifying these, ensure the `Session` interface in `session.go` is satisfied on all platforms. Verify with:

```bash
make cross
```

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(daemon): add state persistence
fix(tui): correct pane resize on tab switch
refactor(ipc): simplify message encoding
test(pty): add resize test for Unix
docs: update architecture decisions
```

- Imperative mood: "add" not "added"
- Max 72 characters on the first line
- Include body for non-trivial changes

## Branch Naming

```
feature/state-persistence
fix/pane-resize-crash
chore/update-dependencies
```

## Pull Requests

- One logical concern per PR
- Title follows Conventional Commits format
- Include a summary and test plan in the description
- Keep PRs under 400 lines when possible

## Architecture Decisions

When making significant design choices, document them in [ARCHITECTURE.md](ARCHITECTURE.md) using the ADR format:

```markdown
## ADR-N: Title

**Decision:** What was decided.

**Context:** Why this decision was needed.

**Consequences:** What follows from this decision.
```
