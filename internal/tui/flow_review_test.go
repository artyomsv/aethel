package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/ipc"
)

// newFlowAtRow opens New flow through Update and parks the cursor on row.
func newFlowAtRow(t *testing.T, row int) Model {
	t.Helper()
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.projects[0].RootDir = "/repo/root"
	m.dialog = dialogCommandPalette
	m.palette = paletteState{filtered: []paletteCommand{{action: palActNewFlow, label: "New flow", enabled: true}}}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for i := 0; i < row; i++ {
		m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	}
	return m
}

func flowSentPaneInput(m Model) bool {
	conn := m.client.(*fakeConn)
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, msg := range conn.sent {
		if msg.Type == ipc.MsgPaneInput {
			return true
		}
	}
	return false
}

// A flow dialog must consume clipboard text itself. Update had no flow arm for
// tea.PasteMsg at all, so every paste with the dialog open was typed into the
// pane behind it — a clipboard leak into a live shell, which would run a
// pasted line ending in a newline.
func TestFlowUpdate_Paste_ReachesTheFocusedFieldAndNeverThePane(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   int
		paste tea.Msg
		want  func(Model) string
	}{
		{"repository row, terminal paste", 2, tea.PasteMsg{Content: "/chosen/repository"}, func(m Model) string { return m.flowUI.cwd }},
		{"repository row, clipboard paste", 2, editorPasteMsg("/chosen/repository"), func(m Model) string { return m.flowUI.cwd }},
		{"branch row, terminal paste", 1, tea.PasteMsg{Content: "/chosen/repository"}, func(m Model) string { return m.flowUI.branch }},
		{"feature editor, terminal paste", 0, tea.PasteMsg{Content: "/chosen/repository"}, func(m Model) string { return m.flowUI.editor.Content() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFlowAtRow(t, tc.row)
			m = flowUpdate(t, m, tc.paste)
			if got := tc.want(m); !strings.Contains(got, "/chosen/repository") {
				t.Fatalf("focused field did not take the paste: %q", got)
			}
			if flowSentPaneInput(m) {
				t.Fatal("paste leaked into the pane behind the dialog")
			}
		})
	}

	// A settings list row has no field. The paste is swallowed all the same.
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	m.flowUI.row = 0 // max review rounds
	m = flowUpdate(t, m, tea.PasteMsg{Content: "some clipboard text\n"})
	if flowSentPaneInput(m) {
		t.Fatal("paste on a settings list row leaked into the pane")
	}
}

func TestFlowUpdate_SettingsModelRow_TakesPasteNotThePane(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	for i, row := range m.flowSettingRows() {
		if row.role == flow.Analyst && row.kind == "model" {
			m.flowUI.row = i
		}
	}
	m = flowUpdate(t, m, tea.PasteMsg{Content: "claude-opus-5"})
	if got := m.flowUI.cfg.Roles[flow.Analyst].Model; got != "claude-opus-5" {
		t.Fatal("model row did not take the paste", got)
	}
	if flowSentPaneInput(m) {
		t.Fatal("paste leaked into the pane behind the settings dialog")
	}
}

// msg.String() spells the space key "space", so the old single-rune test
// dropped it and no path with a space in it could be typed.
func TestFlowUpdate_RepositoryRow_AcceptsSpacesInPaths(t *testing.T) {
	const want = "C:/Program Files/repo"
	m := newFlowAtRow(t, 2)
	m.flowUI.cwd = ""
	for _, r := range want {
		key := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == ' ' {
			key = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		}
		m = flowUpdate(t, m, key)
	}
	if m.flowUI.cwd != want {
		t.Fatalf("typed path lost characters: %q", m.flowUI.cwd)
	}
	m.flowUI.row = 0
	m = flowUpdate(t, m, editorPasteMsg("Ship it"))
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var req ipc.StartFlowReqPayload
	if err := m.client.(*fakeConn).lastSent().DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
	if req.CWD != want {
		t.Fatalf("submitted repository lost its spaces: %q", req.CWD)
	}
}

