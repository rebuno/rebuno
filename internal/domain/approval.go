package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type ApprovalStatus string

const (
	ApprovalPending ApprovalStatus = "pending"
	ApprovalGranted ApprovalStatus = "granted"
	ApprovalDenied  ApprovalStatus = "denied"
	ApprovalExpired ApprovalStatus = "expired"
)

type Approval struct {
	ID          uuid.UUID       `json:"id"`
	StepID      string          `json:"step_id"`
	ExecutionID uuid.UUID       `json:"execution_id"`
	Status      ApprovalStatus  `json:"status"`
	Approvers   json.RawMessage `json:"approvers,omitempty"`
	Message     string          `json:"message,omitempty"`
	TimeoutAt   time.Time       `json:"timeout_at"`
	DecidedBy   string          `json:"decided_by,omitempty"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
	Rationale   string          `json:"rationale,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}
