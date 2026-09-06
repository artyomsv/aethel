package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/config"
)

// TestSnapshotRestore_PaneSizeRoundTrip verifies pane dimensions survive the
// workspace snapshot → restore cycle. Without persisted cols/rows the daemon
// respawns every restored pane's ConPTY at the 80x24 default and the child
// boots at the wrong size — interactive TUIs (claude-code) then render an
// 80-column UI inside a full-width pane until the next window resize.
func TestSnapshotRestore_PaneSizeRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Cols = 238
	pane.Rows = 45

	// A second pane with no size recorded yet — must restore at 0/0 so the
	// respawn path falls back to the default constructor.
	pane2, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}

	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	restored := d2.session.Pane(pane.ID)
	if restored == nil {
		t.Fatalf("pane %s not restored", pane.ID)
	}
	if restored.Cols != 238 || restored.Rows != 45 {
		t.Errorf("restored size = %dx%d, want 238x45", restored.Cols, restored.Rows)
	}

	restored2 := d2.session.Pane(pane2.ID)
	if restored2 == nil {
		t.Fatalf("pane %s not restored", pane2.ID)
	}
	if restored2.Cols != 0 || restored2.Rows != 0 {
		t.Errorf("size-less pane restored as %dx%d, want 0x0", restored2.Cols, restored2.Rows)
	}
}

// A workspace snapshotted while the console-less-client bug was live holds
// "cols": 1, "rows": 1 for every pane. Giving newRestoredPTY a degenerate pair
// and letting it fall back to the default is NOT enough on its own, because the
// PTY is not the only reader of the stored pair — every one of these takes it
// straight off the pane:
//
//   - streamPTYOutput's first-output callback → paneSize → resizeKick, which
//     re-applies the stored size the moment the child writes a byte, undoing
//     the fallback and reflowing the transcript to one column;
//   - redrawKick's resize jiggle on attach;
//   - snapshot(), which writes the pair straight back out, so the poison
//     survives every restart until something normalises it.
//
// So the fix belongs at the restore boundary, where the pair enters the daemon.
// 0 is the field's documented "unknown" value (Pane.Cols), resizeKick already
// no-ops on it, and snapshot() only persists a positive pair — so normalising
// to 0 disarms all four readers at once and lets the first real client resize
// establish the truth.
func TestSnapshotRestore_DegenerateStoredSizeIsNormalisedAway(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")

	poisoned, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	poisoned.Cols, poisoned.Rows = 1, 1

	// A legitimately narrow SPLIT pane, which must survive untouched: it is
	// narrow in ONE dimension and wide in the other.
	narrow, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	narrow.Cols, narrow.Rows = 1, 46

	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}

	got := d2.session.Pane(poisoned.ID)
	if got == nil {
		t.Fatalf("pane %s not restored", poisoned.ID)
	}
	if got.Cols != 0 || got.Rows != 0 {
		t.Errorf("pane poisoned with 1x1 restored as %dx%d, want 0x0 — every "+
			"reader of the stored pair (resizeKick on first output, the attach "+
			"jiggle, the next snapshot) would otherwise re-apply one column",
			got.Cols, got.Rows)
	}

	gotNarrow := d2.session.Pane(narrow.ID)
	if gotNarrow == nil {
		t.Fatalf("pane %s not restored", narrow.ID)
	}
	if gotNarrow.Cols != 1 || gotNarrow.Rows != 46 {
		t.Errorf("narrow split pane restored as %dx%d, want 1x46 — only BOTH "+
			"dimensions at the floor is degenerate", gotNarrow.Cols, gotNarrow.Rows)
	}
}

// The reader Greptile named, exercised directly on the pair a restore produces:
// resizeKick must not put a restored pane back at one column. It is a no-op on
// 0, so this passes once the stored pair is normalised — and fails while the
// poisoned 1x1 survives restoration, which is what makes it a regression test
// for the whole chain rather than for the normalisation alone.
func TestResizeKick_RestoredDegenerateSizeNeverReachesThePTY(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("QUIL_HOME", tmp)

	d := New(config.Default())
	tab := d.session.CreateTab("Shell")
	pane, err := d.session.CreatePane(tab.ID, "/tmp")
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Cols, pane.Rows = 1, 1
	d.snapshot()

	d2 := New(config.Default())
	if err := d2.restoreWorkspace(); err != nil {
		t.Fatalf("restoreWorkspace: %v", err)
	}
	restored := d2.session.Pane(pane.ID)
	if restored == nil {
		t.Fatalf("pane %s not restored", pane.ID)
	}

	// Exactly what streamPTYOutput's first-output callback does.
	fake := &fakeSession{}
	cols, rows := paneSize(restored)
	resizeKick(fake, cols, rows)

	for _, r := range fake.resizes {
		if r[0] <= 1 && r[1] <= 1 {
			t.Fatalf("the first-output kick resized the PTY to %dx%d (rows x cols) "+
				"— the restored pane's stored size undid the spawn-time fallback",
				r[0], r[1])
		}
	}
}
