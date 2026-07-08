package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	EventExecutionCreated   = "execution.created"
	EventExecutionStarted   = "execution.started"
	EventExecutionCompleted = "execution.completed"
	EventExecutionFailed    = "execution.failed"
	EventExecutionCancelled = "execution.cancelled"
	EventExecutionBlocked   = "execution.blocked"
	EventExecutionResumed   = "execution.resumed"

	EventStepProposed         = "step.proposed"
	EventStepAllowed          = "step.allowed"
	EventStepDenied           = "step.denied"
	EventStepAwaitingApproval = "step.awaiting_approval"
	EventStepExecuting        = "step.executing"
	EventStepSucceeded        = "step.succeeded"
	EventStepFailed           = "step.failed"
	EventStepCancelled        = "step.cancelled"

	EventApprovalRequested = "approval.requested"
	EventApprovalGranted   = "approval.granted"
	EventApprovalDenied    = "approval.denied"
	EventApprovalExpired   = "approval.expired"

	EventDispatchSent      = "dispatch.sent"
	EventDispatchAcked     = "dispatch.acked"
	EventDispatchFailed    = "dispatch.failed"
	EventDispatchRetried   = "dispatch.retried"
	EventDispatchExhausted = "dispatch.exhausted"
)

type Event struct {
	ExecutionID uuid.UUID       `json:"execution_id"`
	EventSeq    int64           `json:"event_seq"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	OccurredAt  time.Time       `json:"occurred_at"`
}