// Router.Send routes an UNSTAMPED message to whatever destination is active at
// SEND time, so a scan queued for one host and executed after the active
// project changed asked the other daemon about a path only the first has — and
// its answer, keyed on (cwd, gen) alone, was accepted as the first host's own.
func TestFlowUpdate_RepositoryScan_StaysOnTheDialogsDestination(t *testing.T) {
	local, gpu := newFakeConn(), newFakeConn()
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.client = NewRouter(map[string]Client{"": local, "gpu01": gpu})
	m.projects[1].RootDir = "/remote/root"
	m.activeProject = 1 // the remote project
	if m.activeDest() != "gpu01" {
		t.Fatal("fixture is not on the remote destination", m.activeDest())
	}
	updated, cmd := m.openNewFlow()
	m = updated.(Model)
	if m.flowUI.dest != "gpu01" {
		t.Fatal("dialog did not pin the remote destination", m.flowUI.dest)
	}
	// The user switches back to the local project before the scan runs.
	m.activeProject = 0
	if cmd == nil {
		t.Fatal("no scan command")
	}
	runCmd(cmd)
	if local.sentCount() != 0 {
		t.Fatal("remote repository scan was sent to the local daemon")
	}
	sent := gpu.lastSent()
	if sent == nil || sent.Type != ipc.MsgGitReposReq {
		t.Fatal("scan did not reach the pinned destination", sent)
	}
	if sent.Origin != "gpu01" {
		t.Fatalf("scan was not stamped for its destination: %q", sent.Origin)
	}
}

// A model id belongs to ONE agent: carrying it across an agent change produces
// a configuration the charset check accepts and the new agent rejects.
func TestFlowUpdate_AgentChange_ClearsThePreviousAgentsModel(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	m.flowUI.plugins = []ipc.PluginCatalogEntry{
		{Name: "claude-code", Category: "ai", Available: true},
		{Name: "codex", Category: "ai", Available: true},
	}
	r := m.flowUI.cfg.Roles[flow.Analyst]
	r.Model = "claude-sonnet-5"
	m.flowUI.cfg.Roles[flow.Analyst] = r
	for i, row := range m.flowSettingRows() {
		if row.role == flow.Analyst && row.kind == "agent" {
			m.flowUI.row = i
		}
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	got := m.flowUI.cfg.Roles[flow.Analyst]
	if got.Agent != "codex" || got.Model != "" {
		t.Fatalf("previous agent's model survived the switch: %+v", got)
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var req ipc.SaveFlowConfigReqPayload
	if err := m.client.(*fakeConn).lastSent().DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
	if req.Config.Roles[flow.Analyst].Model != "" {
		t.Fatal("stale model was submitted for the new agent", req.Config.Roles[flow.Analyst])
	}
}

// The model row claims text keys only: the dialog's own Ctrl+S and Esc are
// answered above it and keep working without leaving the row.
func TestFlowUpdate_SettingsModelRow_StillSavesAndCloses(t *testing.T) {
	onModelRow := func() Model {
		m := paletteModelWithProjects(t)
		m.initKeymap()
		m.dialog = dialogFlowSettings
		m.flowUI.cfg = config.DefaultFlows()
		for i, row := range m.flowSettingRows() {
			if row.role == flow.Developer && row.kind == "model" {
				m.flowUI.row = i
			}
		}
		return m
	}
	m := flowUpdate(t, onModelRow(), tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if msg := m.client.(*fakeConn).lastSent(); msg == nil || msg.Type != ipc.MsgSaveFlowConfigReq {
		t.Fatal("Ctrl+S did not save from the model row", msg)
	}
	m = flowUpdate(t, onModelRow(), tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dialog != dialogSettings {
		t.Fatal("Esc did not leave the flow settings from the model row", m.dialog)
	}
}
