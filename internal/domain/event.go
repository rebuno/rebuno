package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type EventType string

const (
	EventExecutionCreated   EventType = "execution.created"
	EventExecutionStarted   EventType = "execution.started"
	EventExecutionBlocked   EventType = "execution.blocked"
	EventExecutionResumed   EventType = "execution.resumed"
	EventExecutionCompleted EventType = "execution.completed"
	EventExecutionFailed    EventType = "execution.failed"
	EventExecutionCancelled EventType = "execution.cancelled"
	EventExecutionReset     EventType = "execution.reset"
)

const (
	EventIntentAccepted EventType = "intent.accepted"
	EventIntentDenied   EventType = "intent.denied"
)

const (
	EventStepCreated          EventType = "step.created"
	EventStepDispatched       EventType = "step.dispatched"
	EventStepStarted          EventType = "step.started"
	EventStepCompleted        EventType = "step.completed"
	EventStepFailed           EventType = "step.failed"
	EventStepTimedOut         EventType = "step.timed_out"
	EventStepRetried          EventType = "step.retried"
	EventStepCancelled        EventType = "step.cancelled"
	EventStepApprovalRequired EventType = "step.approval_required"
)

const (
	EventSignalReceived EventType = "signal.received"
	EventAgentTimeout   EventType = "agent.timeout"
)

type Event struct {
	ID             uuid.UUID       `json:"id"`
	ExecutionID    string          `json:"execution_id"`
	StepID         string          `json:"step_id,omitempty"`
	Type           EventType       `json:"type"`
	SchemaVersion  int             `json:"schema_version"`
	Timestamp      time.Time       `json:"timestamp"`
	Payload        json.RawMessage `json:"payload"`
	CausationID    uuid.UUID       `json:"causation_id"`
	CorrelationID  uuid.UUID       `json:"correlation_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Sequence       int64           `json:"sequence"`
}

type ExecutionCreatedPayload struct {
	AgentID string            `json:"agent_id"`
	Input   json.RawMessage   `json:"input"`
	Labels  map[string]string `json:"labels"`
}

type ExecutionStartedPayload struct {
	SessionID  string `json:"session_id"`
	ConsumerID string `json:"consumer_id"`
}

type ExecutionBlockedPayload struct {
	Reason    string          `json:"reason"`              // "tool", "signal", or "approval"
	Ref       string          `json:"ref"`                 // step_id or signal_type
	ToolID    string          `json:"tool_id,omitempty"`   // set when reason="approval"
	Arguments json.RawMessage `json:"arguments,omitempty"` // set when reason="approval"
	Remote    bool            `json:"remote,omitempty"`    // set when reason="approval"
}

type ExecutionResumedPayload struct {
	Reason string `json:"reason"`
}

type ExecutionCompletedPayload struct {
	Output json.RawMessage `json:"output"`
}

type ExecutionFailedPayload struct {
	Error string `json:"error"`
}

type ExecutionCancelledPayload struct {
	Reason string `json:"reason"`
}

type ExecutionResetPayload struct {
	Reason     string `json:"reason"`      // e.g. "agent_disconnect", "recovery"
	FromStatus string `json:"from_status"` // status before reset
}

type IntentAcceptedPayload struct {
	IntentType string          `json:"intent_type"`
	Details    json.RawMessage `json:"details,omitempty"`
}

type IntentDeniedPayload struct {
	IntentType     string          `json:"intent_type"`
	ToolID         string          `json:"tool_id,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Reason         string          `json:"reason"`
	RuleID         string          `json:"rule_id"`
}

type StepCreatedPayload struct {
	ToolID      string          `json:"tool_id"`
	ToolVersion int             `json:"tool_version"`
	Arguments   json.RawMessage `json:"arguments"`
	MaxAttempts int             `json:"max_attempts"`
	Attempt     int             `json:"attempt"`
}

type StepDispatchedPayload struct {
	RunnerID string    `json:"runner_id"`
	JobID    string    `json:"job_id"`
	Deadline time.Time `json:"deadline"`
}

type StepStartedPayload struct {
	RunnerID string `json:"runner_id"`
}

type StepCompletedPayload struct {
	Result json.RawMessage `json:"result"`
}

type StepFailedPayload struct {
	Error     string `json:"error"`
	Retryable bool   `json:"retryable"`
}

type StepTimedOutPayload struct{}

type StepCancelledPayload struct {
	Reason string `json:"reason"`
}

type StepApprovalRequiredPayload struct {
	ToolID    string          `json:"tool_id"`
	Arguments json.RawMessage `json:"arguments"`
	Remote    bool            `json:"remote"`
	Reason    string          `json:"reason"`
}

type StepRetriedPayload struct {
	NextAttempt int `json:"next_attempt"`
}

type SignalReceivedPayload struct {
	SignalType string          `json:"signal_type"`
	Payload    json.RawMessage `json:"payload"`
}

type AgentTimeoutPayload struct {
	SessionID string `json:"session_id"`
}
