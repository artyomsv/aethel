package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/claudetoken"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	"github.com/artyomsv/quil/internal/sandbox"
)

// Opening a sandbox pane with no token must trigger the sign-in, or the pane
// falls through to a per-container login the user has to repeat for every pane
// — the state this whole flow exists to remove.
func TestNeedsSandboxSignIn(t *testing.T) {
	tests := []struct {
		name  string
		auth  string
		token string
		want  bool
	}{
		{"token flow, no token", "", "", true},
		{"token flow explicit, no token", "token", "", true},
		{"token flow, token present", "", "sk-ant-x", false},
		// The user chose the per-pane sign-in. Hijacking that with a browser
		// window would override an explicit decision.
		{"browser chosen, no token", "browser", "", false},
		{"browser chosen, token present", "browser", "sk-ant-x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(oauthTokenEnv, tt.token)
			d := &Daemon{cfg: config.Default()}
			d.cfg.Sandbox.Auth = tt.auth
			if got := d.needsSandboxSignIn(nil); got != tt.want {
				t.Errorf("needsSandboxSignIn = %v, want %v", got, tt.want)
			}
		})
	}
}

// Two panes created at once must not each open a browser. The loser stands
// down and is recorded, so the winner spawns it too — a waiter that is merely
// refused would sit with a message and never get a child.
func TestSandboxSignIn_SingleFlightRecordsWaiters(t *testing.T) {
	var s sandboxSignIn

	if !s.begin("pane-1") {
		t.Fatal("the first caller did not get ownership")
	}
	if s.begin("pane-2") {
		t.Error("a second caller also got ownership; two browser flows would open")
	}
	if s.begin("pane-3") {
		t.Error("a third caller also got ownership")
	}

	waiters := s.end()
	if len(waiters) != 2 || waiters[0] != "pane-2" || waiters[1] != "pane-3" {
		t.Errorf("waiters = %v, want both panes that stood down — otherwise they never spawn", waiters)
	}

	// end() must also release the claim, or the next sign-in can never start.
	if !s.begin("pane-4") {
		t.Error("ownership was not released")
	}
	if w := s.end(); len(w) != 0 {
		t.Errorf("waiters = %v after a fresh round; the list was not cleared", w)
	}
}

// --- the on switch ---
//
// Everything above tests the sign-in's LOGIC. None of it reaches the wiring,
// and the wiring is where this feature has broken four separate times.
// Disabling the call in spawnPane leaves every logic test green while a user
// opening a sandbox pane is asked to sign in inside the container again — the
// exact state the flow exists to remove.

// The real spawn path must hand a token-less sandbox pane to the sign-in,
// stand down, and leave the pane childless for the goroutine to spawn.
func TestSpawnPane_SandboxWithNoTokenStartsTheSignIn(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"
	t.Setenv(oauthTokenEnv, "")
	d.cfg = config.Default() // token flow by default

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	captured := make(chan string, 1)
	prevCap := captureTokenFn
	captureTokenFn = func(path string, _ claudetoken.Options) (string, error) {
		captured <- path
		return "", errors.New("stopped in the test")
	}
	t.Cleanup(func() { captureTokenFn = prevCap })

	// Available, so the spawn reaches the sign-in branch rather than refusing.
	prevProbe := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		return sandbox.Info{ServerVersion: "1", OSType: "linux"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prevProbe })

	if err := d.spawnPane(pane, &fakeSpawnSession{}, false); err != nil {
		t.Fatalf("spawnPane = %v; standing down for a sign-in is not a failure", err)
	}

	select {
	case path := <-captured:
		if path != "/fake/claude" {
			t.Errorf("capture ran with %q, want the located claude", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in never started — a sandbox pane with no token asked the " +
			"user to sign in inside the container instead")
	}
}

