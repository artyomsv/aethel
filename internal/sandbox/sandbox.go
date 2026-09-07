// Package sandbox computes what it takes to run an AI pane inside a Docker
// container with its git worktree bind-mounted, so the agent's filesystem reach
// is bounded by the container rather than by the host.
//
// Everything here is pure except docker.go: NewMapping shells out to git to
// resolve a checkout, and the rest is path arithmetic over the result. That
// split is deliberate — the mount set is the security boundary of the whole
// feature, and a boundary that needs a Docker daemon to test is a boundary
// nobody tests. `dev.sh test` runs the suite inside a container with no Docker
// available, so every decision in this package is reachable from a table test.
//
// Two rules the mount arithmetic must never lose, both learned by measurement
// against a real container (see the design doc):
//
//   - Only the repository's .git enters the container, never the main
//     checkout's working tree. Mounting the checkout root hands the agent a
//     read-only copy of the user's primary source, secrets included.
//   - Container-side paths are built with plain "/" joins, never
//     filepath.Join. On a Windows daemon filepath.Join emits backslashes and
//     the container gets a path it cannot resolve. Separators belong to the
//     machine holding the disk.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// Container-side paths. Constants rather than fields on Mapping: they are the
// same for every pane, and a container path that varied per pane would be one
// more thing a caller could get wrong when it builds argv.
const (
	// ContainerRepo is where the repository's .git is mounted. Its parent
	// (/repo) holds nothing else — the main checkout's working tree is
	// deliberately absent.
	ContainerRepo     = "/repo"
	ContainerGitDir   = "/repo/.git"
	ContainerWorkRoot = "/work"
	// ContainerQuil is the pane's own subtree: the hook spool, its Claude
	// config, and its git object store. QUIL_HOOK_HOME points here, which is
	// why the hook binary needs no change to run in a container.
	ContainerQuil    = "/quil"
	ContainerObjects = "/quil/objects"
	ContainerClaude  = "/quil/claude"
	// ContainerQuild is the Linux quild the hooks invoke.
	ContainerQuild = "/usr/local/bin/quild"
)

// Kind distinguishes the two checkout shapes, because they need different
// mount sets. A linked worktree keeps its index and HEAD in the MAIN
// repository, so the container must reach two directories; an ordinary
// checkout keeps everything in one.
type Kind int

const (
	// KindWorktree is a linked worktree: .git is a FILE pointing into the
	// main repository's admin directory.
	KindWorktree Kind = iota
	// KindCheckout is an ordinary repository: .git is a real directory
	// inside the working tree.
	KindCheckout
)

// Mapping is everything one sandbox pane needs to be launched: which host
// directories are mounted where, and which container paths the daemon must
// write into argv and env.
//
// Host paths are in the host's native form (git returns forward slashes even
// on Windows, which docker accepts). Container paths are always "/"-joined.
type Mapping struct {
	PaneID string
	Kind   Kind

	// HostWorktree is the toplevel of the checkout the pane works in — never
	// a subdirectory of it, even when the pane was created in one.
	HostWorktree string
	// HostSubdir is the pane's directory relative to HostWorktree, in
	// container form ("" at the toplevel). It becomes the container's
	// working directory, so a pane created after browsing into wt/src opens
	// in src rather than mounting src alone as a non-repository.
	HostSubdir string
	// HostGitCommon is the repository's shared .git — <main>/.git for a
	// linked worktree, <toplevel>/.git for an ordinary checkout.
	HostGitCommon string
	// HostAdminDir is <main>/.git/worktrees/<name>, holding this worktree's
	// index, HEAD and locks. Empty for KindCheckout.
	HostAdminDir string
	// AdminName is the last element of HostAdminDir, which is also the name
	// the container's admin mount must use — the path is baked into the
	// overlay files, so it cannot be derived container-side.
	AdminName string

	// HostPaneRoot is $QUIL_HOME/sandbox/panes/<paneID>, mounted at /quil.
	HostPaneRoot string
	// HostOverlayDir is $QUIL_HOME/sandbox/overlays/<paneID>. It is NEVER
	// mounted as a directory — only the two files inside it are, read-only.
	// A read-only flag is per mount point, so an overlay reachable
	// read-write anywhere else is rewritable by the agent, and rewriting
	// admin-gitdir lets `git worktree prune` destroy the host worktree.
	HostOverlayDir string
	// HostEmptyDir is a permanently empty directory used to shadow the two
	// objects/info directories read-only. Shared by every pane; nothing ever
	// writes to it.
	HostEmptyDir string

	// Slug is the container working-directory name under /work.
	Slug string

	// SharedClaudeRoot, when set, replaces the per-pane Claude config
	// directory with one shared by every sandbox pane. Empty is the default
	// and the safe shape; see HostClaudeConfig for the trade.
	SharedClaudeRoot string

	// HostQuild is the Linux quild the container's hooks invoke, on the host.
	// Empty means this pane runs without hooks — legal for opencode and
	// codex, refused for claude-code, where a missing session record turns
	// the NEXT restart into an exit-129 crash rather than a degraded pane.
	HostQuild string
}

