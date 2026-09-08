package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubDocker captures the argv every call would have run, so the argv itself
// can be asserted.
//
// The daemon-side sweep test stubs its own list function and never sees these
// arguments at all — so it names "never reaps another daemon's container"
// while a mutation dropping the quil.home filter passes it. The label
// scoping is decided HERE, so it is tested here.
func stubDocker(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	var calls [][]string
	prev := runDocker
	runDocker = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, args)
		return out, err
	}
	t.Cleanup(func() { runDocker = prev })
	return &calls
}

// Both labels, always. Docker labels are engine-wide and the dev and
// production daemons share one engine, so filtering on quil.pane alone has a
// dev daemon classify every production sandbox container as an orphan.
func TestListPaneIDs_FiltersOnBothLabels(t *testing.T) {
	calls := stubDocker(t, "pane-a\npane-b\n", nil)

	ids, err := ListPaneIDs(context.Background(), "homehash")
	if err != nil {
		t.Fatalf("ListPaneIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %v, want two", ids)
	}
	argv := strings.Join((*calls)[0], " ")
	if !strings.Contains(argv, "label=quil.pane") {
		t.Error("the quil.pane filter is missing")
	}
	if !strings.Contains(argv, "label=quil.home=homehash") {
		t.Error("the quil.home filter is missing — this daemon would reap another install's containers")
	}
}

// `docker ps -aq --format …` prints "WARNING: Ignoring custom format, because
// both --format and --quiet are set" and returns container IDS. A sweep
// written that way compares ids against pane ids and matches nothing, silently.
func TestListPaneIDs_UsesDashAWithoutQuiet(t *testing.T) {
	calls := stubDocker(t, "", nil)
	if _, err := ListPaneIDs(context.Background(), "h"); err != nil {
		t.Fatalf("ListPaneIDs: %v", err)
	}
	for _, a := range (*calls)[0] {
		if a == "-aq" || a == "-qa" {
			t.Fatal("argv uses -aq: docker then ignores --format and returns container ids")
		}
	}
	var sawA, sawFormat bool
	for _, a := range (*calls)[0] {
		if a == "-a" {
			sawA = true
		}
		if strings.Contains(a, `{{.Label "quil.pane"}}`) {
			sawFormat = true
		}
	}
	if !sawA {
		t.Error("-a missing: stopped containers would not be reaped")
	}
	if !sawFormat {
		t.Error("the pane-id format is missing")
	}
}

func TestRemoveForce_TargetsThePanesContainer(t *testing.T) {
	calls := stubDocker(t, "", nil)
	if err := RemoveForce(context.Background(), "p1"); err != nil {
		t.Fatalf("RemoveForce: %v", err)
	}
	argv := strings.Join((*calls)[0], " ")
	if !strings.Contains(argv, "rm -f "+ContainerName("p1")) {
		t.Errorf("argv = %q, want a forced remove of this pane's container", argv)
	}
}

// A pane with no container is the normal case — every caller's next step is
// the same either way, and a spurious error would fail a teardown that has
// nothing to do.
func TestRemoveForce_MissingContainerIsNotAnError(t *testing.T) {
	stubDocker(t, "", errors.New("Error response from daemon: No such container: quil-p1"))
	if err := RemoveForce(context.Background(), "p1"); err != nil {
		t.Errorf("RemoveForce on a pane with no container: %v", err)
	}
}

// docker stop's grace is ten seconds per container and claude as PID 1 ignores
// SIGTERM, while the daemon's whole shutdown budget is five seconds.
func TestKill_UsesKillNotStop(t *testing.T) {
	calls := stubDocker(t, "", nil)
	if err := Kill(context.Background(), "p1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if (*calls)[0][0] != "kill" {
		t.Errorf("argv[0] = %q, want kill — stop cannot finish inside the shutdown budget", (*calls)[0][0])
	}
}

func TestKill_AlreadyStoppedIsNotAnError(t *testing.T) {
	stubDocker(t, "", errors.New("Error response from daemon: Container quil-p1 is not running"))
	if err := Kill(context.Background(), "p1"); err != nil {
		t.Errorf("Kill on a stopped container: %v", err)
	}
}

// Docker Desktop in Windows-containers mode answers an info probe perfectly
// well and then fails every linux image at run.
func TestProbe_NormalisesArchAndRequiresLinux(t *testing.T) {
	stubDocker(t, "29.6.2 linux x86_64\n", nil)
	info, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Arch != "amd64" {
		t.Errorf("Arch = %q, want the Go spelling", info.Arch)
	}
	if !info.Usable() {
		t.Error("a reachable linux engine is not usable")
	}

	stubDocker(t, "29.6.2 windows x86_64\n", nil)
	info, err = Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Usable() {
		t.Error("an engine running windows containers reads as usable")
	}
}

func TestProbe_Aarch64BecomesArm64(t *testing.T) {
	stubDocker(t, "29.6.2 linux aarch64\n", nil)
	info, _ := Probe(context.Background())
	if info.Arch != "arm64" {
		t.Errorf("Arch = %q, want arm64", info.Arch)
	}
}
