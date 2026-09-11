package daemon

import (
	"reflect"
	"testing"

	"github.com/artyomsv/quil/internal/codexhook"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
	apty "github.com/artyomsv/quil/internal/pty"
)

const restartTestSessionID = "01a05db1-9f44-73b2-b426-8aad5f5232f4"

// TestHandleRestartPaneReq_CodexResumesItsRecordedSession drives the CALL SITE,
// and that is the whole point of it existing beside the resolveSpawnArgs table
// below.
//
// The bug was not in how a template becomes argv — that was always right. It
// was that restart reaches spawnPane with restoring=false, where only
// preassign_id was handled, so a codex pane was respawned with no resume
// argument at all and silently started a new conversation. Every helper on the
// path had a green test. A test that calls resolveSpawnArgs directly can be
// made to pass by a fix that the restart handler never reaches.
//
// Observed 2026-09-11 in a production daemon log: two codex panes restarted a
// minute apart, both `spawn: … restoring=false` with no `resume`, while
// $QUIL_HOME/sessions/codex-<paneID>.id sat on disk holding the id each had
// just been resumed with hours earlier.
func TestHandleRestartPaneReq_CodexResumesItsRecordedSession(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// AFTER newTestDaemon: newTestDaemonInDir installs its own newSessionFn and
	// restores the previous one in a Cleanup, so an override set before it is
	// silently replaced and every assertion below reads an untouched fake.
	origNew := newSessionFn
	fake := &fakeSession{}
	newSessionFn = func(cols, rows int) apty.Session { return fake }
	t.Cleanup(func() { newSessionFn = origNew })

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "codex"

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	if !fake.started {
		t.Fatal("restart did not spawn a child at all")
	}
	if !containsPair(fake.startArgs, "resume", restartTestSessionID) {
		t.Errorf("restart argv = %q\nwant it to carry `resume %s` — without it the pane abandons a live conversation and there is no way back to it",
			fake.startArgs, restartTestSessionID)
	}
}

// TestHandleRestartPaneReq_CodexWithNoRecordStartsFresh pins the OTHER
// direction, because the fix must not invent a session.
//
// codex.toml leaves resume_args empty on purpose: `resume --last` is codex's
// most-recent-session-in-CWD lookup, which on a multi-pane workspace finds a
// SIBLING pane's conversation. A restart with nothing recorded must therefore
// start clean rather than reach for the nearest transcript.
func TestHandleRestartPaneReq_CodexWithNoRecordStartsFresh(t *testing.T) {
	origExe, origRead := quildExeFn, readCodexSessionFn
	quildExeFn = func() (string, error) { return "/opt/quil/quild", nil }
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{}, nil
	}
	t.Cleanup(func() { quildExeFn, readCodexSessionFn = origExe, origRead })

	d := newTestDaemon(t)
	registerCodexPlugin(t, d)

	// AFTER newTestDaemon: newTestDaemonInDir installs its own newSessionFn and
	// restores the previous one in a Cleanup, so an override set before it is
	// silently replaced and every assertion below reads an untouched fake.
	origNew := newSessionFn
	fake := &fakeSession{}
	newSessionFn = func(cols, rows int) apty.Session { return fake }
	t.Cleanup(func() { newSessionFn = origNew })

	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatalf("CreatePane: %v", err)
	}
	pane.Type = "codex"

	msg, err := ipc.NewMessage(ipc.MsgRestartPaneReq, ipc.RestartPaneReqPayload{PaneID: pane.ID})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	d.handleRestartPaneReq(nil, msg)

	for _, a := range fake.startArgs {
		if a == "resume" || a == "--last" {
			t.Fatalf("restart argv = %q, want no resume with nothing recorded", fake.startArgs)
		}
	}
}

// TestResolveSpawnArgs_RestartResumesSessionScrape is the arg-merging matrix for
// the restart branch. Restore is covered by TestResolveSpawnArgs_CodexResume;
// this one fixes restoring=false, which is what Alt+R and the MCP restart_pane
// tool both pass.
func TestResolveSpawnArgs_RestartResumesSessionScrape(t *testing.T) {
	codexPlugin := &plugin.PanePlugin{
		Name:        plugin.CodexPluginName,
		Command:     plugin.CommandConfig{Cmd: "codex"},
		Persistence: plugin.PersistenceConfig{Strategy: "session_scrape", ResumeArgs: nil},
	}

	tests := []struct {
		name string
		pane *Pane
		rec  codexhook.SessionRecord
		want []string
	}{
		{
			"recorded id — restart rejoins it",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{ID: restartTestSessionID},
			[]string{"resume", restartTestSessionID},
		},
		{
			// The toggles the pane was created with are on InstanceArgs and must
			// survive a restart exactly as they survive a daemon restore —
			// otherwise Alt+R silently drops --search or the approval mode and
			// the pane comes back with different permissions than it had.
			"runtime toggles survive the restart",
			&Pane{ID: "pane-abc", InstanceArgs: []string{"--search"}},
			codexhook.SessionRecord{ID: restartTestSessionID},
			[]string{"--search", "resume", restartTestSessionID},
		},
		{
			"nothing recorded — restart starts fresh",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{},
			nil,
		},
		{
			// Shape validation is upstream in codexResumeTemplate, but it has to
			// hold on THIS branch too: a flag-shaped id reaching argv would make
			// the restart run `codex resume --last`, which is the sibling-session
			// trap the empty fallback exists to avoid.
			"flag-shaped id — restart starts fresh",
			&Pane{ID: "pane-abc"},
			codexhook.SessionRecord{ID: "--last"},
			nil,
		},
	}

	orig := readCodexSessionFn
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readCodexSessionFn = func(string) (codexhook.SessionRecord, error) { return tt.rec, nil }
			got := resolveSpawnArgs(codexPlugin, tt.pane, false, "", claimAny)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveSpawnArgs(restoring=false):\n  got:  %v\n  want: %v", got, tt.want)
			}
		})
	}
}

// TestResolveSpawnArgs_RestartLeavesOtherStrategiesAlone keeps the new branch
// from widening past session_scrape.
//
// preassign_id has its OWN restart handling a few lines above — it reads the
// pane's current record and emits `--resume <id>`, and reaching this branch as
// well would append a second resume to the same argv. cwd_only, rerun and none
// never resume at all, on restore or restart.
func TestResolveSpawnArgs_RestartLeavesOtherStrategiesAlone(t *testing.T) {
	orig := readCodexSessionFn
	readCodexSessionFn = func(string) (codexhook.SessionRecord, error) {
		return codexhook.SessionRecord{ID: restartTestSessionID}, nil
	}
	t.Cleanup(func() { readCodexSessionFn = orig })

	for _, strategy := range []string{"cwd_only", "rerun", "none", ""} {
		t.Run(strategy, func(t *testing.T) {
			p := &plugin.PanePlugin{
				Name:    plugin.CodexPluginName,
				Command: plugin.CommandConfig{Cmd: "codex"},
				Persistence: plugin.PersistenceConfig{
					Strategy:   strategy,
					ResumeArgs: []string{"resume", "{session_id}"},
				},
			}
			got := resolveSpawnArgs(p, &Pane{ID: "pane-abc"}, false, "", claimAny)
			if len(got) != 0 {
				t.Errorf("strategy %q restart argv = %v, want none", strategy, got)
			}
		})
	}
}

func containsPair(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

