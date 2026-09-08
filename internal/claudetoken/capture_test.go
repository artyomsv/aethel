package claudetoken

import (
	"os/exec"
	"time"

	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTokenRe_FindsATokenInTerminalOutput(t *testing.T) {
	const token = "sk-ant-oat01-" + "AbCdEf0123456789_-XyZaBcDeFgHiJkLmNoP"

	tests := []struct {
		name string
		in   string
	}{
		{"bare on its own line", "Success!\n" + token + "\n"},
		{"wrapped in prose", "Your token is: " + token + " — keep it safe\n"},
		{"carriage returns", "done\r\n" + token + "\r\n"},
		// The stream is a PTY, so it is full of escapes. The scanner strips
		// them first; this pins that the two steps compose.
		{"styled", "\x1b[1;32m" + token + "\x1b[0m\n"},
		{"cursor moves around it", "\x1b[2K\r" + token + "\x1b[1A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenRe.FindString(ansi.Strip(tt.in)); got != token {
				t.Errorf("found %q, want %q", got, token)
			}
		})
	}
}
func TestTokenRe_DoesNotMatchProse(t *testing.T) {
	for _, in := range []string{
		"Paste the sk-ant- token here",  // the prompt naming the prefix
		"visit https://claude.ai/oauth", // a URL from the flow
		"sk-ant-short",                  // too short to be a token
		"",                              // nothing yet
	} {
		if got := TokenRe.FindString(in); got != "" {
			t.Errorf("matched %q in %q — that would be persisted as a credential", got, in)
		}
	}
}
func TestTokenRe_SurvivesASplitAcrossReads(t *testing.T) {
	const token = "sk-ant-oat01-" + "AbCdEf0123456789_-XyZaBcDeFgHiJkLmNoP"
	chunks := []string{"Success!\nsk-ant-oat01-AbCdEf012345", "6789_-XyZaBcDeFgHiJkLmNoP\n"}

	var acc strings.Builder
	var found string
	for _, c := range chunks {
		acc.WriteString(c)
		if m := TokenRe.FindString(ansi.Strip(acc.String())); m != "" && found == "" {
			found = m
		}
	}
	if found != token {
		t.Errorf("accumulated scan found %q, want %q", found, token)
	}

	// And the per-chunk scan this replaced finds nothing — so the test above
	// is not passing for an unrelated reason.
	for _, c := range chunks {
		if m := TokenRe.FindString(ansi.Strip(c)); m == token {
			t.Fatal("a per-chunk scan already finds the whole token; this fixture proves nothing")
		}
	}
}
func TestCols_WiderThanAnyToken(t *testing.T) {
	if Cols < 300 {
		t.Errorf("Cols = %d; a token plus surrounding text must not reach the wrap column", Cols)
	}
}

// Capture must return when the TOKEN appears, not when the process exits.
//
// `claude setup-token` prints the token and then KEEPS RUNNING. Waiting on the
// process alone sat for the whole five-minute budget and then reported failure
// for a sign-in the user had already completed in the browser — observed on a
// real run, with the pane black the whole time.
//
// The fake prints a token and then sleeps far past this test's patience, so a
// pass is only possible if the token itself is the return condition.
func TestCapture_ReturnsOnTheTokenNotOnExit(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	const token = "sk-ant-oat01-AbCdEf0123456789_-XyZaBcDeFgHiJkLmNoP"

	start := time.Now()
	got, err := Capture(sh, Options{
		Args: []string{"-c", "echo " + token + "; sleep 120"},
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != token {
		t.Errorf("token = %q, want %q", got, token)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Capture took %s — it waited for the process instead of the token", elapsed)
	}
}

// A command that exits WITHOUT printing a token must be reported, not waited
// out: that is a failed sign-in, and the budget is for a human in a browser.
func TestCapture_ReportsACommandThatPrintsNoToken(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	start := time.Now()
	if _, err := Capture(sh, Options{Args: []string{"-c", "echo no token here"}}); err == nil {
		t.Error("Capture succeeded for a command that printed no token")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("took %s — a command that exited was waited out", elapsed)
	}
}

// The token is opaque and {20,} is a floor, not its length — so a read that
// ends 25 characters into a 43-character token leaves a match that satisfies
// the pattern and is a PREFIX of the credential. Accepting it persists a
// broken token to the user environment and forwards it to every container,
// where it fails to authenticate with nothing on screen to say why.
func TestCompleteToken_WaitsForTheEndOfTheToken(t *testing.T) {
	const token = "sk-ant-oat01-AbCdEf0123456789_-XyZaBcDeFgHiJkLmNoP"
	const cut = 32 // "sk-ant-" + 25 token characters
	head := token[:cut]

	// The premise: the bare regex is already satisfied by the head, so this
	// fixture reaches the branch under test rather than passing vacuously.
	if m := TokenRe.FindString(head); m == "" {
		t.Fatalf("fixture is wrong: the regex does not match %q at all", head)
	} else if m == token {
		t.Fatal("fixture is wrong: the head already contains the whole token")
	}

	if got := completeToken(head, false); got != "" {
		t.Errorf("accepted %q from a stream that has not ended — that is a truncated credential", got)
	}
	if got := completeToken(token+"\n", false); got != token {
		t.Errorf("with a delimiter present: got %q, want %q", got, token)
	}
	// A stream can legitimately end on the token's last byte, and then no
	// delimiter is ever coming.
	if got := completeToken(token, true); got != token {
		t.Errorf("at EOF: got %q, want %q", got, token)
	}
	if got := completeToken("no token here", true); got != "" {
		t.Errorf("at EOF with no token: got %q, want \"\"", got)
	}
}

// The same defect through Capture, because the decision that matters is the
// one the reader goroutine makes on a real split read — a direct-call test
// cannot see a call site that never consults it.
func TestCapture_DoesNotReturnAPrefixOfASplitToken(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	const token = "sk-ant-oat01-AbCdEf0123456789_-XyZaBcDeFgHiJkLmNoP"
	const cut = 32

	// Two writes with a pause between them is a split read, exactly as a slow
	// terminal produces one. The trailing sleep keeps the process alive so a
	// pass cannot come from the exit path.
	script := "printf %s '" + token[:cut] + "'; sleep 1; printf '%s\\n' '" + token[cut:] + "'; sleep 120"

	got, err := Capture(sh, Options{Args: []string{"-c", script}})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got != token {
		t.Errorf("token = %q, want %q — a prefix here is a credential that cannot authenticate", got, token)
	}
}
