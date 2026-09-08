package main

import (
	"testing"
)

// The variable name is the contract between this binary and the daemon: the
// daemon spells it independently, and docker is handed the NAME so it can read
// the value from its own environment.
func TestOAuthTokenVar_IsTheNameClaudeCodeReads(t *testing.T) {
	if oauthTokenVar != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("oauthTokenVar = %q; the daemon and docker both expect CLAUDE_CODE_OAUTH_TOKEN", oauthTokenVar)
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) != "yes" || yesNo(false) != "no" {
		t.Error("yesNo does not report both states")
	}
}
