package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/opencodehook"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sandbox"
)

// hookModeFor picks the per-plugin hook verbosity the container's env carries.
// Mirrors the switch spawnPane uses for a host pane, so a sandbox pane obeys
// the same [notification.hooks] settings.
func hookModeFor(cfg config.Config, p *plugin.PanePlugin) string {
	switch {
	case p.Name == "opencode":
		return cfg.Notification.Hooks.OpenCode
	case p.Name == plugin.CodexPluginName:
		return cfg.Notification.Hooks.Codex
	default:
		return cfg.Notification.Hooks.Claude
	}
}

// sandboxTypePrefix marks a persisted pane type as sandboxed.
//
// The prefix, rather than a bare boolean field, is what makes a DOWNGRADE
// safe. Restore reads only the keys it knows, and auto-update has a rollback
// path, so a daemon that predates this feature would read a sandbox pane's
// type as plain "claude-code", point it at the worktree, and spawn an agent on
// the host — un-sandboxed, silently. An unknown type instead takes the
// existing fallback to "terminal": a shell, which is wrong but harmless and
// visible.
const sandboxTypePrefix = "sandbox/"

// sandboxPaneType is what goes on disk for a sandboxed pane of the given
// plugin.
func sandboxPaneType(plugin string) string { return sandboxTypePrefix + plugin }

// splitSandboxType reports the plugin a persisted type names, and whether that
// type was sandboxed.
func splitSandboxType(typ string) (plugin string, sandboxed bool) {
	if rest, ok := strings.CutPrefix(typ, sandboxTypePrefix); ok {
		return rest, true
	}
	return typ, false
}

// sandboxRoot is the directory holding every per-pane sandbox tree.
func sandboxRoot(quilDir string) string { return filepath.Join(quilDir, "sandbox") }

// sandboxEmptyDir is the permanently empty directory used to shadow the two
// objects/info directories read-only. Shared by every pane; nothing writes to
// it, which is the whole point.
func sandboxEmptyDir(quilDir string) string { return filepath.Join(sandboxRoot(quilDir), "empty") }

// prepareSandbox builds the pane's mapping and puts every host-side file in
// place, so `docker run` has something coherent to mount.
//
// Order matters and is not obvious: the overlays and the alternates line must
// exist BEFORE the container starts. A bind-mounted file cannot be replaced
// from inside afterwards — a host-side atomic write changes the inode and the
// container keeps seeing the old content — and a container that commits before
// the alternates line exists leaves the host answering "fatal: bad object
// HEAD" for that worktree until it does.
func (d *Daemon) prepareSandbox(ctx context.Context, pane *Pane, image string) (sandbox.Mapping, error) {
	quilDir := config.QuilDir()

	m, err := sandbox.NewMapping(ctx, quilDir, pane.CWD, pane.ID)
	if err != nil {
		return sandbox.Mapping{}, err
	}

	// The per-pane tree. Every directory the hook writes into lives under
	// this one root, which is why the hook binary needs no change: it derives
	// all of them from QUIL_HOOK_HOME.
	for _, dir := range []string{
		m.HostPaneRoot,
		m.HostObjects(),
		m.HostClaudeConfig(),
		filepath.Join(m.HostPaneRoot, "sessions"),
		filepath.Join(m.HostPaneRoot, "events"),
		filepath.Join(m.HostPaneRoot, "history"),
		filepath.Join(m.HostPaneRoot, "claudehook"),
		m.HostOverlayDir,
		sandboxEmptyDir(quilDir),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return sandbox.Mapping{}, fmt.Errorf("sandbox: create %s: %w", dir, err)
		}
	}

	if err := writeOverlays(m); err != nil {
		return sandbox.Mapping{}, err
	}
	if err := sandbox.AddAlternate(m); err != nil {
		return sandbox.Mapping{}, fmt.Errorf("sandbox: register object store: %w", err)
	}

	// A container left by a crashed daemon holds the --name this one needs,
	// and docker refuses the run rather than replacing it. Ignoring the
	// result is correct: a pane with no container is the normal case.
	if err := sandbox.RemoveForce(ctx, pane.ID); err != nil {
		log.Printf("sandbox: pane %s: pre-run remove: %v", pane.ID, err)
	}
	return m, nil
}

// writeOverlays puts the two gitdir files in place.
//
// They live in a directory that is never mounted as a directory, only as two
// individual read-only files. A read-only flag is per mount point, so an
// overlay reachable read-write anywhere else is rewritable by the agent — and
// rewriting the admin one lets `git worktree prune` inside the container
// delete the host's worktree registration, taking uncommitted work with it.
func writeOverlays(m sandbox.Mapping) error {
	if m.Kind != sandbox.KindWorktree {
		return nil
	}
	for _, f := range []struct{ path, body string }{
		{m.HostDotGitOverlay(), sandbox.DotGitOverlay(m)},
		{m.HostAdminGitdirOverlay(), sandbox.AdminGitdirOverlay(m)},
	} {
		if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
			return fmt.Errorf("sandbox: write overlay %s: %w", f.path, err)
		}
	}
	return nil
}

