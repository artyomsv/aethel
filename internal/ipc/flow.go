package ipc

import "github.com/artyomsv/quil/internal/flow"

const (
	MsgStartFlowReq       = "start_flow_req"
	MsgStartFlowResp      = "start_flow_resp"
	MsgResumeFlowReq      = "resume_flow_req"
	MsgResumeFlowResp     = "resume_flow_resp"
	MsgReportStepReq      = "report_step_req"
	MsgReportStepResp     = "report_step_resp"
	MsgFlowConfigReq      = "flow_config_req"
	MsgFlowConfigResp     = "flow_config_resp"
	MsgSaveFlowConfigReq  = "save_flow_config_req"
	MsgSaveFlowConfigResp = "save_flow_config_resp"
)

type StartFlowReqPayload struct {
	Feature   string `json:"feature"`
	Branch    string `json:"branch"`
	ProjectID string `json:"project_id,omitempty"`
	// CWD names the repository the flow works in: the worktree is added off
	// the repository containing it. Empty means the project root. The daemon
	// refuses a directory it cannot use rather than falling back, because a
	// flow started in the wrong repository is a wrong branch and a wrong PR.
	CWD string `json:"cwd,omitempty"`
}

type StartFlowRespPayload struct {
	FlowID string `json:"flow_id,omitempty"`
	TabID  string `json:"tab_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ResumeFlowReqPayload struct {
	FlowID string `json:"flow_id"`
}

type ReportStepReqPayload struct {
	PaneID string            `json:"pane_id"`
	TaskID string            `json:"task_id,omitempty"`
	Status string            `json:"status"`
	Result map[string]string `json:"result"`
}

type ReportStepRespPayload struct {
	TaskID string `json:"task_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type FlowConfigRespPayload struct {
	Config FlowConfig `json:"config"`
	Error  string     `json:"error,omitempty"`
}

type SaveFlowConfigReqPayload struct {
	Config FlowConfig `json:"config"`
}

// FlowConfig is the stable wire schema, independent of the TOML configuration.
type FlowConfig struct {
	MaxReviewRounds    int                          `json:"max_review_rounds"`
	StepTimeoutMinutes int                          `json:"step_timeout_minutes"`
	Roles              map[flow.Role]FlowRoleConfig `json:"roles"`
}
type FlowRoleConfig struct {
	Agent     string   `json:"agent"`
	Toggles   []string `json:"toggles"`
	Model     string   `json:"model,omitempty"`
	Prompt    string   `json:"prompt"`
	FixPrompt string   `json:"fix_prompt,omitempty"`
}
