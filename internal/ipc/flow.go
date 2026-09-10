package ipc

import "github.com/artyomsv/quil/internal/config"

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
	Config config.Flows `json:"config"`
	Error  string       `json:"error,omitempty"`
}

type SaveFlowConfigReqPayload struct {
	Config config.Flows `json:"config"`
}
