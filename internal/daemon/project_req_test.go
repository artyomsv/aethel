package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
)

func TestListProjectsReq_ReportsBootstrapProjectWithItsTabs(t *testing.T) {
	d, client := mcpTestDaemon(t)
	empty := decodeInto[ipc.ListTabsRespPayload](t, roundTrip(t, client, ipc.MsgListTabsReq, ipc.MsgListTabsResp, nil))
	if len(empty.Tabs) != 0 {
		t.Fatalf("MCP connection created tabs: %+v", empty.Tabs)
	}
	tab := d.session.CreateTab("t")

	resp := decodeInto[ipc.ListProjectsRespPayload](t, roundTrip(t, client, ipc.MsgListProjectsReq, ipc.MsgListProjectsResp, nil))
	if len(resp.Projects) != 1 {
		t.Fatalf("projects = %+v, want the one bootstrap project", resp.Projects)
	}
	p := resp.Projects[0]
	if !p.Active || !p.Bootstrap || resp.ActiveProject != p.ID {
		t.Fatalf("project = %+v active=%q", p, resp.ActiveProject)
	}
	if len(p.TabIDs) != 1 || p.TabIDs[0] != tab.ID {
		t.Fatalf("TabIDs = %v, want [%s]", p.TabIDs, tab.ID)
	}
}

func TestCreateProjectReq_AnswersWithTheNewID(t *testing.T) {
	d, client := mcpTestDaemon(t)
	d.session.CreateTab("t")

	resp := decodeInto[ipc.CreateProjectRespPayload](t, roundTrip(t, client, ipc.MsgCreateProjectReq, ipc.MsgCreateProjectResp,
		ipc.CreateProjectReqPayload{Name: "api", RootDir: t.TempDir()}))
	if resp.Error != "" || resp.ProjectID == "" || resp.Name != "api" {
		t.Fatalf("resp = %+v", resp)
	}
	if !d.projectExists(resp.ProjectID) {
		t.Fatal("created project not found in the session")
	}
	// A fresh project ships with a shell tab, like one made from the TUI.
	tabs := 0
	for _, p := range d.session.Projects() {
		if p.ID == resp.ProjectID {
			tabs = len(p.TabIDs)
		}
	}
	if tabs != 1 {
		t.Fatalf("new project has %d tabs, want 1", tabs)
	}

	empty := decodeInto[ipc.CreateProjectRespPayload](t, roundTrip(t, client, ipc.MsgCreateProjectReq, ipc.MsgCreateProjectResp,
		ipc.CreateProjectReqPayload{Name: ""}))
	if empty.Error == "" || empty.ProjectID != "" {
		t.Fatalf("empty name: %+v, want a refusal", empty)
	}
}

// The six project mutations stay fire-and-forget for the TUI and answer only
// an ID-bearing request.
func TestProjectMutations_AnswerOnlyIDBearingRequests(t *testing.T) {
	d, client := mcpTestDaemon(t)
	d.session.CreateTab("t")
	proj := d.session.CreateProject("web", t.TempDir())

	ok := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgUpdateProject, ipc.MsgProjectOpResp,
		ipc.UpdateProjectPayload{ProjectID: proj.ID, Name: "web2", RootDir: proj.RootDir}))
	if !ok.OK || ok.ID != proj.ID {
		t.Fatalf("rename: %+v", ok)
	}
	bad := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgUpdateProject, ipc.MsgProjectOpResp,
		ipc.UpdateProjectPayload{ProjectID: "proj-nope", Name: "x"}))
	if bad.OK || bad.Error == "" {
		t.Fatalf("unknown id: %+v, want a refusal", bad)
	}
	sw := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgSwitchProject, ipc.MsgProjectOpResp,
		ipc.SwitchProjectPayload{ProjectID: proj.ID}))
	if !sw.OK || d.session.ActiveProject() != proj.ID {
		t.Fatalf("switch: %+v active=%s", sw, d.session.ActiveProject())
	}
	gone := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgDestroyProject, ipc.MsgProjectOpResp,
		ipc.DestroyProjectPayload{ProjectID: "proj-nope"}))
	if gone.OK {
		t.Fatalf("destroying an unknown project answered OK: %+v", gone)
	}
	del := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgDestroyProject, ipc.MsgProjectOpResp,
		ipc.DestroyProjectPayload{ProjectID: proj.ID}))
	if !del.OK || d.projectExists(proj.ID) {
		t.Fatalf("destroy: %+v exists=%v", del, d.projectExists(proj.ID))
	}

	// ID-less: no answer. Proven by ordering — the list request behind it
	// is answered, so the rename was handled and produced nothing.
	sendNoID(t, client, ipc.MsgUpdateProject, ipc.UpdateProjectPayload{ProjectID: "proj-nope", Name: "x"})
	listMsg := roundTrip(t, client, ipc.MsgListProjectsReq, ipc.MsgListProjectsResp, nil)
	if listMsg.Type != ipc.MsgListProjectsResp {
		t.Fatalf("got %s", listMsg.Type)
	}
}

func TestTabAndPaneOps_AnswerIDBearingRequests(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")
	pane, err := d.session.CreatePane(tab.ID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ren := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgUpdateTab, ipc.MsgTabOpResp,
		ipc.UpdateTabPayload{TabID: tab.ID, Name: "build"}))
	if !ren.OK || d.session.Tab(tab.ID).Name != "build" {
		t.Fatalf("rename tab: %+v name=%q", ren, d.session.Tab(tab.ID).Name)
	}
	badTab := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgUpdateTab, ipc.MsgTabOpResp,
		ipc.UpdateTabPayload{TabID: "tab-nope", Name: "x"}))
	if badTab.OK {
		t.Fatalf("unknown tab answered OK: %+v", badTab)
	}
	pr := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgUpdatePane, ipc.MsgPaneOpResp,
		ipc.UpdatePanePayload{PaneID: pane.ID, Name: "worker"}))
	if !pr.OK || pane.Name != "worker" {
		t.Fatalf("rename pane: %+v name=%q", pr, pane.Name)
	}
	del := decodeInto[ipc.OpRespPayload](t, roundTrip(t, client, ipc.MsgDestroyTab, ipc.MsgTabOpResp,
		ipc.DestroyTabPayload{TabID: tab.ID}))
	if !del.OK || d.session.Tab(tab.ID) != nil {
		t.Fatalf("destroy tab: %+v", del)
	}
}

func TestListTabsReq_FiltersByProjectAndReportsProjectID(t *testing.T) {
	d, client := mcpTestDaemon(t)
	d.session.CreateTab("a")
	proj := d.session.CreateProject("web", t.TempDir())
	webTab := d.session.CreateTabInProject(proj.ID, "b")

	all := decodeInto[ipc.ListTabsRespPayload](t, roundTrip(t, client, ipc.MsgListTabsReq, ipc.MsgListTabsResp, nil))
	if len(all.Tabs) != 2 {
		t.Fatalf("all tabs = %+v", all.Tabs)
	}
	for _, tb := range all.Tabs {
		if tb.ProjectID == "" {
			t.Fatalf("tab %s carries no project id", tb.ID)
		}
	}
	only := decodeInto[ipc.ListTabsRespPayload](t, roundTrip(t, client, ipc.MsgListTabsReq, ipc.MsgListTabsResp,
		ipc.ListTabsReqPayload{ProjectID: proj.ID}))
	if len(only.Tabs) != 1 || only.Tabs[0].ID != webTab.ID {
		t.Fatalf("filtered tabs = %+v, want [%s]", only.Tabs, webTab.ID)
	}
}
