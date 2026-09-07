package sandbox

import (
	"context"
	"strings"
	"testing"
)

func testMapping(t *testing.T) Mapping {
	t.Helper()
	stubRealPath(t)
	stubGit(t, "/projects/wt", "/projects/main/.git", "/projects/main/.git/worktrees/wt")
	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/wt", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	return m
}

// The finding that made v3 unshippable: mounting the checkout ROOT hands the
// agent a read-only copy of the user's primary source tree. Measured against a
// real container as `cat /repo/.env` → SECRET=hunter2.
func TestMounts_MainWorkingTreeNeverEntersTheContainer(t *testing.T) {
	m := testMapping(t)
	for _, mt := range Mounts(m) {
		if mt.host == "/projects/main" {
			t.Fatalf("the main checkout root is mounted at %s — the agent can read every "+
				"file in the user's primary source tree, secrets included", mt.container)
		}
	}
	var sawGitDir bool
	for _, mt := range Mounts(m) {
		if mt.container == ContainerGitDir && mt.host == "/projects/main/.git" && mt.readOnly {
			sawGitDir = true
		}
	}
	if !sawGitDir {
		t.Error("the repository's .git is not mounted read-only at " + ContainerGitDir)
	}
}

// Objects are every commit, tree and blob for every branch. A writable store
// means one rm -rf destroys the repository's whole local history, so the
// alternate must be read-only and new objects must go to the pane's own store.
func TestMounts_ObjectStoreIsNeverWritable(t *testing.T) {
	m := testMapping(t)
	objects := "/projects/main/.git/objects"
	for _, mt := range Mounts(m) {
		if !pathWithin(objects, mt.host) {
			continue
		}
		if !mt.readOnly {
			t.Errorf("%s is mounted read-write at %s", mt.host, mt.container)
		}
	}
	env := Env(m, Identity{}, "linux")
	if !hasEnv(env, "GIT_OBJECT_DIRECTORY="+ContainerObjects) {
		t.Error("new objects are not directed at the pane's own store")
	}
	if !hasEnv(env, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+m.ContainerAlternate()) {
		t.Error("history is not readable through the repository's store as an alternate")
	}
}

// Git follows info/alternates and info/commit-graph transitively. The /repo
// side carries the line Quil itself writes, in host path form, which container
// git cannot normalise; the /quil side is agent-writable and is what the
// host's own line points at.
func TestMounts_BothObjectsInfoDirsAreShadowed(t *testing.T) {
	m := testMapping(t)
	want := map[string]bool{
		m.ContainerAlternate() + "/info": false,
		ContainerObjects + "/info":       false,
	}
	for _, mt := range Mounts(m) {
		if _, ok := want[mt.container]; !ok {
			continue
		}
		if !mt.readOnly {
			t.Errorf("%s is shadowed read-WRITE; the agent can plant info/alternates", mt.container)
		}
		if mt.host != m.HostEmptyDir {
			t.Errorf("%s is shadowed by %s, want the always-empty dir", mt.container, mt.host)
		}
		want[mt.container] = true
	}
	for c, seen := range want {
		if !seen {
			t.Errorf("%s is not shadowed", c)
		}
	}
}

// A commit in a linked worktree writes the index and HEAD in the MAIN
// repository. Without these three, `git add` fails with
// "Unable to create '.../index.lock': Read-only file system".
func TestMounts_CommitPathIsWritable(t *testing.T) {
	m := testMapping(t)
	need := map[string]bool{
		ContainerGitDir + "/refs":         false,
		ContainerGitDir + "/logs":         false,
		ContainerGitDir + "/worktrees/wt": false,
	}
	for _, mt := range Mounts(m) {
		if _, ok := need[mt.container]; ok && !mt.readOnly {
			need[mt.container] = true
		}
	}
	for c, ok := range need {
		if !ok {
			t.Errorf("%s is not writable — the pane cannot stage or commit", c)
		}
	}
}

// Host git EXECUTES .git/hooks and honours .git/config, so a container that
// can write them has host code execution. The worktree case gets this from
// mounting .git read-only; the ordinary-checkout case has to pin them.
func TestMounts_OrdinaryCheckoutPinsHooksAndConfig(t *testing.T) {
	stubRealPath(t)
	stubGit(t, "/projects/plain", "/projects/plain/.git", "/projects/plain/.git")
	m, err := NewMapping(context.Background(), "/home/u/.quil", "/projects/plain", "pane1")
	if err != nil {
		t.Fatalf("NewMapping: %v", err)
	}
	need := map[string]bool{
		m.ContainerGitCommon() + "/hooks":  false,
		m.ContainerGitCommon() + "/config": false,
	}
	for _, mt := range Mounts(m) {
		if _, ok := need[mt.container]; ok {
			if !mt.readOnly {
				t.Errorf("%s is writable: host git runs it", mt.container)
			}
			need[mt.container] = true
		}
	}
	for c, seen := range need {
		if !seen {
			t.Errorf("%s is not pinned read-only", c)
		}
	}
}

