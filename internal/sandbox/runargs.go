package sandbox

import (
	"fmt"
	"sort"
	"strings"
)

// Spec is the part of a sandbox pane the user chose.
type Spec struct {
	// Image is the container image, supplied by the user. Quil publishes no
	// image: running Claude Code inside a product's own image triggers the
	// Commercial Terms conditions, and a user-supplied image means Quil
	// pre-installs nothing.
	Image string
}

// Identity is what the container needs that only the host knows.
type Identity struct {
	// GitUserName and GitUserEmail come from the host's global git config.
	// Without them a commit inside the container fails with "Author identity
	// unknown" — the global config is not mounted, deliberately.
	GitUserName  string
	GitUserEmail string
	// User is the "uid:gid" the container runs as, empty to leave it to the
	// image. Set on a Linux host, where the per-pane root is 0700 as the
	// daemon user and an image running as some other uid cannot write its
	// hook spool or persist a sign-in. Docker Desktop on Windows ignores
	// uids, so it stays empty there.
	User string
	// ForwardOAuthToken names CLAUDE_CODE_OAUTH_TOKEN as an environment
	// variable to forward WITHOUT its value. The value must reach the docker
	// CLI through its own process environment instead: spawnPane logs full
	// argv and the F1 viewer renders that log, so `-e NAME=value` would print
	// the user's token.
	ForwardOAuthToken bool
	// HookMode and RecordHistory mirror the pane env the daemon already
	// computes for a host pane.
	HookMode      string
	RecordHistory bool
	// QuilHomeLabel scopes the container to ONE daemon's data directory. The
	// startup sweep filters on it, because docker labels are engine-wide and
	// a dev daemon that swept on quil.pane alone would force-remove every
	// production sandbox container.
	QuilHomeLabel string
}

// mount is one -v argument, kept structured so tests can assert on it rather
// than on string slicing.
type mount struct {
	host      string
	container string
	readOnly  bool
}

// arg renders one --mount argument.
//
// `--mount`, not `-v`. The short form is `host:container[:mode]`, so a host
// path containing a colon — legal on Linux and macOS, and reachable through a
// checkout directory, $QUIL_HOME, or the shared config directory — is parsed
// as extra volume fields and the pane fails to start with a docker error the
// user cannot connect to Quil. `--mount` uses named comma-separated fields,
// which colons pass through untouched.
//
// A comma in a host path is the residual, and NewMapping refuses one rather
// than emitting an argument docker would mis-split: `--mount` has no escaping
// for it either, so refusing at the boundary is the only honest answer.
func (mt mount) arg() string {
	s := "type=bind,source=" + mt.host + ",target=" + mt.container
	if mt.readOnly {
		s += ",readonly"
	}
	return s
}

