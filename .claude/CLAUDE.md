# Aethel — Project Instructions

## What is this?

Aethel is a persistent workflow orchestrator / terminal multiplexer for AI-native developers. Written in Go with Bubble Tea TUI.

## Tech Stack

- **Language:** Go 1.24
- **Module path:** `github.com/artyomsv/aethel`
- **TUI:** Bubble Tea v1 (`github.com/charmbracelet/bubbletea` v1.3.10)
- **Styling:** Lipgloss v1 (`github.com/charmbracelet/lipgloss` v1.1.0)
- **PTY (Unix):** `creack/pty/v2`
- **PTY (Windows):** `charmbracelet/x/conpty` v0.2.0
- **Config:** TOML via `BurntSushi/toml`
- **IDs:** `google/uuid`

## Architecture

Client-daemon model:
- `cmd/aethel/` — TUI client (Bubble Tea)
- `cmd/aetheld/` — Background daemon
- `internal/config/` — TOML configuration
- `internal/daemon/` — Session manager, message routing
- `internal/ipc/` — Length-prefixed JSON protocol (4-byte big-endian uint32 + JSON)
- `internal/pty/` — Cross-platform PTY (build tags: `linux || darwin || freebsd`, `windows`)
- `internal/tui/` — Bubble Tea model, tabs, panes, styles

## Building

Go and make are NOT installed locally. Use `dev.sh` (Docker-based):

```bash
./dev.sh build        # Build both binaries
./dev.sh test         # Run tests
./dev.sh test-race    # Tests with race detector (CGo — handled automatically)
./dev.sh vet          # Lint
./dev.sh cross        # Cross-compile all platforms
./dev.sh image        # Build scratch-based Docker image
./dev.sh clean        # Remove built binaries
```

Go module cache is persisted in a Docker volume (`aethel-gomod`) for fast repeated builds.

## Key Conventions

- Platform-specific code uses `//go:build` tags (not `// +build`)
- ConPTY API: `conpty.New(width, height, flags)` — 3 args, uses `Spawn()`, reads/writes directly on ConPty object
- Bubble Tea v2 / Lipgloss v2 are NOT available — use v1 import paths
- IPC protocol: 4-byte big-endian length prefix + JSON payload
- `.gitignore` uses root-anchored patterns (`/aethel`, `/aetheld`) to avoid matching `cmd/` directories

## Documents

- `PRD.md` — Full product requirements document
- `VISION.md` — Project vision
- `ARCHITECTURE.md` — Architecture Decision Records
- `CHANGELOG.md` — Keep a Changelog format
- `docs/plans/` — Implementation plans

## Milestones

- **M1 (Done):** Foundation — daemon, TUI, IPC, PTY, tabs, splits
- **M2 (Next):** State persistence — snapshots, ghost buffers, reboot-proof sessions
- **M3:** Resume engine — regex scrapers, AI session resume
- **M4:** Plugin system — TOML plugins, typed panes
- **M5:** Polish — JSON transformer, observability, encrypted tokens
