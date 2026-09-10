package daemon

import (
	"testing"

	"github.com/artyomsv/quil/internal/ipc"
	apty "github.com/artyomsv/quil/internal/pty"
)

func TestMCPCreatePane_StartsAtKnownSize(t *testing.T) {
	for _, tc := range []struct {
		name            string
		attach, sibling bool
		want            terminalSize
	}{
		{"no client", false, false, terminalSize{80, 24}},
		{"attached client", true, false, terminalSize{178, 58}},
		{"sibling wins", true, true, terminalSize{176, 54}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, client := mcpTestDaemon(t)
			tab := d.session.CreateTab("hidden")
			if tc.sibling {
				p, err := d.session.CreatePane(tab.ID, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				p.PluginMu.Lock()
				p.Cols, p.Rows = 176, 54
				p.PluginMu.Unlock()
			}
			if tc.attach {
				sendNoID(t, client, ipc.MsgAttach, ipc.AttachPayload{Cols: 178, Rows: 58})
				roundTrip(t, client, ipc.MsgListTabsReq, ipc.MsgListTabsResp, nil)
			}
			prev := newSessionFn
			sizes := make(chan terminalSize, 4)
			newSessionFn = func(c, r int) apty.Session {
				sizes <- terminalSize{c, r}
				return prev(c, r)
			}
			t.Cleanup(func() { newSessionFn = prev })
			resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
				ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "terminal"}))
			if resp.Error != "" {
				t.Fatal(resp.Error)
			}
			if got := <-sizes; got != tc.want {
				t.Fatalf("PTY started at %+v, want %+v", got, tc.want)
			}
			p := d.session.Pane(resp.PaneID)
			if c, r := paneSize(p); c != tc.want.cols || r != tc.want.rows {
				t.Fatalf("reported size %dx%d", c, r)
			}
			// A new tab has no sibling; it must inherit the attached size too.
			tabResp := decodeInto[ipc.CreateTabRespPayload](t, roundTrip(t, client, ipc.MsgCreateTabReq, ipc.MsgCreateTabResp,
				ipc.CreateTabReqPayload{Name: "another hidden tab"}))
			if tabResp.Error != "" {
				t.Fatal(tabResp.Error)
			}
			wantTab := terminalSize{80, 24}
			if tc.attach {
				wantTab = terminalSize{178, 58}
			}
			if got := <-sizes; got != wantTab {
				t.Fatalf("new tab PTY %+v, want %+v", got, wantTab)
			}
		})
	}
}

func TestMCPRestartPane_RejectsPreviousPTYOutputAndExit(t *testing.T) {
	d, client := mcpTestDaemon(t)
	tab := d.session.CreateTab("t")
	resp := decodeInto[ipc.CreatePaneRespPayload](t, roundTrip(t, client, ipc.MsgCreatePaneReq, ipc.MsgCreatePaneResp,
		ipc.CreatePaneReqPayload{TabID: tab.ID, Type: "terminal"}))
	if resp.Error != "" {
		t.Fatal(resp.Error)
	}
	p := d.session.Pane(resp.PaneID)
	p.PluginMu.Lock()
	oldGeneration := p.ptyGen
	p.Cols, p.Rows = 176, 54
	p.PluginMu.Unlock()
	d.flushPaneOutputGeneration(p.ID, []byte("OLD SCREEN"), oldGeneration)
	delegated := d.delegateTask(ipc.DelegateTaskReqPayload{ToPane: p.ID, Prompt: "old task"})
	if delegated.Error != "" {
		t.Fatal(delegated.Error)
	}
	restarted := decodeInto[ipc.RestartPaneRespPayload](t, roundTrip(t, client, ipc.MsgRestartPaneReq, ipc.MsgRestartPaneResp,
		ipc.RestartPaneReqPayload{PaneID: p.ID}))
	if !restarted.Success {
		t.Fatal("restart failed")
	}
	taskResp := decodeInto[ipc.GetTaskRespPayload](t, roundTrip(t, client, ipc.MsgGetTaskReq, ipc.MsgGetTaskResp,
		ipc.GetTaskReqPayload{TaskID: delegated.Task.ID}))
	if taskResp.Task.State != "failed" {
		t.Fatalf("restart left delegated work pending: %+v", taskResp)
	}
	p.PluginMu.Lock()
	newGeneration := p.ptyGen
	p.PluginMu.Unlock()
	if newGeneration <= oldGeneration {
		t.Fatal("restart did not advance the PTY generation")
	}
	d.flushPaneOutputGeneration(p.ID, []byte("FRESH SCREEN"), newGeneration)
	d.flushPaneOutputGeneration(p.ID, []byte("LATE OLD OUTPUT"), oldGeneration)
	d.onPaneExitGeneration(p, 1, oldGeneration)
	if got := string(p.OutputBuf.Bytes()); got != "FRESH SCREEN" {
		t.Fatalf("replacement buffer = %q", got)
	}
	p.PluginMu.Lock()
	exited := p.ExitCode != nil
	p.PluginMu.Unlock()
	if exited {
		t.Fatal("old PTY exit marked the replacement exited")
	}
	if c, r := paneSize(p); c != 176 || r != 54 {
		t.Fatalf("restart size=%dx%d", c, r)
	}
	for {
		msg, err := client.Receive()
		if err != nil {
			t.Fatal(err)
		}
		if msg.Type != ipc.MsgPaneOutput {
			continue
		}
		out := decodeInto[ipc.PaneOutputPayload](t, msg)
		if string(out.Data) != "FRESH SCREEN" {
			continue
		}
		if out.Generation != newGeneration {
			t.Fatalf("wire generation=%d, want %d", out.Generation, newGeneration)
		}
		break
	}
}
