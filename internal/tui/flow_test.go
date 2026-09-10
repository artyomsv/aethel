package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/ipc"
)

func flowUpdate(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func TestFlowUpdate_SidebarPaletteAndRestartPause(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.notifications = NewNotificationCenter(30, 200)
	f := flow.Flow{ID: "f", TabID: "tab-1", Stage: flow.StageReview, Round: 1, Paused: true, PauseWhy: "Which API?", UpdatedAt: time.Now().UnixMilli(), Panes: map[flow.Role]string{flow.Reviewer: "pane-1"}}
	state := WorkspaceStateMsg{ActiveTab: "tab-1", Tabs: []TabInfo{{ID: "tab-1", Name: "Feature", Panes: []string{"pane-1"}}}, Panes: []PaneInfo{{ID: "pane-1", TabID: "tab-1"}}, Projects: []ProjectInfo{{ID: "proj-local", Name: "quil", TabIDs: []string{"tab-1"}}}, ActiveProject: "proj-local", Flows: []flow.Flow{f}}
	m = flowUpdate(t, m, state)
	if got := flowTabLabel(m.activeTabModel()); !strings.Contains(got, "review · round 2 ⏸") {
		t.Fatal(got)
	}
	if p, _, _ := m.findPaneAndTab("pane-1"); p.blockedReason != "Which API?" || p.blockedSince.IsZero() {
		t.Fatal("pause did not reach sidebar attention")
	}
	for _, c := range m.buildPaletteCommands() {
		if c.action == palActResumeFlow && !c.enabled {
			t.Fatal("resume disabled")
		}
	}
	state.Flows[0].Paused = false
	state.Flows[0].Stage = flow.StageReadyForYou
	state.Flows[0].Results.PR = "42\x1b[31m"
	m = flowUpdate(t, m, state)
	if got := flowTabLabel(m.activeTabModel()); !strings.Contains(got, "✓ PR 42") || strings.Contains(got, "\x1b") {
		t.Fatal(got)
	}
	for _, c := range m.buildPaletteCommands() {
		if c.action == palActResumeFlow && c.enabled {
			t.Fatal("ready flow resumable")
		}
	}
}

func TestFlowUpdate_NewDialogSubmitsAndFocusesOnlyResponseTab(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.notifications = NewNotificationCenter(30, 200)
	// Enter through the palette's real Update dispatch.
	m.dialog = dialogCommandPalette
	m.palette = paletteState{filtered: []paletteCommand{{action: palActNewFlow, label: "New flow", enabled: true}}}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogNewFlow {
		t.Fatal("new flow did not open")
	}
	m = flowUpdate(t, m, editorPasteMsg("Add useful tests"))
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	conn := m.client.(*fakeConn)
	msg := conn.lastSent()
	if msg.Type != ipc.MsgStartFlowReq {
		t.Fatalf("sent %s", msg.Type)
	}
	var req ipc.StartFlowReqPayload
	_ = msg.DecodePayload(&req)
	if req.Feature != "Add useful tests" || req.Branch != "feat/add-useful-tests" || req.ProjectID != "proj-local" {
		t.Fatal(req)
	}
	if m.activeTabModel().ID != "tab-1" {
		t.Fatal("focus changed before response")
	}
	newTab := tabWithPane("flow-tab", "flow-pane")
	m.projects[0].tabs = append(m.projects[0].tabs, newTab)
	reply, _ := ipc.NewMessage(ipc.MsgStartFlowResp, ipc.StartFlowRespPayload{FlowID: "f", TabID: "flow-tab"})
	reply.ID = msg.ID
	m = flowUpdate(t, m, flowReplyMsg{reply})
	if m.activeTabModel().ID != "flow-tab" || m.dialog != dialogNone {
		t.Fatal("response did not focus the requested tab")
	}
}

func TestFlowUpdate_SettingsSaveAndReload(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogSettings
	for i, f := range settingsFields() {
		if f.flowSettings {
			m.dialogCursor = i
		}
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogFlowSettings {
		t.Fatal("F1 settings did not open flows")
	}
	reply, _ := ipc.NewMessage(ipc.MsgFlowConfigResp, ipc.FlowConfigRespPayload{Config: config.DefaultFlows()})
	reply.ID = m.flowUI.requestID
	m = flowUpdate(t, m, flowReplyMsg{reply})
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	conn := m.client.(*fakeConn)
	save := conn.lastSent()
	var req ipc.SaveFlowConfigReqPayload
	if save.Type != ipc.MsgSaveFlowConfigReq {
		t.Fatal(save.Type)
	}
	_ = save.DecodePayload(&req)
	if req.Config.MaxReviewRounds != 4 {
		t.Fatal(req.Config)
	}
	reply, _ = ipc.NewMessage(ipc.MsgSaveFlowConfigResp, ipc.FlowConfigRespPayload{Config: req.Config})
	reply.ID = save.ID
	m = flowUpdate(t, m, flowReplyMsg{reply})
	if m.dialog != dialogSettings || conn.lastSent().Type != ipc.MsgReloadPlugins {
		t.Fatal("save did not return to settings and reload")
	}
}

func TestFlowUpdate_PausedAndReadyToast(t *testing.T) {
	for _, typ := range []string{"flow_paused", "flow_ready"} {
		t.Run(typ, func(t *testing.T) {
			m, n, p := wiredToastModel(t)
			m = flowUpdate(t, m, paneEventMsg{PaneID: p.ID, Type: typ})
			if len(n.sent) != 1 {
				t.Fatalf("%s sent %d toasts", typ, len(n.sent))
			}
		})
	}
}

func TestFlowUpdate_SettingsExclusiveToggles(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	m.flowUI.plugins = []ipc.PluginCatalogEntry{{Name: "codex", Category: "ai", Available: true, Toggles: []ipc.PluginToggleInfo{
		{Name: "auto_workspace_write", Group: "approval"}, {Name: "other", Group: "approval"},
	}}}
	for i, row := range m.flowSettingRows() {
		if row.role == flow.Developer && row.toggle == "other" {
			m.flowUI.row = i
		}
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	got := m.flowUI.cfg.Roles[flow.Developer].Toggles
	if len(got) != 1 || got[0] != "other" {
		t.Fatalf("conflicting toggles selected: %v", got)
	}
}

func TestFlowUpdate_DestinationPinnedAndForeignReplyIgnored(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.activeProject = 1
	m.dialog = dialogCommandPalette
	m.palette = paletteState{filtered: []paletteCommand{{action: palActNewFlow, label: "New flow", enabled: true}}}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.activeProject = 0 // A concurrent project change must not retarget the dialog.
	m = flowUpdate(t, m, editorPasteMsg("Remote feature"))
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	req := m.client.(*fakeConn).lastSent()
	if req == nil || req.Type != ipc.MsgStartFlowReq || req.Origin != "gpu01" {
		t.Fatalf("request escaped pinned destination: %+v", req)
	}
	var payload ipc.StartFlowReqPayload
	_ = req.DecodePayload(&payload)
	if payload.ProjectID != "proj-remote" {
		t.Fatal(payload)
	}
	reply, _ := ipc.NewMessage(ipc.MsgStartFlowResp, ipc.StartFlowRespPayload{TabID: "wrong-tab"})
	reply.ID = req.ID
	m = flowUpdate(t, m, flowReplyMsg{reply})
	if !m.flowUI.pending || m.dialog != dialogNewFlow {
		t.Fatal("accepted reply from another daemon")
	}
}
