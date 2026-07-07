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
	AgentVersion  string          `json:"agent_version,omitempty"`
	Input         json.RawMessage `json:"input"`
	Status        ExecutionStatus `json:"status"`
	Output        json.RawMessage `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	DeadlineAt    *time.Time      `json:"deadline_at,omitempty"`
}

// ExecutionFilter selects and pages a list of executions. The zero value lists
// the most recent executions across all agents. Executions are ordered newest
// first by ID (UUIDv7, so ID order matches creation order), and paging is
// keyset-based: Cursor is the ID of the last execution from the previous page.
type ExecutionFilter struct {
	AgentID string
	Status  ExecutionStatus
	Limit   int
	Cursor  string
}

// ExecutionPage is one page of a listing. NextCursor is empty when there are no
// further pages; otherwise pass it back as ExecutionFilter.Cursor.
type ExecutionPage struct {
	Executions []Execution `json:"executions"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
