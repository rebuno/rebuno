package projector

import (
	"encoding/json"
	"fmt"

	"github.com/rebuno/rebuno/internal/domain"
)

func registerExecutionHandlers(p *Projector) {
	p.Register(domain.EventExecutionCreated, applyExecutionCreated)
	p.Register(domain.EventExecutionStarted, applyExecutionStarted)
	p.Register(domain.EventExecutionBlocked, applyExecutionBlocked)
	p.Register(domain.EventExecutionResumed, applyExecutionResumed)
	p.Register(domain.EventExecutionCompleted, applyExecutionCompleted)
	p.Register(domain.EventExecutionFailed, applyExecutionFailed)
	p.Register(domain.EventExecutionCancelled, applyExecutionCancelled)
	p.Register(domain.EventExecutionReset, applyExecutionReset)

	p.Register(domain.EventIntentAccepted, noOp)
	p.Register(domain.EventIntentDenied, noOp)
	p.Register(domain.EventAgentTimeout, noOp)
	p.Register(domain.EventStepApprovalRequired, noOp)
}

func applyExecutionCreated(state *domain.ExecutionState, evt *domain.Event) error {
	var payload domain.ExecutionCreatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal execution.created payload: %w", err)
	}
	state.Execution.ID = evt.ExecutionID
	state.Execution.AgentID = payload.AgentID
	state.Execution.Input = payload.Input
	state.Execution.Labels = payload.Labels
	state.Execution.Status = domain.ExecutionPending
	state.Execution.CreatedAt = evt.Timestamp
	state.Execution.UpdatedAt = evt.Timestamp
	state.AgentID = payload.AgentID
	return nil
}

func applyExecutionStarted(state *domain.ExecutionState, evt *domain.Event) error {
	state.Execution.Status = domain.ExecutionRunning
	state.Execution.UpdatedAt = evt.Timestamp
	return nil
}

func applyExecutionBlocked(state *domain.ExecutionState, evt *domain.Event) error {
	var payload domain.ExecutionBlockedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal execution.blocked payload: %w", err)
	}
	state.Execution.Status = domain.ExecutionBlocked
	state.Execution.UpdatedAt = evt.Timestamp
	state.BlockedReason = payload.Reason
	state.BlockedRef = payload.Ref
	if payload.Reason == "approval" {
		state.PendingApproval = &domain.PendingApproval{
			StepID:    payload.Ref,
			ToolID:    payload.ToolID,
			Arguments: payload.Arguments,
			Remote:    payload.Remote,
		}
	} else {
		state.PendingApproval = nil
	}
	return nil
}

func applyExecutionResumed(state *domain.ExecutionState, evt *domain.Event) error {
	state.Execution.Status = domain.ExecutionRunning
	state.Execution.UpdatedAt = evt.Timestamp
	state.BlockedReason = ""
	state.BlockedRef = ""
	state.PendingApproval = nil
	return nil
}

func applyExecutionCompleted(state *domain.ExecutionState, evt *domain.Event) error {
	var payload domain.ExecutionCompletedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal execution.completed payload: %w", err)
	}
	state.Execution.Status = domain.ExecutionCompleted
	state.Execution.Output = payload.Output
	state.Execution.UpdatedAt = evt.Timestamp
	return nil
}

func applyExecutionFailed(state *domain.ExecutionState, evt *domain.Event) error {
	state.Execution.Status = domain.ExecutionFailed
	state.Execution.UpdatedAt = evt.Timestamp
	return nil
}

func applyExecutionCancelled(state *domain.ExecutionState, evt *domain.Event) error {
	state.Execution.Status = domain.ExecutionCancelled
	state.Execution.UpdatedAt = evt.Timestamp
	return nil
}

func applyExecutionReset(state *domain.ExecutionState, evt *domain.Event) error {
	state.Execution.Status = domain.ExecutionPending
	state.Execution.UpdatedAt = evt.Timestamp
	state.BlockedReason = ""
	state.BlockedRef = ""
	return nil
}