// sandboxIdentity gathers what the container needs that only the host knows.
func (d *Daemon) sandboxIdentity(pane *Pane, hookMode string, recordHistory bool) sandbox.Identity {
	id := sandbox.Identity{
		HookMode:      hookMode,
		RecordHistory: recordHistory,
		QuilHomeLabel: quilHomeLabel(config.QuilDir()),
		GitUserName:   gitConfigValue("user.name"),
		GitUserEmail:  gitConfigValue("user.email"),
	}
	// The token travels as a NAME only: docker forwards the value from its
	// own environment, so it never reaches argv and therefore never reaches
	// quild.log, which the F1 viewer renders.
	if d.cfg.Sandbox.Auth == "token" && os.Getenv(oauthTokenEnv) != "" {
		id.ForwardOAuthToken = true
	}
	if runtime.GOOS != "windows" {
		// Docker Desktop on Windows ignores uids. On a Linux host — the
		// remote-daemon case — the per-pane tree is 0700 as the daemon user,
		// so an image running as some other uid cannot write its hook spool
		// or persist a sign-in, and the hook swallows the errors.
		id.User = strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	}
	return id
}

// oauthTokenEnv is the credential Claude Code reads for headless
// authentication. Quil never stores it: it is read from the daemon's own
// environment and forwarded by name.
const oauthTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// quilHomeLabel scopes a container to ONE daemon's data directory.
//
// Docker labels are engine-wide and the dev and production daemons share one
// engine, so a sweep filtering on quil.pane alone would have a dev daemon
// classify every production sandbox container as an orphan and force-remove
// it — killing the owner's live agents, and breaking the project's one
// always-on isolation rule.
//
// The value is canonicalised before hashing: QuilDir returns $QUIL_HOME
// verbatim, so a trailing slash or a case difference between a TUI-spawned and
// a manually started daemon would otherwise make a daemon miss its own
// orphans. It fails safe either way — a mismatch never reaps another install's
// containers.
func quilHomeLabel(quilDir string) string {
	s := filepath.ToSlash(filepath.Clean(quilDir))
	s = strings.TrimSuffix(s, "/")
	if runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return fmt.Sprintf("%08x", fnv32(s))
}

// fnv32 is FNV-1a. A label only has to distinguish two data directories on one
// machine, so it needs no cryptographic property and no dependency.
func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// hookPaths tells a hook prep two things it used to conflate: where on the
// HOST to write its files, and what path to NAME them by in the argument or
// environment the child will read.
//
// For an ordinary pane those are the same directory. For a sandbox pane they
// differ, and that difference is the whole of the hook work: the settings file
// is written into the pane's own tree on the host, and claude loads it from
// inside the container, where the host path does not exist. Conflating them is
// how a container ends up handed a `--settings C:\Users\...` it cannot open.
type hookPaths struct {
	// HostDir is where files are actually created.
	HostDir string
	// RefDir is HostDir as the CHILD will see it.
	RefDir string
	// ExePath is the quild the hook command invokes, in the child's terms.
	// Empty means "resolve the running executable", which is right for a host
	// pane and wrong for a container.
	ExePath string
}

// copyOpencodeScript stages the plugin script into a pane's own tree.
//
// opencode's config content names the script by ABSOLUTE path and the loader
// refuses a relative one, so the file has to exist at a path the container can
// open. Copying beats mounting the shared directory: one more mount of a
// shared $QUIL_HOME subtree is exactly the shape that made the first version
// of this feature a cross-pane escape.
func copyOpencodeScript(dstQuilDir, _ string) error {
	src := opencodehook.ScriptPath(config.QuilDir())
	dst := opencodehook.ScriptPath(dstQuilDir)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o600)
}

// hostHookPaths is the ordinary case: write and name the same directory.
func hostHookPaths(quilDir string) hookPaths {
	return hookPaths{HostDir: quilDir, RefDir: quilDir}
}

// containerHookPaths writes into the pane's own tree and names it as the
// container sees it.
func containerHookPaths(m sandbox.Mapping) hookPaths {
	return hookPaths{
		HostDir: m.HostPaneRoot,
		RefDir:  sandbox.ContainerQuil,
		ExePath: sandbox.ContainerQuild,
	}
}