// ContainerWorkdir is where the checkout is mounted.
func (m Mapping) ContainerWorkdir() string { return ContainerWorkRoot + "/" + m.Slug }

// ContainerCWD is the directory the agent starts in — the workdir, or a
// subdirectory of it when the pane was created below the toplevel.
func (m Mapping) ContainerCWD() string {
	if m.HostSubdir == "" {
		return m.ContainerWorkdir()
	}
	return m.ContainerWorkdir() + "/" + m.HostSubdir
}

// ContainerGitCommon is where the repository's shared .git is readable.
//
// The two kinds differ: a linked worktree mounts it separately at /repo/.git,
// while an ordinary checkout already carries its .git inside the working tree
// that is mounted at /work/<slug>.
func (m Mapping) ContainerGitCommon() string {
	if m.Kind == KindWorktree {
		return ContainerGitDir
	}
	return m.ContainerWorkdir() + "/.git"
}

// ContainerAlternate is the object store the container reads history from,
// read-only. New objects go to ContainerObjects instead.
func (m Mapping) ContainerAlternate() string { return m.ContainerGitCommon() + "/objects" }

// HostObjects is the pane's own object store on the host.
func (m Mapping) HostObjects() string { return joinHost(m.HostPaneRoot, "objects") }

// HostClaudeConfig is the Claude config directory this pane's container gets.
//
// Per-pane by default: CLAUDE_CONFIG_DIR holds user-scope settings (hooks),
// .claude.json (mcpServers), every transcript and history.jsonl, so one shared
// directory lets any sandbox pane plant a hook or an MCP server that every
// other sandbox pane's claude executes inside its own container.
//
// SharedClaudeRoot opts into exactly that, in exchange for signing in once
// instead of once per pane. The trade is the user's to make and is spelled out
// in the config comment and the docs; what must not happen is the knob
// existing and doing nothing.
func (m Mapping) HostClaudeConfig() string {
	if m.SharedClaudeRoot != "" {
		return m.SharedClaudeRoot
	}
	return joinHost(m.HostPaneRoot, "claude")
}

// HostDotGitOverlay is the file mounted over the worktree's own .git.
func (m Mapping) HostDotGitOverlay() string { return joinHost(m.HostOverlayDir, "dot-gitdir") }

// HostAdminGitdirOverlay is the file mounted over the MAIN repository's
// worktrees/<name>/gitdir. Empty for KindCheckout, which has no admin file.
func (m Mapping) HostAdminGitdirOverlay() string {
	if m.Kind != KindWorktree {
		return ""
	}
	return joinHost(m.HostOverlayDir, "admin-gitdir")
}

// ErrUnsafeMapping reports a checkout this package refuses to sandbox. Every
// wrapped case is one where producing a mapping would defeat the sandbox, so
// the caller must surface it as a spawn error and never fall back to a host
// spawn.
var ErrUnsafeMapping = errors.New("unsafe sandbox mapping")

// ErrNotARepository reports a directory git does not recognise.
var ErrNotARepository = errors.New("not a git repository")

// NewMapping resolves hostCWD to a checkout and computes every path a sandbox
// pane needs, or refuses.
//
// It asks git rather than reading .git itself. The three facts it needs —
// toplevel, common dir, and this worktree's own git dir — are one rev-parse
// call, and deriving them by hand means reimplementing gitfile parsing that
// git changes between versions. Asking also fixes the subdirectory case for
// free: a pane created in wt/src resolves to wt, so the container mounts a
// repository rather than a directory that merely sits inside one.
func NewMapping(ctx context.Context, quilDir, hostCWD, paneID string) (Mapping, error) {
	if quilDir == "" || hostCWD == "" || paneID == "" {
		return Mapping{}, fmt.Errorf("%w: empty quilDir, cwd or pane id", ErrUnsafeMapping)
	}
	top, common, gitDir, err := resolveRepo(ctx, hostCWD)
	if err != nil {
		return Mapping{}, err
	}

	m := Mapping{
		PaneID:         paneID,
		HostWorktree:   top,
		HostGitCommon:  common,
		HostPaneRoot:   joinHost(quilDir, "sandbox", "panes", paneID),
		HostOverlayDir: joinHost(quilDir, "sandbox", "overlays", paneID),
		HostEmptyDir:   joinHost(quilDir, "sandbox", "empty"),
	}

	// A linked worktree is exactly "my git dir is not the shared one". The
	// alternative — statting .git and branching on file-vs-directory — is the
	// same test one indirection later, and wrong for a submodule.
	if sameHostPath(gitDir, common) {
		m.Kind = KindCheckout
	} else {
		m.Kind = KindWorktree
		m.HostAdminDir = gitDir
		m.AdminName = filepath.Base(filepath.FromSlash(gitDir))
	}

	sub, err := containerSubdir(top, hostCWD)
	if err != nil {
		return Mapping{}, err
	}
	m.HostSubdir = sub
	m.Slug = slug(top)

	if err := refuse(quilDir, m); err != nil {
		return Mapping{}, err
	}
	return m, nil
}

