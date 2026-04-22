package domain

import (
	"encoding/json"
	"time"
)

type ExecutionStatus string

const (
	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionBlocked   ExecutionStatus = "blocked"
	ExecutionCompleted ExecutionStatus = "completed"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCancelled ExecutionStatus = "cancelled"
)

func (s ExecutionStatus) IsTerminal() bool {
	switch s {
	case ExecutionCompleted, ExecutionFailed, ExecutionCancelled:
		return true
	default:
		return false
	}
}

type Execution struct {
	ID        string            `json:"id"`
	Status    ExecutionStatus   `json:"status"`
	AgentID   string            `json:"agent_id"`
	Labels    map[string]string `json:"labels"`
	Input     json.RawMessage   `json:"input"`
	Output    json.RawMessage   `json:"output,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type ExecutionState struct {
	Execution        Execution                   `json:"execution"`
	ActiveSteps      map[string]*Step            `json:"active_steps,omitempty"`
	Steps            map[string]*Step            `json:"steps"`
	PendingSignals   []Signal                    `json:"pending_signals,omitempty"`
	History          []HistoryEntry              `json:"history,omitempty"`
	AgentID          string                      `json:"agent_id"`
	LastSequence     int64                       `json:"last_sequence"`
	Tainted          bool                        `json:"tainted"`
	TaintedReason    string                      `json:"tainted_reason,omitempty"`
	BlockedReason    string                      `json:"blocked_reason,omitempty"`
	BlockedRef       string                      `json:"blocked_ref,omitempty"`
	PendingApprovals map[string]*PendingApproval `json:"pending_approvals,omitempty"`
}

func (s *ExecutionState) HasActiveSteps() bool {
	for _, step := range s.ActiveSteps {
		if !step.Status.IsTerminal() {
			return true
		}
	}
	return false
}

type PendingApproval struct {
	StepID    string          `json:"step_id"`
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
	Remote    bool            `json:"remote"`
}

type HistoryEntry struct {
	StepID      string          `json:"step_id"`
	ToolID      string          `json:"tool_id"`
	Status      StepStatus      `json:"status"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

type SnapshotConfig struct {
	MaxHistoryEntries int `json:"max_history_entries" yaml:"max_history_entries"`
	MaxHistoryBytes   int `json:"max_history_bytes" yaml:"max_history_bytes"`
}

func DefaultSnapshotConfig() SnapshotConfig {
	return SnapshotConfig{
		MaxHistoryEntries: 50,
		MaxHistoryBytes:   512 * 1024,
	}
}

type Checkpoint struct {
	ExecutionID string          `json:"execution_id"`
	Sequence    int64           `json:"sequence"`
	StateData   json.RawMessage `json:"state_data"`
	CreatedAt   time.Time       `json:"created_at"`
}

type ExecutionSummary struct {
	ID        string            `json:"id"`
	Status    ExecutionStatus   `json:"status"`
	AgentID   string            `json:"agent_id"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type ExecutionFilter struct {
	Status  ExecutionStatus   `json:"status,omitempty"`
	AgentID string            `json:"agent_id,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}