// exe resolves the quild path the hook command should invoke.
func (hp hookPaths) exe() (string, error) {
	if hp.ExePath != "" {
		return hp.ExePath, nil
	}
	return quildExeFn()
}

// ref restates a path that was built under HostDir in the child's terms.
//
// A path that is not under HostDir is returned unchanged: the callers build
// every path they pass from HostDir, so anything else is a bug worth leaving
// visible rather than silently rewriting.
func (hp hookPaths) ref(hostPath string) string {
	if hp.RefDir == hp.HostDir || hostPath == "" {
		return hostPath
	}
	rel, err := filepath.Rel(hp.HostDir, hostPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return hostPath
	}
	return hp.RefDir + "/" + filepath.ToSlash(rel)
}

// wrapInContainer replaces the pane's command with the `docker run` that
// starts it inside the container.
//
// It is the LAST transformation in spawnPane, after the hook switch and after
// shellinit — both of which can replace cmd and args, and both of which must
// see the command the container will actually run rather than the docker CLI.
//
// pane.CWD is deliberately left alone by the caller. It is the HOST path, and
// the git subsystem, the close dialog and ownedWorktreePaths all read it; the
// agent's working directory comes from `docker run -w` instead.
func (d *Daemon) wrapInContainer(m sandbox.Mapping, pane *Pane, p *plugin.PanePlugin,
	image, cmd string, args, envVars []string) (string, []string) {

	// A plugin's own [command] env still has to reach the child, and inside a
	// container it can only do so through docker's own -e. Anything the
	// container defines for itself is dropped with a log line rather than
	// forwarded: docker takes the LAST -e for a repeated name, so a plugin
	// setting QUIL_HOOK_HOME or GIT_OBJECT_DIRECTORY would silently repoint
	// the hook spool or the object store at a path outside the mount set.
	// Dropping it silently would be its own bug, so the author sees why.
	var extra []string
	for _, e := range envVars {
		if isReservedSandboxEnv(e) {
			log.Printf("sandbox: pane %s: %q is defined by the container — ignoring the pane-env copy",
				pane.ID, e)
			continue
		}
		extra = append(extra, e)
	}

	id := d.sandboxIdentity(pane, hookModeFor(d.cfg, p), p.Command.RecordHistory)
	runArgs := sandbox.RunArgs(sandbox.Spec{Image: image}, m, id, runtime.GOOS, extra, cmd, args)

	dockerPath := dockerBinaryPath()
	pane.PluginMu.Lock()
	pane.ContainerCWD = m.ContainerCWD()
	pane.PluginMu.Unlock()

	log.Printf("sandbox: pane %s: image=%s workdir=%s kind=%d", pane.ID, image, m.ContainerCWD(), m.Kind)
	return dockerPath, runArgs
}

// isReservedSandboxEnv reports names the container defines for itself, which a
// plugin's own [command] env must not redefine.
func isReservedSandboxEnv(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	switch name {
	case "QUIL_HOOK_HOME", "QUIL_HOOK_EXE", "QUIL_PANE_ID",
		"CLAUDE_CONFIG_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
		return true
	}
	return strings.HasPrefix(name, "GIT_CONFIG_")
}

// dockerCLIEnv is the environment the docker CLI itself runs with.
//
// Almost nothing: the container's environment travels through `docker run -e`,
// and handing the CLI the pane's env as well would put QUIL_HOOK_HOME and
// friends — host paths — into a process that has no use for them.
//
// The token is the exception and the reason this function exists. It is passed
// to `docker run` by NAME, so docker reads the VALUE from its own environment.
// That is what keeps it out of argv, and therefore out of quild.log, which the
// F1 log viewer renders on screen.
func dockerCLIEnv(auth string) []string {
	if auth != "token" {
		return nil
	}
	if v := os.Getenv(oauthTokenEnv); v != "" {
		return []string{oauthTokenEnv + "=" + v}
	}
	return nil
}

// dockerBinaryPath resolves the docker CLI once, falling back to the bare name
// so the spawn produces docker's own "not found" rather than a Quil error for
// a condition the capability probe has already reported.
func dockerBinaryPath() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	return "docker"
}

// gitConfigFn is the seam the identity tests drive.
var gitConfigFn = func(key string) (string, error) {
	cmd := exec.Command("git", "config", "--get", key)
	hideGitWindow(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// gitConfigValue reads one global git setting, or "" when there is none.
//
// Without an identity a commit inside the container fails with "Author
// identity unknown": the host's global config is not mounted, deliberately,
// since it can carry credential helpers and fsmonitor commands that name host
// binaries. Passing just these two values is the narrow alternative.
func gitConfigValue(key string) string {
	v, err := gitConfigFn(key)
	if err != nil {
		return ""
	}
	return v
}