// Mounts returns the container's whole filesystem view, in argv order.
//
// This is the security boundary of the feature. Three rules hold across every
// entry, each learned by measurement:
//
//   - Only the repository's .git enters the container, never the main
//     checkout's working tree. Mounting the checkout root was measured
//     handing the agent `cat /repo/.env` → SECRET=hunter2.
//   - The object store is READ-ONLY, with new objects written to the pane's
//     own store instead. Objects are every commit, tree and blob for every
//     branch; a writable store means one `rm -rf` destroys the repository's
//     entire local history rather than the sandboxed worktree's.
//   - Both objects/info directories are shadowed empty. Git follows
//     info/alternates and info/commit-graph transitively, and those two
//     directories are the one place the agent and the host disagree about who
//     owns them.
func Mounts(m Mapping) []mount {
	var ms []mount

	switch m.Kind {
	case KindWorktree:
		// The repository's .git ONLY — not the checkout root.
		ms = append(ms,
			mount{m.HostGitCommon, ContainerGitDir, true},
			// The three directories a commit must write. A worktree keeps
			// its index and HEAD in the MAIN repository, which is why a
			// wholly read-only /repo cannot even run `git add`.
			mount{joinHost(m.HostGitCommon, "refs"), ContainerGitDir + "/refs", false},
			mount{joinHost(m.HostGitCommon, "logs"), ContainerGitDir + "/logs", false},
			mount{m.HostAdminDir, ContainerGitDir + "/worktrees/" + m.AdminName, false},
			// …but NOT its config.worktree. With extensions.worktreeConfig
			// enabled, git reads per-worktree configuration from that file —
			// including values host git EXECUTES, core.fsmonitor and
			// core.hooksPath among them. The admin directory has to be
			// writable for the index and HEAD, so the one executable-config
			// file inside it is shadowed with an empty read-only file. Same
			// shape as the objects/info shadows, same reason: a directory the
			// agent must write, containing one thing it must not.
			mount{m.HostEmptyFile, ContainerGitDir + "/worktrees/" + m.AdminName + "/config.worktree", true},
			// The worktree itself.
			mount{m.HostWorktree, m.ContainerWorkdir(), false},
			// The two overlays, which make the host's absolute gitdir paths
			// resolvable inside the container without touching the host's
			// own files.
			mount{m.HostDotGitOverlay(), m.ContainerWorkdir() + "/.git", true},
			mount{m.HostAdminGitdirOverlay(),
				ContainerGitDir + "/worktrees/" + m.AdminName + "/gitdir", true},
		)
	case KindCheckout:
		// An ordinary checkout carries its .git inside the working tree, so
		// there is one mount and no overlay. The pieces host git EXECUTES —
		// hooks and config — are pinned read-only over it, for the same
		// reason the worktree case never makes them writable: the host runs
		// them on its next `git status`.
		ms = append(ms,
			mount{m.HostWorktree, m.ContainerWorkdir(), false},
			mount{joinHost(m.HostGitCommon, "objects"), m.ContainerAlternate(), true},
			mount{joinHost(m.HostGitCommon, "hooks"), m.ContainerGitCommon() + "/hooks", true},
			mount{joinHost(m.HostGitCommon, "config"), m.ContainerGitCommon() + "/config", true},
			// worktrees/ holds the admin directory of every OTHER worktree of
			// this repository, and it is inside the read-write tree here
			// because the whole checkout is mounted. Left writable, a
			// container-side `git worktree prune` unregisters the user's other
			// worktrees — the same data loss the overlay exists to prevent on
			// the linked-worktree path, reached through a directory this one
			// does not otherwise need at all.
			mount{joinHost(m.HostGitCommon, "worktrees"), m.ContainerGitCommon() + "/worktrees", true},
		)
	}

	ms = append(ms,
		// Shadow both objects/info directories with an empty read-only
		// directory.
		//
		// /repo side: the alternates line Quil itself writes is in the
		// host's native path form, and container git tries to follow it as a
		// nested alternate — measured printing "error: unable to normalize
		// alternate object path: /repo/.git/objects/C:/Users/..." on EVERY
		// git invocation inside the container.
		//
		// /quil side: that directory is agent-writable and the host's live
		// alternates line points at it, so a planted info/alternates makes
		// host git error on every command.
		mount{m.HostEmptyDir, m.ContainerAlternate() + "/info", true},
		mount{m.HostEmptyDir, ContainerObjects + "/info", true},
		// The pane's own subtree: hook spool, Claude config, object store.
		mount{m.HostPaneRoot, ContainerQuil, false},
	)
	if m.SharedClaudeRoot != "" {
		// The shared config directory is mounted OVER the per-pane one, so
		// the container path stays /quil/claude either way and nothing
		// downstream has to know which shape it got.
		ms = append(ms, mount{m.SharedClaudeRoot, ContainerClaude, false})
	}
	if m.HostQuild != "" {
		// Read-only: the hook binary is Quil's own, and nothing inside the
		// container has any business rewriting the program the host asked it
		// to run.
		ms = append(ms, mount{m.HostQuild, ContainerQuild, true})
	}
	return ms
}

