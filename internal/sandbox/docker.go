package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// This is the ONLY file in the package that executes anything. Everything else
// is path arithmetic over a Mapping, which is what lets the mount set — the
// security boundary of the whole feature — be table-tested inside `dev.sh
// test`, where no Docker daemon exists.

// dockerBinary is the executable every call here invokes.
var dockerBinary = "docker"

// infoTimeout bounds a capability probe. Stated rather than left to the
// caller's context because a hung Docker Desktop otherwise parks the worker
// goroutine for as long as the caller is willing to wait, on every cache
// expiry, for the life of a daemon that runs for weeks.
var infoTimeout = 10 * time.Second

// removeTimeout bounds a teardown call. Short on purpose: teardown runs on the
// path that also removes the worktree, and the daemon's own shutdown budget is
// five seconds for everything.
var removeTimeout = 5 * time.Second

// runDocker is the seam every invocation goes through.
//
// Mirrors gitworktree.runGit, including the two pieces that are not obvious:
// hideWindow, because the daemon is spawned DETACHED_PROCESS and a Windows
// child of a console-less parent gets a real console WINDOW allocated — and
// `docker info` runs on every capability-cache expiry, so the user would watch
// one flash over the create dialog they are reading; and WaitDelay, because
// CommandContext kills the CLI on expiry but Wait still blocks on any holder
// of the pipe.
//
// `docker run` is deliberately NOT here: it is the pane's PTY child, spawned
// through the daemon's own PTY layer, not an exec in this package.
var runDocker = func(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, dockerBinary, args...)
	hideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, msg)
		}
	}
	return stdout.String(), err
}

// Info is what a capability probe learned about the engine.
type Info struct {
	ServerVersion string
	OSType        string
	// Arch is normalised to Go's spelling (amd64, arm64). Docker reports the
	// machine form — x86_64, aarch64 — and the only consumer is a release
	// asset name, which is in Go's.
	Arch string
}

// Usable reports whether this engine can run the images the feature needs.
//
// OSType matters: Docker Desktop in Windows-containers mode answers `docker
// info` perfectly well and then fails every linux image at `run`, so an
// engine-reachable check alone would offer the user a pane that cannot start.
func (i Info) Usable() bool { return i.OSType == "linux" && i.ServerVersion != "" }

// Probe asks the engine what it is.
//
// `docker info` rather than `docker --version`: the CLI is frequently present
// with no engine reachable — Docker Desktop not started is the normal state at
// Windows login — and a version string proves only that a binary exists.
func Probe(ctx context.Context) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()

	out, err := runDocker(ctx, "info", "--format",
		"{{.ServerVersion}} {{.OSType}} {{.Architecture}}")
	if err != nil {
		return Info{}, err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 2 {
		return Info{}, fmt.Errorf("docker info: unexpected answer %q", strings.TrimSpace(out))
	}
	in := Info{ServerVersion: f[0], OSType: f[1]}
	if len(f) > 2 {
		in.Arch = normalizeArch(f[2])
	}
	return in, nil
}

// normalizeArch maps docker's machine names onto Go's GOARCH spelling.
// Anything unrecognised passes through: a wrong guess would name a release
// asset that does not exist, which fails loudly, while dropping the value
// fails silently.
func normalizeArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return a
	}
}

// RemoveForce removes a pane's container, running or not.
//
// Idempotent and safe to call for a pane that has none: a missing container is
// not an error worth propagating, because every caller's next step is the same
// either way. Callers run it BEFORE `docker run` too — a container surviving a
// daemon crash otherwise holds the --name its replacement needs.
func RemoveForce(ctx context.Context, paneID string) error {
	ctx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()

	_, err := runDocker(ctx, "rm", "-f", ContainerName(paneID))
	if err != nil && isNoSuchContainer(err) {
		return nil
	}
	return err
}

// Kill stops a pane's container without removing it.
//
// `docker kill`, never `docker stop`: stop's grace is ten seconds per
// container and claude as PID 1 ignores SIGTERM (the kernel applies no default
// action to PID 1), while the daemon's whole shutdown budget is five seconds
// before it is SIGKILLed itself. Two sandbox panes would need twenty seconds
// and get none, orphaning both containers and leaving the pid file — turning
// every restart into the crash path.
func Kill(ctx context.Context, paneID string) error {
	ctx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()

	_, err := runDocker(ctx, "kill", ContainerName(paneID))
	if err != nil && (isNoSuchContainer(err) || isNotRunning(err)) {
		return nil
	}
	return err
}

// ListPaneIDs returns the pane ids of every container belonging to THIS
// daemon's data directory.
//
// Both labels, always. Docker labels are engine-wide and the dev and
// production daemons share one engine, so filtering on quil.pane alone would
// have a dev daemon classify every production sandbox container as an orphan
// and force-remove it — killing the owner's live agents, and breaking the
// project's one always-on isolation rule.
//
// `-a` WITHOUT `-q`. Measured: `docker ps -aq --format ...` prints "WARNING:
// Ignoring custom format, because both --format and --quiet are set" and
// returns container ids, so a sweep written that way silently compares ids
// against pane ids and matches nothing.
func ListPaneIDs(ctx context.Context, quilHomeLabel string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()

	out, err := runDocker(ctx, "ps", "-a",
		"--filter", "label=quil.pane",
		"--filter", "label=quil.home="+quilHomeLabel,
		"--format", `{{.Label "quil.pane"}}`)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

func isNoSuchContainer(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such container")
}

func isNotRunning(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "is not running")
}
