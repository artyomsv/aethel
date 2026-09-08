// Package claudetoken obtains a long-lived Claude Code token by running
// Anthropic's own `claude setup-token` and reading the token out of its output.
//
// It is the automation behind "open a sandbox pane and it is signed in": no
// token to copy, no environment variable to set by hand.
//
// A plain pipe does not work, and that was MEASURED rather than assumed: with
// stdout redirected the command emits nothing at all, opens the browser, and
// waits. It needs a real terminal, which is the one thing this codebase already
// has a cross-platform implementation of.
//
// Nothing here PERSISTS the token. The caller hands it to the OS environment
// store; Quil keeps no copy, because `claude setup-token` mints a Claude
// session token and storing one reads as "collect, store, or intermediate …
// session tokens" under Anthropic's authentication policy.
package claudetoken

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/artyomsv/quil/internal/pty"
)

// TokenRe matches the credential `claude setup-token` prints.
//
// Anchored on the `sk-ant-` prefix and then permissive about the rest: the
// characters after it are an opaque, versioned blob, and a tighter pattern
// would stop matching the first time Anthropic changes its shape — failing in
// the worst way, since the flow would have completed and the token would be on
// screen and discarded.
var TokenRe = regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)

// Budget bounds the whole flow. Generous because the slow step is a human
// authorising in a browser, not the command.
const Budget = 5 * time.Minute

// Cols is the PTY width, and it is deliberately far wider than any real
// terminal.
//
// A pseudo-terminal HARD-WRAPS at its own width — it inserts a real newline
// rather than letting the display reflow — so a token longer than the PTY is
// split by bytes indistinguishable from a line the program meant to end.
// Choosing the width is free here, and a wide one makes the wrap impossible
// instead of making the scanner guess.
const Cols = 400

// Options tunes one capture.
type Options struct {
	// Mirror receives the child's output verbatim, for a caller that has a
	// terminal to show it on. nil discards it.
	//
	// The DAEMON passes nil deliberately, and that is a security decision
	// rather than a convenience: its only mirror would be a pane, and a pane's
	// output is written to a ghost buffer on disk — so mirroring would persist
	// the token, which is the one thing this package exists not to do. It
	// reports progress in its own words instead.
	Mirror io.Writer

	// Input is forwarded to the child, for the "paste the code here" fallback
	// when a browser callback cannot reach back. nil forwards nothing.
	Input io.Reader

	// Args replaces the arguments passed to the command. Empty means
	// `setup-token`, which is what every production caller wants.
	//
	// It exists so a test can drive the REAL pty + scanner + return-condition
	// path against a command it controls. The condition that matters — return
	// on the token, not on process exit — cannot be exercised any other way:
	// `claude setup-token` keeps running after printing, and a test that has
	// to run the real thing is a test nobody runs.
	Args []string
}

// Capture runs `claude setup-token` under a PTY and returns the token it
// printed.
func Capture(claudePath string, opts Options) (string, error) {
	s := pty.NewWithSize(Cols, 30)
	args := opts.Args
	if len(args) == 0 {
		args = []string{"setup-token"}
	}
	if err := s.Start(claudePath, args...); err != nil {
		return "", fmt.Errorf("starting `claude setup-token`: %w", err)
	}
	defer s.Close()

	var (
		mu      sync.Mutex
		scan    strings.Builder
		token   string
		readErr error
	)
	// TWO signals, and needing both is the bug this shape fixes. `claude
	// setup-token` PRINTS the token and keeps running — it does not exit — so
	// waiting on the process alone sat here for the whole budget and then
	// reported failure for a sign-in the user had already completed. found
	// fires the moment the token appears; done still matters, because a
	// command that exits WITHOUT printing one has failed and must not be
	// waited out either.
	found := make(chan struct{})
	done := make(chan struct{})
	var foundOnce sync.Once

	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			if n > 0 {
				if opts.Mirror != nil {
					_, _ = opts.Mirror.Write(buf[:n])
				}
				mu.Lock()
				scan.Write(buf[:n])
				// Matched against the whole accumulated stream rather than the
				// chunk: a 4 KiB read can split the token, and a per-chunk
				// scan would miss exactly the one that matters.
				if m := TokenRe.FindString(ansi.Strip(scan.String())); m != "" && token == "" {
					token = m
					foundOnce.Do(func() { close(found) })
				}
				mu.Unlock()
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					mu.Lock()
					readErr = err
					mu.Unlock()
				}
				return
			}
		}
	}()

	if opts.Input != nil {
		// Forwarded best-effort and never waited on: this blocks in a read on
		// a stdin that nothing can interrupt, so it is left to die with the
		// process rather than being part of the shutdown path.
		go func() { _, _ = io.Copy(ptyWriter{s}, opts.Input) }()
	}

	select {
	case <-found:
		// The token is on screen and the flow is over, whatever the process
		// does next. The deferred Close ends it.
	case <-done:
		// Exited without printing one.
	case <-time.After(Budget):
		return "", fmt.Errorf("`claude setup-token` did not finish within %s", Budget)
	}

	mu.Lock()
	defer mu.Unlock()
	if token != "" {
		return token, nil
	}
	if readErr != nil {
		return "", fmt.Errorf("reading from `claude setup-token`: %w", readErr)
	}
	return "", errors.New("no token found in the output of `claude setup-token`")
}

// Find locates the claude binary, so a caller can refuse early with a message
// about the tool rather than a failed exec.
func Find() (string, error) {
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("`claude` is not on PATH")
	}
	return p, nil
}

// ptyWriter adapts a pty.Session to io.Writer. Session has the right Write
// method already; this only gives io.Copy a concrete type to hold.
type ptyWriter struct{ s pty.Session }

func (w ptyWriter) Write(p []byte) (int, error) { return w.s.Write(p) }
