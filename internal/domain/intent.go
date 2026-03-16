package domain

import "encoding/json"

type IntentType string

const (
	IntentInvokeTool IntentType = "invoke_tool"
	IntentWait       IntentType = "wait"
	IntentComplete   IntentType = "complete"
	IntentFail       IntentType = "fail"
)

type Intent struct {
	Type           IntentType      `json:"type"`
	ToolID         string          `json:"tool_id,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	SignalType     string          `json:"signal_type,omitempty"`
	Output         json.RawMessage `json:"output,omitempty"`
	Error          string          `json:"error,omitempty"`
	Remote         bool            `json:"remote,omitempty"`
}

type IntentRequest struct {
	ExecutionID string `json:"execution_id"`
	SessionID   string `json:"session_id"`
	Intent      Intent `json:"intent"`
}

type IntentResult struct {
	Accepted        bool   `json:"accepted"`
	StepID          string `json:"step_id,omitempty"`
	Error           string `json:"error,omitempty"`
	PendingApproval bool   `json:"pending_approval,omitempty"`
}
