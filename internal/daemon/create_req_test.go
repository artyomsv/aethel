package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/artyomsv/quil/internal/gitworktree"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/artyomsv/quil/internal/plugin"
)

func TestResolveToggles(t *testing.T) {
	p := &plugin.PanePlugin{Name: "claude-code"}
	p.Command.Toggles = []plugin.Toggle{
		{Name: "dangerously_skip_permissions", ArgsWhenOn: []string{"--dangerously-skip-permissions"}, Group: "permission_mode"},
		{Name: "enable_auto_mode", ArgsWhenOn: []string{"--enable-auto-mode"}, Group: "permission_mode"},
		{Name: "chrome", ArgsWhenOn: []string{"--chrome"}},
	}
	args, err := resolveToggles(p, []string{"chrome", "dangerously_skip_permissions"})
	if err != nil || strings.Join(args, " ") != "--chrome --dangerously-skip-permissions" {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if _, err := resolveToggles(p, []string{"skip_permissions"}); err == nil || !strings.Contains(err.Error(), "unknown toggle") {
		t.Fatalf("misspelled toggle accepted: %v", err)
	}
	if _, err := resolveToggles(p, []string{"dangerously_skip_permissions", "enable_auto_mode"}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("two permission modes accepted: %v", err)
	}
	if args, err := resolveToggles(nil, nil); err != nil || args != nil {
		t.Fatalf("no toggles on no plugin: %v %v", args, err)
	}
	if _, err := resolveToggles(nil, []string{"x"}); err == nil {
		t.Fatal("toggles for a plugin-less pane accepted")
	}
}

func TestCreatePaneReq_TogglesBecomeArgsAndNameLands(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")

	resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", Name: "reviewer", Toggles: []string{"dangerously_skip_permissions"}}))
	if resp.Error != "" && !strings.Contains(resp.Error, "not found") {
		// The test image has no claude binary; a spawn failure is reported,
		// not hidden, and the pane still exists with the right fields.
		t.Fatalf("unexpected error: %q", resp.Error)
	}
	if resp.PaneID == "" || resp.TabID != tab.ID {
		t.Fatalf("resp = %+v", resp)
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		t.Fatal("pane not created")
	}
	pane.PluginMu.Lock()
	typ, args, name := pane.Type, pane.InstanceArgs, pane.Name
	pane.PluginMu.Unlock()
	if typ != "claude-code" || strings.Join(args, " ") != "--dangerously-skip-permissions" || name != "reviewer" {
		t.Fatalf("type=%q args=%v name=%q", typ, args, name)
	}
}

func TestCreatePaneReq_RefusalsCreateNoPane(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")
	before := len(d.buildPaneInfos())
	// The test image has no git; the seam answers "not a repository" the way
	// a real git would for a plain temp directory.
	prevList := worktreeListFn
	worktreeListFn = func(ctx context.Context, dir string) ([]gitworktree.Worktree, error) { return nil, nil }
	t.Cleanup(func() { worktreeListFn = prevList })

	cases := []struct {
		name string
		req  ipc.CreatePaneReqPayload
		want string
	}{
		{"unknown toggle", ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", Toggles: []string{"nope"}}, "unknown toggle"},
		{"group clash", ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", Toggles: []string{"dangerously_skip_permissions", "enable_auto_mode"}}, "mutually exclusive"},
		{"unknown plugin", ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "vim-but-not-a-plugin"}, "unknown plugin"},
		{"unknown tab", ipc.CreatePaneReqPayload{TabID: "tab-nope"}, "no such tab"},
		{"worktree outside a repo", ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", CWD: t.TempDir(), WorktreeBranch: "feat/x"}, "not inside a git repository"},
		{"bad branch", ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "claude-code", CWD: t.TempDir(), WorktreeBranch: "bad name"}, ""},
	}
	for _, tc := range cases {
		resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp, tc.req))
		if resp.Error == "" || resp.PaneID != "" {
			t.Fatalf("%s: resp = %+v, want an error and no pane", tc.name, resp)
		}
		if tc.want != "" && !strings.Contains(resp.Error, tc.want) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, resp.Error, tc.want)
		}
	}
	if got := len(d.buildPaneInfos()); got != before {
		t.Fatalf("pane count %d → %d: a refused create left a pane behind", before, got)
	}
}

