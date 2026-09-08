package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/artyomsv/quil/internal/sandbox"
)

// Seeding a codex sandbox pane's credential from the host.
//
// MEASURED before it was written: a container given the host's
// ~/.codex/auth.json under CODEX_HOME runs `codex exec` against the user's own
// plan and answers — no sign-in, no device code. Without it every codex pane
// opens on Codex's three-way sign-in menu, and option 1 cannot work from a
// container at all (the OAuth callback goes to a localhost the host browser
// cannot reach).
//
// This is a DIFFERENT posture from the Claude path, deliberately and visibly.
// Quil never copies a Claude credential — `claude setup-token` mints one for
// this purpose and Anthropic's authentication policy speaks directly to
// intermediating theirs. Codex ships no equivalent minting command, and copying
// its auth file is the mechanism its own users use for containers. It stays a
// COPY rather than a mount for two reasons: the container must not be able to
// write back over the host's credential, and a bind-mounted file cannot be
// replaced from inside when codex refreshes its token.

// codexAuthFile is the credential codex reads from CODEX_HOME.
const codexAuthFile = "auth.json"

// codexHomeEnv lets a user point at a non-default codex home, matching codex's
// own variable. Read through a package var so a test can drive it without
// touching a real home directory.
var hostCodexHomeFn = hostCodexHome

func hostCodexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// seedCodexAuth copies the host's codex credential into the pane's own codex
// home, unless one is already there.
//
// NEVER overwrites: once the pane has an auth.json it is the pane's own, and
// codex rewrites it when it refreshes. Clobbering would replace a live
// credential with a staler copy of the host's.
//
// Absent host credential is not an error. The pane then shows Codex's own
// sign-in, which is the behaviour without this function and is correct for a
// user who has never run codex on this machine.
func seedCodexAuth(m sandbox.Mapping) error {
	dst := filepath.Join(m.HostCodexHome(), codexAuthFile)
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat codex auth: %w", err)
	}

	hostHome := hostCodexHomeFn()
	if hostHome == "" {
		return nil
	}
	src := filepath.Join(hostHome, codexAuthFile)
	body, err := os.ReadFile(src)
	if err != nil {
		// Missing is the ordinary case for a user who has not signed in to
		// codex on this machine. Anything else is worth neither failing the
		// spawn nor logging the path of.
		return nil
	}

	// 0600, and the directory is already 0700: this is a credential sitting in
	// a directory the container can write, so it gets the narrowest mode the
	// container user can still read.
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return fmt.Errorf("write codex auth: %w", err)
	}
	return nil
}
