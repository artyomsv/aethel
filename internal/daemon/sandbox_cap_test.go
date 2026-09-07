package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/artyomsv/quil/internal/sandbox"
)

// stubProbe points the capability probe at a canned answer and counts calls.
func stubProbe(t *testing.T, info sandbox.Info, err error) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	prev := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		calls.Add(1)
		return info, err
	}
	t.Cleanup(func() { sandboxProbeFn = prev })
	return &calls
}

func TestProbeSandbox_LinuxEngineIsAvailable(t *testing.T) {
	stubProbe(t, sandbox.Info{ServerVersion: "29.6.2", OSType: "linux", Arch: "amd64"}, nil)

	got := probeSandbox(context.Background())
	if !got.Available {
		t.Fatalf("Available = false for a reachable linux engine: %+v", got)
	}
	if got.Arch != "amd64" {
		t.Errorf("Arch = %q, want the Go spelling", got.Arch)
	}
}

// Docker Desktop in Windows-containers mode answers `docker info` perfectly
// well and then fails every linux image at run. Gating on reachability alone
// would offer the user a pane that cannot start.
func TestProbeSandbox_WindowsContainersAreNotAvailable(t *testing.T) {
	stubProbe(t, sandbox.Info{ServerVersion: "29.6.2", OSType: "windows"}, nil)

	got := probeSandbox(context.Background())
	if got.Available {
		t.Fatal("Available = true for an engine running windows containers")
	}
	if got.Error == "" {
		t.Error("no reason given; the dialog cannot explain itself")
	}
}

func TestProbeSandbox_UnreachableEngineCarriesAReason(t *testing.T) {
	stubProbe(t, sandbox.Info{}, errors.New("cannot connect to the docker daemon"))

	got := probeSandbox(context.Background())
	if got.Available {
		t.Fatal("Available = true with no engine")
	}
	if got.Error == "" {
		t.Fatal("no reason given")
	}
}

// The daemon runs for weeks while Docker Desktop starts and stops, so the
// answer must expire — but not so eagerly that a dialog probes on every ask.
func TestSandboxCap_CachesWithinTTL(t *testing.T) {
	calls := stubProbe(t, sandbox.Info{ServerVersion: "1", OSType: "linux"}, nil)
	var c sandboxCap

	for i := 0; i < 5; i++ {
		if !c.get(context.Background()).Available {
			t.Fatal("Available = false")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("probed %d times, want 1 — the answer is not cached", n)
	}
}

func TestSandboxCap_RefetchesAfterTTL(t *testing.T) {
	calls := stubProbe(t, sandbox.Info{ServerVersion: "1", OSType: "linux"}, nil)
	var c sandboxCap

	c.get(context.Background())
	c.fetched = time.Now().Add(-2 * sandboxCapTTL)
	c.get(context.Background())

	if n := calls.Load(); n != 2 {
		t.Errorf("probed %d times, want 2 — a stale answer was reused", n)
	}
}

// The dialog asks at attach and again at open, so the two can overlap. A
// second asker must wait for the first answer rather than start a second probe
// against the same engine — and must not be REJECTED the way the other dialog
// RPCs reject a concurrent request, because it wants the very answer being
// fetched.
func TestSandboxCap_ConcurrentCallersShareOneProbe(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int64
	prev := sandboxProbeFn
	sandboxProbeFn = func(context.Context) (sandbox.Info, error) {
		calls.Add(1)
		<-release
		return sandbox.Info{ServerVersion: "1", OSType: "linux"}, nil
	}
	t.Cleanup(func() { sandboxProbeFn = prev })

	var c sandboxCap
	var wg sync.WaitGroup
	answers := make([]bool, 8)
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = c.get(context.Background()).Available
		}(i)
	}
	// Let every caller reach the probe or the wait before it can finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("probed %d times, want 1", n)
	}
	for i, ok := range answers {
		if !ok {
			t.Errorf("caller %d got Available = false; a waiter did not receive the answer", i)
		}
	}
}

// A never-probed cache must not read as available: the client treats an
// unavailable daemon as one that cannot host a sandbox, and a zero value that
// said otherwise would offer a pane that fails at spawn.
func TestSandboxCap_ZeroValueIsUnavailable(t *testing.T) {
	var c sandboxCap
	if c.answer.Available {
		t.Error("the zero value reads as available")
	}
}