// refuse rejects every mapping that would defeat the sandbox.
//
// Containment is tested on REAL paths, not cleaned ones. A cleaned absolute
// path still traverses a symlink or a Windows junction, so `C:\dev` pointing
// at a directory inside $QUIL_HOME passes a string prefix test while the mount
// physically exposes $QUIL_HOME. The daemon already resolves symlinks for this
// class of check in defaultCWD.
func refuse(quilDir string, m Mapping) error {
	home, err := realPath(quilDir)
	if err != nil {
		// A QUIL_HOME that cannot be resolved is not a reason to proceed
		// unguarded: the whole point of the check is that we cannot see
		// where it really is.
		return fmt.Errorf("%w: cannot resolve QUIL_HOME %q: %v", ErrUnsafeMapping, quilDir, err)
	}
	for _, cand := range []struct{ what, path string }{
		{"the worktree", m.HostWorktree},
		{"the repository's .git", m.HostGitCommon},
	} {
		real, err := realPath(cand.path)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve %s %q: %v", ErrUnsafeMapping, cand.what, cand.path, err)
		}
		if pathWithin(real, home) {
			return fmt.Errorf("%w: QUIL_HOME (%s) is inside %s (%s) — the container would get "+
				"every pane's settings, the workspace and the daemon socket",
				ErrUnsafeMapping, home, cand.what, real)
		}
	}
	return nil
}

// pathWithin reports whether child is parent or lives under it. Both must
// already be real paths.
func pathWithin(parent, child string) bool {
	p := normalizeCompare(parent)
	c := normalizeCompare(child)
	if p == c {
		return true
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return strings.HasPrefix(c, p)
}

// normalizeCompare puts a host path in the one form comparisons use: forward
// slashes, no trailing separator, and case-folded on Windows because NTFS is
// case-insensitive and a case difference must not defeat a containment test.
func normalizeCompare(p string) string {
	s := filepath.ToSlash(filepath.Clean(p))
	s = strings.TrimSuffix(s, "/")
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return s
}

// sameHostPath compares two host paths for identity under the same rules.
func sameHostPath(a, b string) bool { return normalizeCompare(a) == normalizeCompare(b) }

// containerSubdir expresses cwd relative to the checkout toplevel, in
// container form. It refuses anything that escapes, which a caller cannot
// produce today but which would silently become a mount outside the worktree
// if it ever could.
func containerSubdir(top, cwd string) (string, error) {
	rel, err := filepath.Rel(filepath.FromSlash(top), filepath.FromSlash(cwd))
	if err != nil {
		return "", fmt.Errorf("%w: %q is not inside %q", ErrUnsafeMapping, cwd, top)
	}
	s := filepath.ToSlash(rel)
	if s == "." {
		return "", nil
	}
	if s == ".." || strings.HasPrefix(s, "../") {
		return "", fmt.Errorf("%w: %q escapes the checkout %q", ErrUnsafeMapping, cwd, top)
	}
	return s, nil
}

// slugDigestLen is how much of the path digest ends up in the container
// working directory name. Eight hex characters distinguish every checkout a
// user realistically has open while keeping the path readable in a prompt.
const slugDigestLen = 8

// slug builds the container working-directory name for a host path.
//
// The digest covers the host path and NOTHING else — deliberately not the pane
// id. Claude stores transcripts per SESSION under a directory derived from the
// working directory, so two panes in one directory never collide; adding the
// pane id would instead give every new pane on a worktree a fresh directory
// and leave its resume picker permanently empty.
//
// The readable half is collapsed to [A-Za-z0-9_-] with "." excluded, so ".."
// is unreachable by construction rather than filtered. That matters because
// the slug reaches both `-w` and the container side of a colon-delimited `-v`:
// a directory named "feat:ro" would otherwise produce a mount docker parses as
// having a mode.
func slug(hostPath string) string {
	sum := sha256.Sum256([]byte(normalizeCompare(hostPath)))
	digest := hex.EncodeToString(sum[:])[:slugDigestLen]

	base := filepath.Base(filepath.FromSlash(strings.TrimSuffix(filepath.ToSlash(hostPath), "/")))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		// A drive root or a path that collapses away entirely. The digest
		// still makes it unique; the name is merely less helpful.
		return digest
	}
	return clean + "-" + digest
}

// joinHost joins host path elements. Host paths keep the host's own
// separators, so this is filepath.Join — the container-side counterpart is a
// plain "/" join and must never come here.
func joinHost(elem ...string) string { return filepath.Join(elem...) }
