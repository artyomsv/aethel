package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
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
	rows, _ := m.sidebarRows(100)
	var sidebar string
	for _, row := range rows {
		sidebar += row.text
	}
	if !strings.Contains(sidebar, "review · round 2 ⏸") {
		t.Fatal("flow state missing from sidebar", sidebar)
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

func TestFlowUpdate_PaletteActions_ResumesOrConfirmsCancel(t *testing.T) {
	for _, action := range []paletteAction{palActResumeFlow, palActCancelFlow} {
		m := paletteModelWithProjects(t)
		m.initKeymap()
		m.activeTabModel().Flow = &flow.Flow{ID: "flow-test", Stage: flow.StagePlan, Paused: true}
		m.dialog = dialogCommandPalette
		for _, command := range m.buildPaletteCommands() {
			if command.action == action {
				m.palette = paletteState{filtered: []paletteCommand{command}}
			}
		}
		m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if action == palActResumeFlow {
			msg := m.client.(*fakeConn).lastSent()
			if msg == nil || msg.Type != ipc.MsgResumeFlowReq {
				t.Fatal("resume not sent", msg)
			}
			var req ipc.ResumeFlowReqPayload
			if err := msg.DecodePayload(&req); err != nil || req.FlowID != "flow-test" {
				t.Fatal(req, err)
			}
		} else if m.dialog != dialogConfirm || m.confirmKind != "tab" || m.confirmID != "tab-1" {
			t.Fatal("cancel did not use the close-tab confirmation", m.dialog)
		}
	}
}

func TestFlowUpdate_StopHook_DoesNotToastInternalHandoff(t *testing.T) {
	m, notifier, pane := wiredToastModel(t)
	m.activeTabModel().Flow = &flow.Flow{ID: "f", Panes: map[flow.Role]string{flow.Analyst: pane.ID}}
	m = flowUpdate(t, m, paneEventMsg{PaneID: pane.ID, Type: "hook.claude.UserPromptSubmit"})
	m = flowUpdate(t, m, paneEventMsg{PaneID: pane.ID, Type: "hook.claude.Stop"})
	if len(notifier.sent) != 0 || pane.unseen {
		t.Fatal("internal handoff raised user attention")
	}
	m = flowUpdate(t, m, paneEventMsg{PaneID: pane.ID, Type: "flow_ready"})
	if len(notifier.sent) != 1 {
		t.Fatal("flow outcome did not toast")
	}
}

func TestFlowUpdate_AgentChange_SeedsPluginDefaults(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	m.flowUI.row = 2 // analyst agent
	m.flowUI.plugins = []ipc.PluginCatalogEntry{
		{Name: "claude-code", Category: "ai", Available: true},
		{Name: "codex", Category: "ai", Available: true, Toggles: []ipc.PluginToggleInfo{{Name: "search", Default: true}, {Name: "permission", Default: false}}},
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	role := m.flowUI.cfg.Roles[flow.Analyst]
	if role.Agent != "codex" || len(role.Toggles) != 1 || role.Toggles[0] != "search" {
		t.Fatal(role)
	}
}

func TestFlowUpdate_AgentChange_ShippedPluginsFocusPermissionChoice(t *testing.T) {
	dir := t.TempDir()
	if _, err := plugin.EnsureDefaultPlugins(dir); err != nil {
		t.Fatal(err)
	}
	registry := plugin.NewRegistry()
	if err := registry.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	m.flowUI.row = 2
	for _, name := range []string{"claude-code", "codex"} {
		p := registry.Get(name)
		entry := ipc.PluginCatalogEntry{Name: name, Category: "ai", Available: true}
		for _, toggle := range p.Command.Toggles {
			entry.Toggles = append(entry.Toggles, ipc.PluginToggleInfo{Name: toggle.Name, Group: toggle.Group, Default: toggle.Default})
		}
		m.flowUI.plugins = append(m.flowUI.plugins, entry)
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	row := m.flowSettingRows()[m.flowUI.row]
	if row.role != flow.Analyst || row.kind != "toggle" || flowMissingPermissionToggle(m.flowUI.cfg.Roles[flow.Analyst], m.flowUI.plugins) != row.toggle || !strings.Contains(m.renderFlowDialog(), "Select a permission mode for analyst") {
		t.Fatal("missing visible permission choice", row)
	}
	for i, candidate := range m.flowSettingRows() {
		if candidate.role == flow.Analyst && candidate.toggle == "auto_workspace_write" {
			m.flowUI.row = i
		}
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if strings.Contains(m.renderFlowDialog(), "Select a permission mode") {
		t.Fatal("hint survived selection")
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var req ipc.SaveFlowConfigReqPayload
	if err := m.client.(*fakeConn).lastSent().DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
	if got := req.Config.Roles[flow.Analyst].Toggles; len(got) != 1 || got[0] != "auto_workspace_write" {
		t.Fatal(got)
	}
}

func TestFlowUpdate_DestroyedPane_ToastsThroughSurvivingTab(t *testing.T) {
	m, notifier, survivor := wiredToastModel(t)
	tab := m.activeTabModel()
	m = flowUpdate(t, m, paneEventMsg{PaneID: "destroyed", TabID: tab.ID, Type: "flow_paused", Title: "pane analyst was closed"})
	if len(notifier.sent) != 1 || survivor.blockedReason != "pane analyst was closed" {
		t.Fatal("destroyed pane lost attention", survivor.blockedReason, len(notifier.sent))
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
	if err := msg.DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
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
	reply, _ := ipc.NewMessage(ipc.MsgFlowConfigResp, ipc.FlowConfigRespPayload{Config: config.DefaultFlows().Wire()})
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
	if err := save.DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
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
	if err := req.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
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

func flowLayoutState(panes []string, f flow.Flow) WorkspaceStateMsg {
	state := WorkspaceStateMsg{ActiveTab: "tab-9", Tabs: []TabInfo{{ID: "tab-9", Name: "feat", Panes: panes}}, Projects: []ProjectInfo{{ID: "proj-local", Name: "quil", TabIDs: []string{"tab-9"}}}, ActiveProject: "proj-local", Flows: []flow.Flow{f}}
	for _, id := range panes {
		state.Panes = append(state.Panes, PaneInfo{ID: id, TabID: "tab-9"})
	}
	return state
}

func assertFlowLayout(t *testing.T, tab *TabModel) {
	t.Helper()
	root := tab.Root
	if root == nil || root.IsLeaf() || root.Split != SplitHorizontal || root.Left == nil || !root.Left.IsLeaf() || root.Left.Pane.ID != "an" {
		t.Fatalf("root is not analyst | rest: %+v", root)
	}
	right := root.Right
	if right == nil || right.IsLeaf() || right.Split != SplitVertical || right.Left.Pane == nil || right.Left.Pane.ID != "dev" || right.Right.Pane == nil || right.Right.Pane.ID != "rev" {
		t.Fatalf("right column is not developer / reviewer: %+v", right)
	}
}

func TestFlowUpdate_RolePanes_LayoutAnalystLeftOthersStacked(t *testing.T) {
	// The real sequence: the tab exists with the analyst placeholder while
	// the worktree is prepared, then developer and reviewer land together.
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.notifications = NewNotificationCenter(30, 200)
	f := flow.Flow{ID: "f", TabID: "tab-9", Stage: flow.StagePreparing, Panes: map[flow.Role]string{flow.Analyst: "an"}}
	m = flowUpdate(t, m, flowLayoutState([]string{"an"}, f))
	f.Stage, f.Panes = flow.StagePlan, map[flow.Role]string{flow.Analyst: "an", flow.Developer: "dev", flow.Reviewer: "rev"}
	m = flowUpdate(t, m, flowLayoutState([]string{"an", "dev", "rev"}, f))
	assertFlowLayout(t, m.tabByID("tab-9"))

	// A client attaching after the fact sees all three at once.
	m = paletteModelWithProjects(t)
	m.initKeymap()
	m = flowUpdate(t, m, flowLayoutState([]string{"an", "dev", "rev"}, f))
	assertFlowLayout(t, m.tabByID("tab-9"))

	// Panes that belong to no flow keep stacking below the first leaf.
	m = paletteModelWithProjects(t)
	m.initKeymap()
	m = flowUpdate(t, m, flowLayoutState([]string{"an", "dev", "rev"}, flow.Flow{ID: "elsewhere", TabID: "tab-other"}))
	if root := m.tabByID("tab-9").Root; root.IsLeaf() || root.Split != SplitVertical {
		t.Fatalf("plain tab changed shape: %+v", root)
	}
}

func TestFlowUpdate_NewDialog_RepositoryRowPicksAndSubmitsCWD(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.projects[0].RootDir = "/repo/root"
	m.dialog = dialogCommandPalette
	m.palette = paletteState{filtered: []paletteCommand{{action: palActNewFlow, label: "New flow", enabled: true}}}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog != dialogNewFlow || m.flowUI.cwd != "/repo/root" || m.repoScan.cwd != "/repo/root" || m.repoScan.purpose != repoScanFlow {
		t.Fatalf("dialog did not open on the project root with a scan: %+v %+v", m.flowUI, m.repoScan)
	}
	m = flowUpdate(t, m, gitReposMsg{Resp: ipc.GitReposRespPayload{CWD: "/repo/root", Repos: []string{"/repo/root", "/repo/root/sub"}}, Gen: m.repoScan.gen})
	if len(m.flowUI.repos) != 2 || m.flowUI.cwd != "/repo/root" {
		t.Fatal("scan result replaced the typed path or was dropped", m.flowUI.repos, m.flowUI.cwd)
	}
	m = flowUpdate(t, m, editorPasteMsg("Ship it"))
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.flowUI.row != 2 {
		t.Fatal("repository row not reachable by Tab", m.flowUI.row)
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.flowUI.cwd != "/repo/root/sub" || !strings.Contains(m.renderFlowDialog(), "Repository: /repo/root/sub") {
		t.Fatal("right did not pick the next repository", m.flowUI.cwd)
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	msg := m.client.(*fakeConn).lastSent()
	var req ipc.StartFlowReqPayload
	if msg == nil || msg.Type != ipc.MsgStartFlowReq || msg.DecodePayload(&req) != nil {
		t.Fatal("start not sent", msg)
	}
	if req.CWD != "/repo/root/sux" || req.Feature != "Ship it" {
		t.Fatal(req)
	}
	// An emptied repository row means the project root: sent empty, so the
	// daemon resolves it rather than the client guessing a path.
	m.flowUI.pending, m.flowUI.cwd = false, "   "
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var second ipc.StartFlowReqPayload // fresh: cwd is omitempty, so a reused struct keeps the old value
	if err := m.client.(*fakeConn).lastSent().DecodePayload(&second); err != nil || second.CWD != "" {
		t.Fatal("blank repository row was not sent as the project root", second.CWD, err)
	}
}

func TestFlowUpdate_SettingsModelRow_TypesAndSaves(t *testing.T) {
	m := paletteModelWithProjects(t)
	m.initKeymap()
	m.dialog = dialogFlowSettings
	m.flowUI.cfg = config.DefaultFlows()
	for i, row := range m.flowSettingRows() {
		if row.role == flow.Developer && row.kind == "model" {
			m.flowUI.row = i
		}
	}
	if !strings.Contains(m.renderFlowDialog(), "developer model: (agent default") {
		t.Fatal(m.renderFlowDialog())
	}
	for _, r := range "gpt-jk5" { // j and k are typed here, not navigation
		m = flowUpdate(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.flowUI.cfg.Roles[flow.Developer].Model; got != "gpt-jk" {
		t.Fatal(got)
	}
	row := m.flowUI.row
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.flowUI.row != row-1 {
		t.Fatal("arrow keys stopped navigating on the model row")
	}
	m = flowUpdate(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var req ipc.SaveFlowConfigReqPayload
	if err := m.client.(*fakeConn).lastSent().DecodePayload(&req); err != nil {
		t.Fatal(err)
	}
	if req.Config.Roles[flow.Developer].Model != "gpt-jk" {
		t.Fatal(req.Config.Roles[flow.Developer])
	}
}
