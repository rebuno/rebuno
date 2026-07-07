package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Dispatch struct {
	ID            uuid.UUID
	ExecutionID   uuid.UUID
	Status        DispatchStatus
	Attempt       int
	MaxAttempts   int
	NextAttemptAt time.Time
	LockedBy      *string
	LockedAt      *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type DispatchStatus string

const (
	DispatchPending   DispatchStatus = "pending"
	DispatchInFlight  DispatchStatus = "in_flight"
	DispatchAcked     DispatchStatus = "acked"
	DispatchFailed    DispatchStatus = "failed"
	DispatchExhausted DispatchStatus = "exhausted"
)

type StepDecision struct {
	Decision   string          `json:"decision"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      json.RawMessage `json:"error,omitempty"`
	ApprovalID *uuid.UUID      `json:"approval_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}