// Env returns the container environment, sorted for a stable argv.
//
// hostGOOS is a PARAMETER rather than runtime.GOOS so both shapes are testable
// on a Linux CI runner. The Windows-only entry is the one most likely to
// regress unnoticed otherwise.
func Env(m Mapping, id Identity, hostGOOS string) []string {
	env := map[string]string{
		"QUIL_PANE_ID":   m.PaneID,
		"QUIL_HOOK_HOME": ContainerQuil,
		"QUIL_HOOK_EXE":  ContainerQuild,
		// Claude writes and refreshes its own credentials here. Quil never
		// reads, copies or stores one.
		"CLAUDE_CONFIG_DIR": ContainerClaude,
		// Codex reads auth.json, its config and its sessions from here.
		// Always set, even for a claude pane: the directory is the pane's own
		// either way, and a codex started by hand inside the container must
		// not fall back to a home directory nothing mounts.
		"CODEX_HOME": ContainerCodex,
		// New objects go to the pane's own store; history is read from the
		// repository's, read-only.
		"GIT_OBJECT_DIRECTORY":             ContainerObjects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": m.ContainerAlternate(),
	}
	if id.HookMode != "" {
		env["QUIL_HOOK_MODE"] = id.HookMode
	}
	if id.RecordHistory {
		env["QUIL_RECORD_HISTORY"] = "1"
	}

	// git configuration by environment, because the host's global config is
	// not mounted and must not be.
	cfg := []struct{ key, val string }{
		// A non-root container user otherwise refuses the bind-mounted
		// repository with "detected dubious ownership" — and non-root is
		// required, since claude refuses --dangerously-skip-permissions as
		// root.
		{"safe.directory", "*"},
	}
	if hostGOOS == "windows" {
		// core.autocrlf lives in the user's global config on Windows. Without
		// it container git compares CRLF working files against LF blobs and
		// reports the WHOLE tree as modified — measured on a file the
		// container never touched — so the agent burns its context on phantom
		// changes or commits a repo-wide line-ending rewrite.
		cfg = append(cfg, struct{ key, val string }{"core.autocrlf", "true"})
	}
	if id.GitUserName != "" {
		cfg = append(cfg, struct{ key, val string }{"user.name", id.GitUserName})
	}
	if id.GitUserEmail != "" {
		cfg = append(cfg, struct{ key, val string }{"user.email", id.GitUserEmail})
	}
	env["GIT_CONFIG_COUNT"] = fmt.Sprint(len(cfg))
	for i, c := range cfg {
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)] = c.key
		env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = c.val
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// ContainerName is the docker --name for a pane. Stable across restarts,
// because a restart reuses the pane id and must be able to remove whatever the
// previous run left.
func ContainerName(paneID string) string { return "quil-" + paneID }

// RunArgs builds the full `docker run` argv, ending with the command the pane
// would have run on the host.
//
// No --rm: a container removed the instant it exits takes its logs with it,
// and "the pane died and there is nothing to look at" is the worst of the
// failure modes this feature can produce. Removal is explicit at teardown.
//
// No --privileged, no user-supplied flag of any kind, and no -v beyond the
// computed set. The image reference is the ONLY thing the user controls, and
// the caller validates it before it reaches here.
// extraEnv carries a plugin's own [command] env. It is appended AFTER the
// computed set, and docker takes the last -e for a repeated name, so the
// caller must strip anything the container defines for itself before passing
// it here — a plugin that redefined QUIL_HOOK_HOME or GIT_OBJECT_DIRECTORY
// would silently repoint the hook spool or the object store at a path outside
// the mount set.
func RunArgs(spec Spec, m Mapping, id Identity, hostGOOS string, extraEnv []string,
	cmd string, cmdArgs []string) []string {
	args := []string{
		"run",
		"--name", ContainerName(m.PaneID),
		"--label", "quil.pane=" + m.PaneID,
		"--label", "quil.home=" + id.QuilHomeLabel,
		// -i keeps stdin open; -t allocates a TTY inside the container. The
		// pane's PTY is on the outside of the docker CLI, not the inside.
		"-i", "-t",
		"-w", m.ContainerCWD(),
	}
	if id.User != "" {
		args = append(args, "--user", id.User)
	}
	for _, mt := range Mounts(m) {
		args = append(args, "--mount", mt.arg())
	}
	for _, e := range Env(m, id, hostGOOS) {
		args = append(args, "-e", e)
	}
	for _, e := range extraEnv {
		args = append(args, "-e", e)
	}
	if id.ForwardOAuthToken {
		// NAME only, never NAME=value: docker forwards the value from its
		// own environment, so the token never appears in argv and therefore
		// never in quild.log.
		args = append(args, "-e", oauthTokenVar)
	}
	args = append(args, spec.Image, cmd)
	return append(args, cmdArgs...)
}

// oauthTokenVar is the credential Claude Code reads for headless
// authentication. Anthropic's dev-container documentation describes supplying
// it to a container exactly this way.
const oauthTokenVar = "CLAUDE_CODE_OAUTH_TOKEN"

// ImageOK reports whether an image reference is one this package will put in
// argv.
//
// A conservative allowlist rather than a docker-reference parser: every IPC
// client can set the image, it lands in a command line, and the set of
// characters a legitimate reference needs is small. Anything rejected here is
// a reference the user can rewrite; anything accepted wrongly is argv.
func ImageOK(image string) bool {
	if image == "" || len(image) > 255 || strings.HasPrefix(image, "-") {
		return false
	}
	for _, r := range image {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '/', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}
