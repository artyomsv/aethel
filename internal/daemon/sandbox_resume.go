package daemon

import (
	"path/filepath"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/sandbox"
)

// hostTranscriptPath restates a sandbox pane's recorded transcript path in the
// host's terms, or answers "" when it cannot be trusted.
//
// A sandbox pane's hook runs inside the container and records what IT saw:
// /quil/claude/projects/<escaped>/<id>.jsonl. Every consumer of that path
// stats it on the HOST, where it does not exist — so without this every
// candidate is classified missing, nothing is ever located, and a restart
// spawns `--session-id <id>` against an id whose transcript exists. Claude
// refuses that with exit 129, which is precisely the failure locatedOwnSession
// was written to prevent.
//
// It VALIDATES rather than merely rewrites, and that is the security half. The
// path is written by the agent, and transcriptExistsFn is the budgeted host
// stat — so an unchecked value is an existence oracle for any host path ending
// <id>.jsonl, and a blocking-FS permit spent on a dead mount on every restart.
// Only a path under the container's own config directory is rewritten; the
// cleaned result must still land under this pane's host config directory, so a
// "..\..\" between them escapes nothing.
//
// A rejected path answers "": callers treat that as candidateUnknown, which is
// the honest state. Guessing "missing" would discard a live session, and
// guessing "located" would resume one that is not there.
func hostTranscriptPath(pane *Pane, recorded string) string {
	if recorded == "" {
		return ""
	}
	pane.PluginMu.Lock()
	sandboxed := pane.SandboxImage != ""
	pane.PluginMu.Unlock()
	if !sandboxed {
		return recorded
	}

	slashed := filepath.ToSlash(recorded)
	prefix := sandbox.ContainerClaude + "/"
	if !strings.HasPrefix(slashed, prefix) {
		return ""
	}
	hostRoot := filepath.Join(sandboxRoot(config.QuilDir()), "panes", pane.ID, "claude")
	joined := filepath.Join(hostRoot, filepath.FromSlash(strings.TrimPrefix(slashed, prefix)))
	// filepath.Join CLEANS a traversal rather than refusing it, so the
	// containment test has to run on the RESULT, not on the input.
	if !withinHostDir(hostRoot, joined) {
		return ""
	}
	return joined
}

// withinHostDir reports whether child is dir or lives under it, comparing in
// the one normalised form the rest of the sandbox code uses.
func withinHostDir(dir, child string) bool {
	d := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(dir)), "/")
	c := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(child)), "/")
	if strings.EqualFold(d, c) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(c), strings.ToLower(d)+"/")
}

// sandboxClaudeConfigDir is where a sandbox pane's Claude data lives on the
// host — the directory the session picker must be pointed at instead of the
// daemon's own ~/.claude.
//
// Per-pane rather than shared: CLAUDE_CONFIG_DIR holds user-scope settings
// (hooks), .claude.json (MCP servers), every transcript and the prompt
// history, so one shared directory would let any sandbox pane plant a hook or
// an MCP server that every other sandbox pane's claude executes inside its own
// container.
func sandboxClaudeConfigDir(quilDir, paneID string) string {
	return filepath.Join(sandboxRoot(quilDir), "panes", paneID, "claude")
}

// There is deliberately no "which config dir does the session PICKER use"
// helper, and the reason is a property of the picker rather than an oversight.
//
// The picker runs while the create dialog is open — before the pane exists. A
// sandbox pane's Claude config directory is per-pane and is created at spawn,
// so at picker time there is no directory to list and no pane id to name one
// with (ClaudeSessionsReqPayload carries a CWD and nothing else). A brand-new
// container has no prior sessions by construction.
//
// So the honest behaviour is to OFFER NO PICKER for a sandbox create rather
// than to list the host's sessions, which is what pointing it at the daemon's
// own $CLAUDE_CONFIG_DIR would do — the user would pick a conversation the
// container has never seen and get a "No conversation found" spawn. The TUI
// hides the session field when the sandbox row is on; see the setup dialog.
//
// RESUME is the separate case and does have a pane: hostTranscriptPath above
// rewrites what that pane's own hook recorded.
