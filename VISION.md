# Aethel

**The Persistent Workflow Orchestrator for AI-Native Development**

## 1. Executive Summary

Aethel is a cross-platform terminal manager designed to eliminate "context loss." Unlike traditional terminal emulators or multiplexers (like tmux), Aethel is **project-aware**. It doesn't just manage shells; it manages workflows by persisting the state of AI sessions, webhooks, and build tools even across full system reboots.

## 2. Core Architecture: "The Brain"

- **Language:** Go (Golang) for high-concurrency and easy cross-platform binaries.
- **Engine:** Internal Multiplexer (built via `creack/pty`). No dependency on tmux.
- **UI:** Bubble Tea for a rich, interactive TUI.
- **Model:** Client-Server Architecture.
  - `aetheld` (Daemon): A background server that maintains PTY sessions and monitors process output.
  - `aethel` (Client): The frontend TUI that attaches to the daemon.

## 3. Key Feature Pillars

### A. Total State Persistence (Reboot-Proof)

- **Continuous Snapshotting:** Aethel saves a `state.json` mapping of every Tab and Pane (Working Directory, Layout, Type, and Metadata).
- **Ghost Buffer:** Upon OS restart, Aethel immediately renders the last 500 lines of cached text for every pane, providing instant visual context while the underlying shells re-initialize.
- **Process Re-hydration:** On startup, Aethel doesn't just open a shell; it executes the Abstract Resume Command for that specific pane.

### B. Abstract Resume Engine

Aethel uses a "Template & Scraper" model to ensure tools such as Claude Code or SSH sessions are never lost.

- **The Scraper:** A background regex listener that "watches" terminal output for Session IDs or Context Tokens (e.g., `Conversation ID: ([a-z0-9-]+)`).
- **The Template:** Users define an abstract command string per pane (e.g., `claude --resume {{.SessionID}}`).
- **Universal Support:** This allows Aethel to "resume" any CLI tool (Claude, Gemini, Docker, SSH, etc.) without hardcoded logic.

### C. Typed Panes (Functional Workspaces)

Panes are assigned a Type with specialized behaviors:

- **AI Pane:** Optimized Markdown rendering and automatic session-id extraction.
- **Webhook Pane:** Real-time monitors (Stripe/Twilio). Borders flash Orange on activity or Red on errors. Supports auto-restart if the listener crashes.
- **Infrastructure Pane:** Displays persistent status lines (e.g., `K8s Context: production`) to prevent accidental commands.
- **Build Pane:** Integrated "Quick Actions" for Maven/Gradle/NPM. Tab colors reflect Success (Green) or Failure (Red).

### D. Advanced Layout & UI

- **Dynamic Positioning:** Tabs can be docked to Top, Bottom, Left, or Right.
- **Visual Logic:**
  - **JSON Transformer:** A hotkey (`Ctrl+J`) to toggle between Raw, Minified, and Pretty-Printed JSON with syntax highlighting.
  - **Pane Naming:** Every pane can be manually named or dynamically named based on the running process.
  - **Split-Views:** Support for infinite nesting of vertical and horizontal splits within a single tab.

## 4. Technical Requirements

| Requirement          | Implementation Detail                                                                |
|----------------------|--------------------------------------------------------------------------------------|
| Persistence          | SQLite or JSON-based state storage in `~/.aethel/`.                                   |
| Rendering            | GPU-aware via Windows Terminal or WezTerm as the host.                               |
| Shell Support        | Native ConPTY for PowerShell/CMD; PTY for Bash/Zsh.                                 |
| Syntax Highlighting  | Integrate Chroma for JSON/Code formatting.                                           |
| Networking           | Unix Sockets (Linux/Mac) and Named Pipes (Windows) for Client-Server communication. |

## 5. User Persona: The "AI-Native" Developer

The user is tired of re-typing `claude --resume` and re-opening five different project tabs every morning. They want to type one command — `aethel` — and have their entire multi-tool environment snap back into existence exactly as it was when they went to sleep.
