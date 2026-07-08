package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type StepKind string

const (
	StepKindTool StepKind = "tool_call"
	StepKindLLM  StepKind = "llm_call"
)

type StepStatus string

const (
	StepProposed         StepStatus = "proposed"
	StepAllowed          StepStatus = "allowed"
	StepDenied           StepStatus = "denied"
	StepAwaitingApproval StepStatus = "awaiting_approval"
	StepExecuting        StepStatus = "executing"
	StepSucceeded        StepStatus = "succeeded"
	StepFailed           StepStatus = "failed"
)

func (s StepStatus) IsTerminal() bool {
	switch s {
	case StepSucceeded, StepFailed, StepDenied:
		return true
	}
	return false
}

type Step struct {
	StepID      string          `json:"step_id"`
	ExecutionID uuid.UUID       `json:"execution_id"`
	Kind        StepKind        `json:"kind"`
	Target      string          `json:"target"`
	ArgsHash    string          `json:"args_hash"`
	Occurrence  int             `json:"occurrence"`
	Status      StepStatus      `json:"status"`
	Idempotency string          `json:"idempotency"`
	Args        json.RawMessage `json:"args,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}
