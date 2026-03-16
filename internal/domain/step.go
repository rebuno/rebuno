package domain

import (
	"encoding/json"
	"time"
)

type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepDispatched StepStatus = "dispatched"
	StepRunning    StepStatus = "running"
	StepSucceeded  StepStatus = "succeeded"
	StepFailed     StepStatus = "failed"
	StepTimedOut   StepStatus = "timed_out"
	StepCancelled  StepStatus = "cancelled"
)

func (s StepStatus) IsTerminal() bool {
	switch s {
	case StepSucceeded, StepFailed, StepTimedOut, StepCancelled:
		return true
	default:
		return false
	}
}

type Step struct {
	ID             string          `json:"id"`
	ExecutionID    string          `json:"execution_id"`
	ToolID         string          `json:"tool_id"`
	ToolVersion    int             `json:"tool_version"`
	Status         StepStatus      `json:"status"`
	Attempt        int             `json:"attempt"`
	MaxAttempts    int             `json:"max_attempts"`
	Arguments      json.RawMessage `json:"arguments"`
	IdempotencyKey string          `json:"idempotency_key"`

	Deadline     *time.Time `json:"deadline,omitempty"`
	RunnerID     string     `json:"runner_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`

	Result    json.RawMessage `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Retryable bool            `json:"retryable"`
}