// Container paths are "/"-joined always. filepath.Join on a Windows daemon
// emits backslashes and the container gets a path it cannot resolve.
func TestMounts_ContainerSideNeverHasBackslashes(t *testing.T) {
	m := testMapping(t)
	for _, mt := range Mounts(m) {
		if strings.Contains(mt.container, `\`) {
			t.Errorf("container path %q has a backslash", mt.container)
		}
		if !strings.HasPrefix(mt.container, "/") {
			t.Errorf("container path %q is not absolute", mt.container)
		}
	}
}

// CRLF is the Windows-only entry, and CI runs on Linux — so without a hostGOOS
// parameter this shape would never be built, let alone asserted.
func TestEnv_AutocrlfOnlyOnAWindowsHost(t *testing.T) {
	m := testMapping(t)
	if !hasEnvKeyValue(Env(m, Identity{}, "windows"), "core.autocrlf", "true") {
		t.Error("a Windows host must set core.autocrlf, or the agent sees the whole tree as modified")
	}
	if hasEnvKeyValue(Env(m, Identity{}, "linux"), "core.autocrlf", "true") {
		t.Error("core.autocrlf must not be forced on a Linux host")
	}
}

// Without an identity the commit fails "Author identity unknown": the host's
// global git config is not mounted, deliberately.
func TestEnv_CarriesAuthorIdentity(t *testing.T) {
	m := testMapping(t)
	env := Env(m, Identity{GitUserName: "A Dev", GitUserEmail: "d@example.com"}, "linux")
	if !hasEnvKeyValue(env, "user.name", "A Dev") || !hasEnvKeyValue(env, "user.email", "d@example.com") {
		t.Error("author identity is not passed through")
	}
	// safe.directory is not optional: a non-root container user otherwise
	// refuses the bind-mounted repository, and non-root is required because
	// claude will not run --dangerously-skip-permissions as root.
	if !hasEnvKeyValue(env, "safe.directory", "*") {
		t.Error("safe.directory is missing")
	}
}

// GIT_CONFIG_COUNT must match the number of pairs actually emitted, or git
// reads a key that is not there and fails to start.
func TestEnv_GitConfigCountMatches(t *testing.T) {
	m := testMapping(t)
	for _, goos := range []string{"linux", "windows"} {
		env := Env(m, Identity{GitUserName: "n", GitUserEmail: "e"}, goos)
		count := envValue(env, "GIT_CONFIG_COUNT")
		var pairs int
		for _, e := range env {
			if strings.HasPrefix(e, "GIT_CONFIG_KEY_") {
				pairs++
			}
		}
		if count != itoa(pairs) {
			t.Errorf("%s: GIT_CONFIG_COUNT = %s but %d keys are set", goos, count, pairs)
		}
	}
}

// spawnPane logs full argv and the F1 viewer renders that log, so a token in
// argv is a token on screen and on disk. Docker forwards the value from its
// own environment when given the bare name.
func TestRunArgs_TokenNeverCarriesItsValue(t *testing.T) {
	m := testMapping(t)
	args := RunArgs(Spec{Image: "img"}, m, Identity{ForwardOAuthToken: true}, "linux", nil, "claude", nil)
	var found bool
	for _, a := range args {
		if a == oauthTokenVar {
			found = true
		}
		if strings.HasPrefix(a, oauthTokenVar+"=") {
			t.Fatalf("argv carries the token value: %q", a)
		}
	}
	if !found {
		t.Error("the token variable is not forwarded at all")
	}
}

// --rm would remove the container the instant it exits, taking its logs with
// it — and "the pane died and there is nothing to look at" is the worst
// failure this feature can produce.
func TestRunArgs_NoRmAndNoPrivileged(t *testing.T) {
	m := testMapping(t)
	args := RunArgs(Spec{Image: "img"}, m, Identity{}, "linux", nil, "claude", []string{"--x"})
	for _, a := range args {
		if a == "--rm" || a == "--privileged" {
			t.Errorf("argv contains %s", a)
		}
	}
	if args[len(args)-1] != "--x" || args[len(args)-2] != "claude" {
		t.Errorf("the pane's own command is not the argv tail: %v", args[len(args)-3:])
	}
}

// Both labels, always. Docker labels are engine-wide, so a sweep that filtered
// on quil.pane alone would let a dev daemon force-remove production containers.
func TestRunArgs_CarriesBothLabels(t *testing.T) {
	m := testMapping(t)
	args := RunArgs(Spec{Image: "img"}, m, Identity{QuilHomeLabel: "abc123"}, "linux", nil, "claude", nil)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "quil.pane=pane1") {
		t.Error("quil.pane label missing")
	}
	if !strings.Contains(joined, "quil.home=abc123") {
		t.Error("quil.home label missing — the sweep cannot tell daemons apart")
	}
}

func TestRunArgs_UserOnlyWhenSet(t *testing.T) {
	m := testMapping(t)
	if strings.Contains(strings.Join(RunArgs(Spec{Image: "i"}, m, Identity{}, "windows", nil, "c", nil), " "), "--user") {
		t.Error("--user must be omitted when the host does not need it")
	}
	if !strings.Contains(strings.Join(RunArgs(Spec{Image: "i"}, m, Identity{User: "1000:1000"}, "linux", nil, "c", nil), " "), "--user") {
		t.Error("--user is missing on a host that needs it")
	}
}

func TestImageOK(t *testing.T) {
	ok := []string{"alpine", "alpine:3", "ghcr.io/o/r:1.0", "r@sha256:abc"}
	bad := []string{"", "--privileged", "img;rm -rf /", "img $(x)", "img\nrun", "img `x`"}
	for _, s := range ok {
		if !ImageOK(s) {
			t.Errorf("ImageOK(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ImageOK(s) {
			t.Errorf("ImageOK(%q) = true, want false", s)
		}
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// hasEnvKeyValue checks a GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n pair, which is
// how git configuration reaches the container.
func hasEnvKeyValue(env []string, key, val string) bool {
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok || !strings.HasPrefix(k, "GIT_CONFIG_KEY_") || v != key {
			continue
		}
		idx := strings.TrimPrefix(k, "GIT_CONFIG_KEY_")
		if envValue(env, "GIT_CONFIG_VALUE_"+idx) == val {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
