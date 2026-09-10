package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/artyomsv/quil/internal/config"
	"github.com/artyomsv/quil/internal/flow"
	"github.com/artyomsv/quil/internal/hookevents"
	"github.com/artyomsv/quil/internal/ipc"
	"github.com/google/uuid"
)

func (d *Daemon) reloadFlows() {
	cfg, err := config.LoadFlows()
	d.session.mu.Lock()
	defer d.session.mu.Unlock()
	d.session.flowConfigError = ""
	if err != nil {
		d.session.flowConfigError = err.Error()
		return
	}
	d.session.flowConfig = cfg
}

func (d *Daemon) flowsConfig() (config.Flows, error) {
	d.session.mu.RLock()
	defer d.session.mu.RUnlock()
	if d.session.flowConfigError != "" {
		return config.Flows{}, fmt.Errorf("%s", d.session.flowConfigError)
	}
	if d.session.flowConfig.Roles == nil {
		return config.DefaultFlows(), nil
	}
	return d.session.flowConfig, nil
}

func (d *Daemon) flowSnapshots() []flow.Flow {
	d.session.mu.RLock()
	defer d.session.mu.RUnlock()
	out := make([]flow.Flow, 0, len(d.session.flows))
	for _, f := range d.session.flows {
		if d.session.tabs[f.TabID] != nil {
			out = append(out, f.Clone())
		}
	}
	return out
}

func (d *Daemon) restoreFlows(raw any) {
	b, err := json.Marshal(raw)
	if err != nil {
		return
	}
	var fs []flow.Flow
	if json.Unmarshal(b, &fs) != nil {
		return
	}
	d.session.mu.Lock()
	defer d.session.mu.Unlock()
	d.session.flows = make(map[string]*flow.Flow)
	for _, f := range fs {
		if d.session.tabs[f.TabID] == nil || f.Stage == flow.StagePreparing {
			continue
		}
		f.TaskID = ""
		if f.Stage.Role() != "" {
			f = flow.Pause(f, "daemon restarted")
		}
		copy := f.Clone()
		d.session.flows[f.ID] = &copy
	}
}

func (d *Daemon) handleStartFlowReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.StartFlowReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgStartFlowResp, ipc.StartFlowRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	// Root resolution can touch a dead filesystem; leave the client dispatch free.
	go func() {
		resp, prepare := d.startFlow(req)
		respondTo(conn, msg.ID, ipc.MsgStartFlowResp, resp)
		if prepare != nil {
			prepare()
		}
	}()
}

func (d *Daemon) validateFlowRoles(cfg config.Flows) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, role := range flow.Roles {
		r := cfg.Roles[role]
		p := d.registry.Get(r.Agent)
		if p == nil || p.Category != "ai" || !p.Available {
			return fmt.Errorf("%s agent %q is not an available AI plugin", role, r.Agent)
		}
		if !flowMCPSupported(r.Agent) {
			return fmt.Errorf("%s agent %q has no per-spawn MCP support", role, r.Agent)
		}
		if _, err := resolveToggles(p, r.Toggles); err != nil {
			return err
		}
	}
	_, err := flowMCPExeFn()
	return err
}

