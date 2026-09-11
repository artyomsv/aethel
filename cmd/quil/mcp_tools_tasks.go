package main

import (
	"context"
	"fmt"
	"time"

	"github.com/artyomsv/quil/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Agent tasking: one pane hands work to another and hears when it is done.

func registerTaskTools(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	registerReportStepTool(s, r)
	registerDelegateTaskTool(s, r, mcpLog)
	registerGetTaskTool(s, r)
	registerWaitTaskTool(s, r, mcpLog)
	registerListTasksTool(s, r)
}

func registerReportStepTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		Status string            `json:"status" jsonschema:"done or blocked"`
		Result map[string]string `json:"result" jsonschema:"step results: plan, pr, verdict (approved or changes), notes, or question when blocked; at most 16 values of 8 KiB each"`
		TaskID string            `json:"task_id,omitempty" jsonschema:"optional task id; defaults to your pane's live task"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "report_step", Description: "Report your flow step result before ending your turn. The daemon advances only after your report and settled idle. Use blocked with a question when you need the user. A second report corrects the first until the task ends. You can only report on your own pane's task."},
		func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
			if err := r.local.requireDaemonAtLeast("report_step", reportStepMinVersion); err != nil {
				return nil, nil, err
			}
			if r.selfPane == "" {
				return nil, nil, fmt.Errorf("report_step: no step is waiting on this pane (QUIL_PANE_ID is unset)")
			}
			resp, err := r.local.request(ipc.MsgReportStepReq, ipc.ReportStepReqPayload{PaneID: r.selfPane, TaskID: input.TaskID, Status: input.Status, Result: input.Result})
			if err != nil {
				return nil, nil, fmt.Errorf("report_step: %w", err)
			}
			var payload ipc.ReportStepRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return nil, nil, fmt.Errorf("report_step decode: %w", err)
			}
			if payload.Error != "" {
				return nil, nil, fmt.Errorf("report_step: %s", payload.Error)
			}
			return jsonResult(payload), nil, nil
		})
}

type hostedTask struct {
	ipc.TaskInfo
	Host string `json:"host,omitempty"`
}

func registerFlowGetTaskTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		TaskID string `json:"task_id" jsonschema:"your flow task id returned by report_step"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_task", Description: "Get your own flow task on this daemon."},
		func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
			if err := r.local.requireDaemon("get_task"); err != nil {
				return nil, nil, err
			}
			resp, err := r.local.request(ipc.MsgGetTaskReq, ipc.GetTaskReqPayload{TaskID: input.TaskID})
			if err != nil {
				return nil, nil, err
			}
			var payload ipc.GetTaskRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return nil, nil, err
			}
			if payload.Error != "" {
				return nil, nil, fmt.Errorf("get_task: %s", payload.Error)
			}
			if r.selfPane == "" || payload.Task.ToPane != r.selfPane {
				return nil, nil, fmt.Errorf("get_task: task does not belong to this pane")
			}
			return jsonResult(payload.Task), nil, nil
		})
}

func registerDelegateTaskTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		PaneID  string `json:"pane_id" jsonschema:"pane to give the work to (an AI pane, or a terminal for a shell command)"`
		Prompt  string `json:"prompt" jsonschema:"the prompt or command; multi-line is fine, it is pasted as one block"`
		Notify  *bool  `json:"notify,omitempty" jsonschema:"type a one-line completion notice into YOUR pane when the task ends (default true; ignored when this bridge runs outside a pane)"`
		Timeout int    `json:"timeout,omitempty" jsonschema:"seconds until the task is marked timeout (default 0 = none)"`
		Host    string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the id was discovered on, else local)"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "delegate_task",
		Description: "Give another pane a task and get a task id back. The prompt is delivered as a paste plus Enter. The daemon then follows " +
			"the target's agent state: the task becomes done when the target's turn has settled idle (background subagents included), " +
			"failed if its process exits, timeout if you set one. A terminal target is done when its shell reports the command finished. " +
			"On completion a task_done notification is queued (watch_notifications / get_notifications), wait_task returns, and — " +
			"with notify — a line like '[quil task task-xxxx] pane <id> done. Last output: ...' is typed into your own pane once you are idle, " +
			"so you can carry on with other work and react when it arrives. Requester and target must be on the same host. " +
			"ONE live task per target pane: a pane that already has a task in flight is refused, naming it — completion comes from the " +
			"target's agent state, which cannot say which of two prompts finished. Wait with wait_task, or pick another pane.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host, input.PaneID)
		if err != nil {
			return nil, nil, fmt.Errorf("delegate_task: %w", err)
		}
		if err := bridge.requireDaemon("delegate_task"); err != nil {
			return nil, nil, fmt.Errorf("delegate_task: %w", err)
		}
		notify := input.Notify == nil || *input.Notify
		from := r.selfPane
		if host != "" {
			// The requester's pane lives on the local daemon; a remote daemon
			// cannot type into it. The task still completes and task_done is
			// still queued on the remote; the caller waits with wait_task.
			from = ""
		}
		redactCount := countRedactMarkers(input.Prompt)
		prompt := stripRedactMarkers(input.Prompt)
		detail := fmt.Sprintf("bytes=%d from=%s notify=%v", len(prompt), from, notify && from != "")
		if redactCount > 0 {
			detail += fmt.Sprintf(" [%d redacted]", redactCount)
		}
		mcpLog.Log(input.PaneID, "delegate_task", detail)
		resp, err := bridge.request(ipc.MsgDelegateTaskReq, ipc.DelegateTaskReqPayload{
			ToPane:    input.PaneID,
			Prompt:    prompt,
			FromPane:  from,
			Notify:    notify && from != "",
			TimeoutMs: input.Timeout * 1000,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("delegate_task: %w", err)
		}
		var payload ipc.DelegateTaskRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("delegate_task decode: %w", err)
		}
		if payload.Error != "" {
			return nil, nil, fmt.Errorf("delegate_task: %s", payload.Error)
		}
		r.remember(host, payload.Task.ID)
		return jsonResult(hostedTask{TaskInfo: payload.Task, Host: host}), nil, nil
	})
}

func registerGetTaskTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		TaskID string `json:"task_id" jsonschema:"task id from delegate_task"`
		Host   string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the task was created on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_task",
		Description: "Get a delegated task: state (sent, working, done, failed, timeout), timestamps, and — once ended — the target pane's last output lines as result.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		bridge, host, err := r.bridgeFor(input.Host, input.TaskID)
		if err != nil {
			return nil, nil, fmt.Errorf("get_task: %w", err)
		}
		if err := bridge.requireDaemon("get_task"); err != nil {
			return nil, nil, fmt.Errorf("get_task: %w", err)
		}
		resp, err := bridge.request(ipc.MsgGetTaskReq, ipc.GetTaskReqPayload{TaskID: input.TaskID})
		if err != nil {
			return nil, nil, fmt.Errorf("get_task: %w", err)
		}
		var payload ipc.GetTaskRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("get_task decode: %w", err)
		}
		if payload.Error != "" {
			return nil, nil, fmt.Errorf("get_task: %s", payload.Error)
		}
		return jsonResult(hostedTask{TaskInfo: payload.Task, Host: host}), nil, nil
	})
}

func registerWaitTaskTool(s *mcp.Server, r *mcpRouter, mcpLog *mcpLogger) {
	type Input struct {
		TaskID  string `json:"task_id" jsonschema:"task id from delegate_task"`
		Timeout int    `json:"timeout,omitempty" jsonschema:"seconds to wait (default 60, max 300)"`
		Host    string `json:"host,omitempty" jsonschema:"daemon host from list_hosts (empty = the host the task was created on, else local)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wait_task",
		Description: "Block until a delegated task reaches a terminal state or the timeout passes. Returns the task (with result) and timeout=true if it is still running.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		timeout := input.Timeout
		if timeout <= 0 {
			timeout = 60
		}
		if timeout > 300 {
			timeout = 300
		}
		bridge, host, err := r.bridgeFor(input.Host, input.TaskID)
		if err != nil {
			return nil, nil, fmt.Errorf("wait_task: %w", err)
		}
		if err := bridge.requireDaemon("wait_task"); err != nil {
			return nil, nil, fmt.Errorf("wait_task: %w", err)
		}
		mcpLog.Log("", "wait_task", fmt.Sprintf("task=%s timeout=%ds", input.TaskID, timeout))
		resp, err := bridge.requestWithTimeout(ipc.MsgWaitTaskReq,
			ipc.WaitTaskReqPayload{TaskID: input.TaskID, TimeoutMs: timeout * 1000},
			time.Duration(timeout+5)*time.Second)
		if err != nil {
			return nil, nil, fmt.Errorf("wait_task: %w", err)
		}
		var payload ipc.WaitTaskRespPayload
		if err := resp.DecodePayload(&payload); err != nil {
			return nil, nil, fmt.Errorf("wait_task decode: %w", err)
		}
		if payload.Error != "" {
			return nil, nil, fmt.Errorf("wait_task: %s", payload.Error)
		}
		return jsonResult(struct {
			hostedTask
			Timeout bool `json:"timeout"`
		}{hostedTask{TaskInfo: payload.Task, Host: host}, payload.Timeout}), nil, nil
	})
}

func registerListTasksTool(s *mcp.Server, r *mcpRouter) {
	type Input struct {
		PaneID string `json:"pane_id,omitempty" jsonschema:"only tasks where this pane is the requester or the target"`
		Host   string `json:"host,omitempty" jsonschema:"limit to one host (default: every connected host)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List delegated tasks (oldest first) across every connected host, optionally filtered to one pane.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input Input) (*mcp.CallToolResult, any, error) {
		host := input.Host
		if host == "" && input.PaneID != "" {
			if _, h, err := r.bridgeFor("", input.PaneID); err == nil {
				host = h
			}
		}
		var out []hostedTask
		err := r.forEachHost(host, func(hb hostBridge) error {
			if err := hb.bridge.requireDaemon("list_tasks"); err != nil {
				return err
			}
			resp, err := hb.bridge.request(ipc.MsgListTasksReq, ipc.ListTasksReqPayload{PaneID: input.PaneID})
			if err != nil {
				return fmt.Errorf("list_tasks%s: %w", hostSuffix(hb.host), err)
			}
			var payload ipc.ListTasksRespPayload
			if err := resp.DecodePayload(&payload); err != nil {
				return fmt.Errorf("list_tasks decode: %w", err)
			}
			for _, t := range payload.Tasks {
				r.remember(hb.host, t.ID)
				out = append(out, hostedTask{TaskInfo: t, Host: hb.host})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list_tasks: %w", err)
		}
		if out == nil {
			out = []hostedTask{}
		}
		return jsonResult(out), nil, nil
	})
}
