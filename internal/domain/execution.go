package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
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
	}
	return false
}

type Execution struct {
	ID            uuid.UUID       `json:"id"`
	AgentID       string          `json:"agent_id"`
	Input         json.RawMessage `json:"input"`
	Status        ExecutionStatus `json:"status"`
	Output        json.RawMessage `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeadlineAt    *time.Time      `json:"deadline_at,omitempty"`
}

// Cursor holds an execution ID. IDs are UUIDv7, so ordering by ID orders by creation.
type ExecutionFilter struct {
	AgentID string
	Status  ExecutionStatus
	Limit   int
	Cursor  string
}

// Empty NextCursor means the last page; otherwise pass it back as ExecutionFilter.Cursor.
type ExecutionPage struct {
	Executions []Execution `json:"executions"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