// startFlow validates before allocating anything. The caller answers before
// running prepare, which performs the worktree checkout on its worker.
func (d *Daemon) startFlow(req ipc.StartFlowReqPayload) (ipc.StartFlowRespPayload, func()) {
	fail := func(err error) (ipc.StartFlowRespPayload, func()) {
		return ipc.StartFlowRespPayload{Error: err.Error()}, nil
	}
	if strings.TrimSpace(req.Feature) == "" {
		return fail(fmt.Errorf("feature is empty"))
	}
	if len(req.Feature) > 128*1024 {
		return fail(fmt.Errorf("feature exceeds 128 KiB"))
	}
	if req.Branch == "" {
		return fail(fmt.Errorf("branch is empty"))
	}
	if req.ProjectID == "" {
		req.ProjectID = d.session.ActiveProject()
	}
	if req.ProjectID != "" && !d.projectExists(req.ProjectID) {
		return fail(fmt.Errorf("no such project: %s", req.ProjectID))
	}
	cfg, err := d.flowsConfig()
	if err != nil {
		return fail(err)
	}
	if err := d.validateFlowRoles(cfg); err != nil {
		return fail(err)
	}
	payloads := make(map[flow.Role]ipc.CreatePanePayload)
	var cwd string
	for _, role := range flow.Roles {
		r := cfg.Roles[role]
		branch := ""
		if role == flow.Analyst {
			branch = req.Branch
		}
		p, dir, err := d.buildCreatePayload(ipc.CreatePaneReqPayload{Type: r.Agent, Toggles: r.Toggles, WorktreeBranch: branch}, "", d.projectCWD(req.ProjectID))
		if err != nil {
			return fail(err)
		}
		p.FlowRole = string(role)
		payloads[role], cwd = p, dir
	}
	tab := d.session.CreateTabInProject(req.ProjectID, req.Branch)
	analyst := payloads[flow.Analyst]
	placeholder, err := d.constructPreparingPane(tab.ID, cwd, "terminal", ipc.FirstPaneSpec{Worktree: analyst.Worktree})
	if err != nil {
		_ = d.session.DestroyTab(tab.ID)
		return fail(err)
	}
	applyPaneName(placeholder, string(flow.Analyst))
	for role, p := range payloads {
		p.TabID = tab.ID
		payloads[role] = p
	}
	analyst = payloads[flow.Analyst]
	analyst.ReplacePaneID = placeholder.ID
	payloads[flow.Analyst] = analyst
	now := time.Now().UnixMilli()
	f := flow.Flow{ID: "flow-" + uuid.NewString(), TabID: tab.ID, Branch: req.Branch, Feature: req.Feature,
		Stage: flow.StagePreparing, Panes: map[flow.Role]string{flow.Analyst: placeholder.ID},
		MaxReviewRounds: cfg.MaxReviewRounds, CreatedAt: now, UpdatedAt: now}
	d.session.mu.Lock()
	if d.session.flows == nil {
		d.session.flows = make(map[string]*flow.Flow)
	}
	if d.session.flowPreparing == nil {
		d.session.flowPreparing = make(map[string]map[flow.Role]ipc.CreatePanePayload)
	}
	d.session.flows[f.ID], d.session.flowPreparing[f.ID] = &f, payloads
	d.session.mu.Unlock()
	d.broadcastState()
	d.requestSnapshot()
	return ipc.StartFlowRespPayload{FlowID: f.ID, TabID: tab.ID}, func() { d.prepareFlow(f.ID) }
}

func (d *Daemon) prepareFlow(id string) {
	d.session.mu.RLock()
	payloads := d.session.flowPreparing[id]
	d.session.mu.RUnlock()
	if payloads == nil {
		return
	}
	analyst := payloads[flow.Analyst]
	resp := d.worktreeAddAndCreate(analyst)
	if resp.Error != "" {
		if !resp.Swapped {
			d.failPreparingPane(analyst.ReplacePaneID, "worktree not created: "+resp.Error)
		}
		d.pauseFlow(id, resp.Error)
		return
	}
	pane := d.session.Pane(resp.PaneID)
	if pane == nil {
		d.pauseFlow(id, "pane analyst was closed")
		return
	}
	applyPaneName(pane, string(flow.Analyst))
	pane.PluginMu.Lock()
	cwd := pane.CWD
	pane.PluginMu.Unlock()
	panes := map[flow.Role]string{flow.Analyst: pane.ID}
	var spawnErr error
	for _, role := range []flow.Role{flow.Developer, flow.Reviewer} {
		p := payloads[role]
		created, err := d.constructPaneAt(p, cwd, p.Type)
		if created != nil {
			applyPaneName(created, string(role))
			panes[role] = created.ID
		}
		if err != nil && spawnErr == nil {
			spawnErr = err
		}
	}
	d.session.mu.Lock()
	f := d.session.flows[id]
	if f == nil {
		d.session.mu.Unlock()
		return
	}
	f.Panes = panes
	// Once the placeholder is gone, preparing is never retried. A role spawn
	// failure pauses at plan; restart the failed pane, then explicitly resume.
	f.Stage = flow.StagePlan
	delete(d.session.flowPreparing, id)
	d.session.mu.Unlock()
	if spawnErr != nil {
		d.pauseFlow(id, spawnErr.Error())
		return
	}
	d.broadcastState()
	d.requestSnapshot()
	d.dispatchFlow(id)
}

func (d *Daemon) pauseFlow(id, why string) {
	d.session.mu.Lock()
	f := d.session.flows[id]
	if f == nil {
		d.session.mu.Unlock()
		return
	}
	*f = flow.Pause(*f, why)
	f.UpdatedAt = time.Now().UnixMilli()
	snap := f.Clone()
	d.session.mu.Unlock()
	d.publishFlow(snap)
}

