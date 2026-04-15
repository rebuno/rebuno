package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Job struct {
	ID          uuid.UUID       `json:"id"`
	ExecutionID string          `json:"execution_id"`
	StepID      string          `json:"step_id"`
	Attempt     int             `json:"attempt"`
	ToolID      string          `json:"tool_id"`
	ToolVersion int             `json:"tool_version"`
	Arguments   json.RawMessage `json:"arguments"`
	Deadline    time.Time       `json:"deadline"`
	NotBefore   time.Time       `json:"not_before,omitempty"`
}

type JobResult struct {
	JobID       string          `json:"job_id"`
	ExecutionID string          `json:"execution_id"`
	StepID      string          `json:"step_id"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	Retryable   bool            `json:"retryable"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	RunnerID    string          `json:"runner_id"`
}
