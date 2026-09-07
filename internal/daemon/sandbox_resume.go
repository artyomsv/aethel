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

// claudeConfigDirForPane answers which config directory a pane's sessions live
// under: the daemon's own for a host pane, the pane's own for a sandbox one.
//
// The session PICKER needs this as much as resume does. It resolves through
// claudesessions.List, which reads the DAEMON's $CLAUDE_CONFIG_DIR — so for a
// sandbox pane it would list the host's sessions and hand claude an id its own
// config directory has never seen.
func claudeConfigDirForPane(pane *Pane) string {
	if pane == nil {
		return ""
	}
	pane.PluginMu.Lock()
	sandboxed := pane.SandboxImage != ""
	pane.PluginMu.Unlock()
	if !sandboxed {
		return ""
	}
	return sandboxClaudeConfigDir(config.QuilDir(), pane.ID)
}