func TestCreateTabReq_FilesTheTabUnderTheNamedProject(t *testing.T) {
	d, client := mcpTestDaemon(t)
	first := d.session.CreateTab("t")
	proj := d.session.CreateProject("web", t.TempDir())
	active := d.session.ActiveTabID()

	resp := decodeInto[ipc.CreateTabRespPayload](t, roundTrip(t, client, ipc.MsgCreateTabReq, ipc.MsgCreateTabResp,
		ipc.CreateTabReqPayload{Name: "worker", ProjectID: proj.ID, FirstPane: &ipc.CreatePaneReqPayload{Name: "w1"}}))
	if resp.Error != "" || resp.TabID == "" || resp.PaneID == "" {
		t.Fatalf("resp = %+v", resp)
	}
	tab := d.session.Tab(resp.TabID)
	if tab == nil || tab.ProjectID != proj.ID || tab.Name != "worker" {
		t.Fatalf("tab = %+v, want project %s", tab, proj.ID)
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil || pane.TabID != resp.TabID || pane.Name != "w1" {
		t.Fatalf("pane = %+v", pane)
	}
	// The user's focus is left alone.
	if d.session.ActiveTabID() != active || active != first.ID {
		t.Fatalf("active tab moved to %s", d.session.ActiveTabID())
	}
	bad := decodeInto[ipc.CreateTabRespPayload](t, roundTrip(t, client, ipc.MsgCreateTabReq, ipc.MsgCreateTabResp,
		ipc.CreateTabReqPayload{ProjectID: "proj-nope"}))
	if bad.Error == "" || bad.TabID != "" {
		t.Fatalf("unknown project: %+v", bad)
	}
}

// The TUI's own create_tab honours a project_id too, and an empty one keeps
// filing under the active project.
func TestHandleCreateTab_HonoursProjectID(t *testing.T) {
	d := newTestDaemon(t)
	d.session.CreateTab("t")
	proj := d.session.CreateProject("web", t.TempDir())
	msg, _ := ipc.NewMessage(ipc.MsgCreateTab, ipc.CreateTabPayload{Name: "x", ProjectID: proj.ID})
	d.handleMessage(nil, msg)
	var found *Tab
	for _, tb := range d.session.Tabs() {
		if tb.Name == "x" {
			found = tb
		}
	}
	if found == nil || found.ProjectID != proj.ID {
		t.Fatalf("tab = %+v, want project %s", found, proj.ID)
	}
}

func TestPluginCatalogReq_ListsTogglesByName(t *testing.T) {
	_, client := mcpTestDaemon(t)
	resp := decodeInto[ipc.PluginCatalogRespPayload](t, roundTrip(t, client, ipc.MsgPluginCatalogReq, ipc.MsgPluginCatalogResp, nil))
	var claude *ipc.PluginCatalogEntry
	for i := range resp.Plugins {
		if resp.Plugins[i].Name == "claude-code" {
			claude = &resp.Plugins[i]
		}
	}
	if claude == nil {
		t.Fatalf("claude-code missing from %+v", resp.Plugins)
	}
	if !claude.PromptsCWD || !claude.Sessions || claude.Category != "ai" {
		t.Fatalf("claude entry = %+v", *claude)
	}
	names := map[string]string{}
	for _, tg := range claude.Toggles {
		names[tg.Name] = tg.Group
	}
	if names["dangerously_skip_permissions"] != "permission_mode" || names["enable_auto_mode"] != "permission_mode" {
		t.Fatalf("toggles = %+v", claude.Toggles)
	}
	if _, ok := names["chrome"]; !ok {
		t.Fatalf("chrome toggle missing: %+v", claude.Toggles)
	}
}