// A pane that already has a token must take the ordinary path untouched, or
// every sandbox pane pays for a browser flow it does not need.
func TestSpawnPane_SandboxWithATokenSkipsTheSignIn(t *testing.T) {
	d, pane, _ := sandboxCallsiteFixture(t)
	withClaudePlugin(t, d)
	pane.Type = "claude-code"
	t.Setenv(oauthTokenEnv, "sk-ant-already-signed-in")
	d.cfg = config.Default()

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) {
		t.Error("the sign-in was started for a pane that already has a token")
		return "", errors.New("should not be reached")
	}
	t.Cleanup(func() { findClaudeFn = prevFind })

	if d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: "claude-code"}) {
		t.Error("beginSandboxSignIn took ownership with a token already present")
	}
}

// A restarted daemon must USE the saved token, not mint a new one.
//
// Its environment comes from whatever spawned it — normally the TUI — and the
// TUI's own environment is a snapshot from ITS start, so a token saved after
// that point is invisible to both. The sign-in therefore ran again on every
// restart, and each `claude setup-token` mints a NEW long-lived token, which
// supersedes the one live panes are already using. Observed three times in one
// session, with a running pane losing its credential.
func TestNeedsSandboxSignIn_AdoptsTheSavedToken(t *testing.T) {
	t.Setenv(oauthTokenEnv, "") // as a freshly spawned daemon has it

	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "sk-ant-saved-earlier", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: config.Default()}
	if d.needsSandboxSignIn(nil) {
		t.Error("a second sign-in was started while a saved token was available — " +
			"the new token supersedes the one running panes are using")
	}
	if os.Getenv(oauthTokenEnv) == "" {
		t.Error("the saved token was not adopted into the process environment, so " +
			"dockerCLIEnv has nothing to hand docker")
	}
}

// With nothing saved anywhere the sign-in must still run, or the feature is off.
func TestNeedsSandboxSignIn_NoSavedTokenStillSignsIn(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")

	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: config.Default()}
	if !d.needsSandboxSignIn(nil) {
		t.Error("no sign-in was started with no token anywhere")
	}
}

// The pane's OWN choice must beat the config, or the dialog's radio is
// decoration. Both directions, because each failure is different: a browser
// pane hijacked by a host token silently loses Fable and Remote Control, and a
// token pane forced to sign in is the per-pane login the choice exists to
// avoid.
func TestPaneAuthMode_ThePaneChoiceBeatsTheConfig(t *testing.T) {
	tests := []struct {
		name       string
		configAuth string
		paneAuth   string
		want       config.SandboxAuthMode
	}{
		{"pane picks browser against a token config", "token", "browser", config.SandboxAuthBrowser},
		{"pane picks token against a browser config", "browser", "token", config.SandboxAuthToken},
		{"untouched pane follows the config", "browser", "", config.SandboxAuthBrowser},
		{"untouched pane follows the default", "", "", config.SandboxAuthToken},
		// A snapshot from a future version must not pin a pane to a mode this
		// build cannot honour.
		{"unknown stored value falls back to the config", "browser", "future-mode", config.SandboxAuthBrowser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{cfg: config.Default()}
			d.cfg.Sandbox.Auth = tt.configAuth
			pane := &Pane{ID: "p1", SandboxAuth: tt.paneAuth}
			if got := d.paneAuthMode(pane); got != tt.want {
				t.Errorf("paneAuthMode = %q, want %q", got, tt.want)
			}
		})
	}
}

// A browser pane must NOT be handed the host token: that is the credential it
// was chosen to avoid.
func TestNeedsSandboxSignIn_ABrowserPaneNeverUsesTheHostToken(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	d := &Daemon{cfg: config.Default()} // token flow configured
	pane := &Pane{ID: "p1", SandboxAuth: "browser"}

	if d.needsSandboxSignIn(pane) {
		t.Error("a browser pane started the HOST sign-in; it must sign in inside its own container")
	}
	if d.sandboxTokenAvailable(pane, "claude-code") {
		t.Error("a browser pane reports a token available; its config would be seeded and " +
			"the in-container sign-in screen hidden")
	}
}