func (d *Daemon) publishFlow(f flow.Flow) {
	d.broadcastState()
	d.requestSnapshot()
	if !f.Paused && f.Stage != flow.StageReadyForYou {
		return
	}
	role := f.Stage.Role()
	if role == "" {
		role = flow.Reviewer
		if f.Stage == flow.StagePreparing {
			role = flow.Analyst
		}
	}
	typ, title, severity := "flow_ready", "PR "+f.Results.PR+" is ready for you", "info"
	if f.Paused {
		typ, title, severity = "flow_paused", f.PauseWhy, "warning"
	}
	d.emitEvent(PaneEvent{ID: uuid.NewString(), PaneID: f.Panes[role], TabID: f.TabID, PaneName: string(role),
		Type: typ, Title: title, Severity: severity, Timestamp: time.Now(), Data: map[string]string{"flow_id": f.ID}})
}

func (d *Daemon) dispatchFlow(id string) {
	cfg, err := d.flowsConfig()
	if err != nil {
		d.pauseFlow(id, err.Error())
		return
	}
	d.session.mu.Lock()
	f := d.session.flows[id]
	if f == nil || f.Paused || f.TaskID != "" || f.Stage.Role() == "" {
		d.session.mu.Unlock()
		return
	}
	f.TaskID = "task-" + uuid.NewString()
	f.UpdatedAt = time.Now().UnixMilli()
	snap := f.Clone()
	d.session.mu.Unlock()
	resp := d.delegateTaskWithID(ipc.DelegateTaskReqPayload{ToPane: snap.Panes[snap.Stage.Role()],
		Prompt: cfg.Prompt(snap), TimeoutMs: cfg.StepTimeoutMinutes * 60000}, snap.TaskID)
	if resp.Error != "" {
		// A completion may already have advanced the stage while delivery ran.
		d.session.mu.Lock()
		f = d.session.flows[id]
		if f == nil || f.TaskID != snap.TaskID {
			d.session.mu.Unlock()
			return
		}
		*f = flow.Pause(*f, resp.Error)
		snap = f.Clone()
		d.session.mu.Unlock()
		d.publishFlow(snap)
	}
}

// Called only by finishTask, after it releases the task registry lock.
func (d *Daemon) flowOnTaskEnd(info ipc.TaskInfo, report *flow.Report) {
	d.session.mu.Lock()
	var current *flow.Flow
	for _, f := range d.session.flows {
		if f.TaskID == info.ID {
			current = f
			break
		}
	}
	if current == nil {
		d.session.mu.Unlock()
		return
	}
	var next flow.Flow
	if info.State != string(taskDone) {
		why := info.Error
		if why == "" {
			why = "timed out"
			if info.State != string(taskTimeout) {
				why = "step failed"
			}
		}
		if d.session.panes[info.ToPane] == nil || info.Error == "pane destroyed" {
			why = "pane " + string(current.Stage.Role()) + " was closed"
		}
		next = flow.Pause(*current, why)
	} else {
		var err error
		next, err = flow.Next(*current, report)
		if err != nil {
			next = flow.Pause(*current, err.Error())
		}
	}
	next.UpdatedAt = time.Now().UnixMilli()
	*current = next
	snap := next.Clone()
	d.session.mu.Unlock()
	d.publishFlow(snap)
	if !snap.Paused && snap.Stage.Role() != "" {
		d.dispatchFlow(snap.ID)
	}
}

func (d *Daemon) resumeFlow(id string) error {
	d.session.mu.Lock()
	f := d.session.flows[id]
	if f == nil {
		d.session.mu.Unlock()
		return fmt.Errorf("no such flow")
	}
	if !f.Paused {
		d.session.mu.Unlock()
		return fmt.Errorf("flow is not paused")
	}
	preparing := f.Stage == flow.StagePreparing
	if preparing {
		p := d.session.panes[f.Panes[flow.Analyst]]
		valid := false
		if p != nil {
			p.PluginMu.Lock()
			valid = p.WorktreeInterrupted && p.SpawnError != ""
			p.PluginMu.Unlock()
		}
		if !valid || d.session.flowPreparing[id] == nil {
			d.session.mu.Unlock()
			return fmt.Errorf("the failed placeholder is gone; close this tab and start a new flow")
		}
	} else {
		for _, role := range flow.Roles {
			if d.session.panes[f.Panes[role]] == nil {
				d.session.mu.Unlock()
				return fmt.Errorf("pane %s was closed; close this tab and start a new flow", role)
			}
		}
	}
	*f, _ = flow.Resume(*f)
	f.UpdatedAt = time.Now().UnixMilli()
	d.session.mu.Unlock()
	d.broadcastState()
	d.requestSnapshot()
	if preparing {
		go d.prepareFlow(id)
	} else {
		d.dispatchFlow(id)
	}
	return nil
}

