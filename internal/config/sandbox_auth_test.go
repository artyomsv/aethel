package config

import "testing"

// The design chose the token flow as the main path and the in-container
// sign-in as the fallback. The code shipped the reverse, because Auth's zero
// value is "" and "" meant browser — so a config that never mentioned auth
// silently selected the fallback and every pane asked the user to sign in
// again, with nothing explaining why.
func TestResolveAuth(t *testing.T) {
	tests := []struct {
		name           string
		auth           string
		wantMode       SandboxAuthMode
		wantUnrecognis string
	}{
		// The load-bearing row. Every config.toml already on disk names
		// `auth = ""`, and Load lets the decoder overwrite the keys a file
		// names — so changing Default() alone would reach no existing
		// install. This is the only change that reaches everyone.
		{"unset resolves to the token flow", "", SandboxAuthToken, ""},
		{"token is explicit", "token", SandboxAuthToken, ""},
		{"browser selects the fallback", "browser", SandboxAuthBrowser, ""},
		// A typo must not silently look like a deliberate choice.
		{"a typo falls back and is reported", "toekn", SandboxAuthBrowser, "toekn"},
		{"case is not guessed at", "Token", SandboxAuthBrowser, "Token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, unrecognised := SandboxConfig{Auth: tt.auth}.ResolveAuth()
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if unrecognised != tt.wantUnrecognis {
				t.Errorf("unrecognised = %q, want %q", unrecognised, tt.wantUnrecognis)
			}
		})
	}
}

// Default() does not mention Sandbox at all, so the zero value IS the shipped
// default. If that ever changes, the migration reasoning above changes with
// it — this pins the premise rather than the value.
func TestDefault_SandboxAuthIsTheZeroValue(t *testing.T) {
	if got := Default().Sandbox.Auth; got != "" {
		t.Errorf("Default().Sandbox.Auth = %q; the ResolveAuth migration assumes the zero value", got)
	}
	if mode, _ := Default().Sandbox.ResolveAuth(); mode != SandboxAuthToken {
		t.Errorf("a default config resolves to %q, want the token flow", mode)
	}
}