// And a token pane still gets the automated sign-in even when the config says
// browser, or picking "Token" in the dialog buys nothing.
func TestNeedsSandboxSignIn_ATokenPaneSignsInAgainstABrowserConfig(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	d := &Daemon{cfg: config.Default()}
	d.cfg.Sandbox.Auth = "browser"
	pane := &Pane{ID: "p1", SandboxAuth: "token"}

	if !d.needsSandboxSignIn(pane) {
		t.Error("a token pane did not start the sign-in; the dialog's choice was ignored")
	}
}

// applySandboxSpec is the ONE place a wire spec becomes pane state, and it
// must record the auth as well as the image. Dropping it here is invisible
// everywhere else: the pane simply follows the config, so the dialog's radio
// silently does nothing.
func TestApplySandboxSpec_RecordsTheAuthChoice(t *testing.T) {
	tests := []struct {
		name, wire, want string
	}{
		{"browser is recorded", "browser", "browser"},
		{"token is recorded", "token", "token"},
		{"absent follows the config", "", ""},
		// Any IPC client can set this, and the two modes hand the container
		// different credentials — so an unknown value is dropped to "follow
		// the config" rather than stored and acted on.
		{"unknown is refused", "superuser", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := &Pane{ID: "p1"}
			if err := applySandboxSpec(pane, &ipc.SandboxSpec{Image: "img:1", Auth: tt.wire}); err != nil {
				t.Fatalf("applySandboxSpec: %v", err)
			}
			pane.PluginMu.Lock()
			got := pane.SandboxAuth
			pane.PluginMu.Unlock()
			if got != tt.want {
				t.Errorf("SandboxAuth = %q, want %q — the dialog's choice never reaches the spawn", got, tt.want)
			}
		})
	}
}

// The Claude sign-in must never fire for another vendor's agent.
//
// It runs `claude setup-token` and opens a browser. Codex keeps its own
// ~/.codex credentials and opencode its own again, so for those panes this is
// a browser window for an account the user did not ask about — and it shipped
// that way: beginSandboxSignIn took no plugin at all.
func TestBeginSandboxSignIn_OnlyForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	for _, name := range []string{"codex", "opencode", "terminal"} {
		t.Run(name, func(t *testing.T) {
			d := &Daemon{cfg: config.Default(), session: NewSessionManager(1024), events: newEventQueue(50)}
			pane := &Pane{ID: "p1", SandboxImage: "img"}
			if d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: name}) {
				t.Errorf("a %s pane started the CLAUDE sign-in — a browser window for "+
					"an account it does not use", name)
			}
		})
	}
}

// And it must still fire for claude-code, or the gate turned the feature off.
func TestBeginSandboxSignIn_StillFiresForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "")
	prevGet := userenvGet
	userenvGet = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { userenvGet = prevGet })

	prevFind := findClaudeFn
	findClaudeFn = func() (string, error) { return "/fake/claude", nil }
	t.Cleanup(func() { findClaudeFn = prevFind })

	prevCap := captureTokenFn
	captureTokenFn = func(string, claudetoken.Options) (string, error) {
		return "", errors.New("stopped in the test")
	}
	t.Cleanup(func() { captureTokenFn = prevCap })

	d := &Daemon{cfg: config.Default(), session: NewSessionManager(1024), events: newEventQueue(50)}
	pane := &Pane{ID: "p1", SandboxImage: "img"}
	if !d.beginSandboxSignIn(pane, &plugin.PanePlugin{Name: "claude-code"}) {
		t.Error("the sign-in no longer fires for claude-code")
	}
}

// A codex or opencode container must not be handed a Claude token, and its
// Claude config must not be seeded — those first-run screens are not the ones
// it shows.
func TestSandboxTokenAvailable_OnlyForClaudeCode(t *testing.T) {
	t.Setenv(oauthTokenEnv, "sk-ant-present")
	d := &Daemon{cfg: config.Default()}
	pane := &Pane{ID: "p1"}

	if !d.sandboxTokenAvailable(pane, "claude-code") {
		t.Error("claude-code lost its token")
	}
	for _, name := range []string{"codex", "opencode"} {
		if d.sandboxTokenAvailable(pane, name) {
			t.Errorf("a %s pane reports a Claude token available", name)
		}
	}
}