func (d *Daemon) handleResumeFlowReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ResumeFlowReqPayload
	err := msg.DecodePayload(&req)
	if err == nil {
		err = d.resumeFlow(req.FlowID)
	}
	resp := ipc.StartFlowRespPayload{FlowID: req.FlowID}
	if err != nil {
		resp.Error = err.Error()
	}
	respondTo(conn, msg.ID, ipc.MsgResumeFlowResp, resp)
}

func (d *Daemon) handleReportStepReq(conn *ipc.Conn, msg *ipc.Message) {
	var req ipc.ReportStepReqPayload
	if err := msg.DecodePayload(&req); err != nil {
		respondTo(conn, msg.ID, ipc.MsgReportStepResp, ipc.ReportStepRespPayload{Error: "malformed payload: " + err.Error()})
		return
	}
	respondTo(conn, msg.ID, ipc.MsgReportStepResp, d.reportStep(req))
}

func (d *Daemon) reportStep(req ipc.ReportStepReqPayload) ipc.ReportStepRespPayload {
	fail := func(why string) ipc.ReportStepRespPayload { return ipc.ReportStepRespPayload{Error: why} }
	r := &flow.Report{Status: req.Status, Result: make(map[string]string, len(req.Result))}
	for k, v := range req.Result {
		r.Result[k] = v
	}
	if err := flow.ValidateReport(*r); err != nil {
		return fail(err.Error())
	}
	reg := d.tasksRegistry()
	reg.mu.Lock()
	t := reg.byID[req.TaskID]
	if req.TaskID == "" {
		for _, candidate := range reg.byID {
			if candidate.to == req.PaneID && !candidate.state.terminal() {
				t = candidate
				break
			}
		}
	}
	if t == nil {
		reg.mu.Unlock()
		return fail("no step is waiting on this pane")
	}
	if req.PaneID == "" || t.to != req.PaneID {
		reg.mu.Unlock()
		return fail("pane is not this task's target")
	}
	if t.state.terminal() {
		reg.mu.Unlock()
		return fail("step already ended")
	}
	t.report = r
	id := t.id
	reg.mu.Unlock()
	// Unknown is distinct from idle. Recheck at expiry so hooks that loaded
	// during the window restore the strict report-then-settled-idle contract.
	if p := d.session.Pane(req.PaneID); p != nil {
		p.workMu.Lock()
		unknown := p.Work.State() == hookevents.WorkUnknown
		p.workMu.Unlock()
		if unknown {
			time.AfterFunc(agentIdleSettle, func() {
				select {
				case <-d.shutdown:
					return
				default:
				}
				p := d.session.Pane(req.PaneID)
				if p == nil {
					return
				}
				p.workMu.Lock()
				unknown := p.Work.State() == hookevents.WorkUnknown
				p.workMu.Unlock()
				if unknown {
					d.finishTaskIf(t, taskDone, "", true)
				}
			})
		}
	}
	return ipc.ReportStepRespPayload{TaskID: id}
}

func (d *Daemon) handleFlowConfigReq(conn *ipc.Conn, msg *ipc.Message) {
	if msg.Type == ipc.MsgSaveFlowConfigReq {
		var req ipc.SaveFlowConfigReqPayload
		err := msg.DecodePayload(&req)
		if err == nil {
			err = d.validateFlowRoles(req.Config)
		}
		if err == nil {
			err = config.WriteFlows(req.Config)
		}
		if err == nil {
			d.reloadFlows()
		}
		resp := ipc.FlowConfigRespPayload{Config: req.Config}
		if err != nil {
			resp.Error = err.Error()
		}
		respondTo(conn, msg.ID, ipc.MsgSaveFlowConfigResp, resp)
		return
	}
	cfg, err := d.flowsConfig()
	resp := ipc.FlowConfigRespPayload{Config: cfg}
	if err != nil {
		resp.Error = err.Error()
	}
	respondTo(conn, msg.ID, ipc.MsgFlowConfigResp, resp)
}
