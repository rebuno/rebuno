package projector

import (
	"encoding/json"
	"fmt"

	"github.com/rebuno/rebuno/internal/domain"
)

func registerStepHandlers(p *Projector) {
	p.Register(domain.EventStepCreated, applyStepCreated)
	p.Register(domain.EventStepDispatched, applyStepDispatched)
	p.Register(domain.EventStepStarted, applyStepStarted)
	p.Register(domain.EventStepCompleted, applyStepCompleted)
	p.Register(domain.EventStepFailed, applyStepFailed)
	p.Register(domain.EventStepTimedOut, applyStepTimedOut)
	p.Register(domain.EventStepCancelled, applyStepCancelled)
	p.Register(domain.EventStepRetried, applyStepRetried)
}

func applyStepCreated(state *domain.ExecutionState, evt *domain.Event) error {
	var payload domain.StepCreatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal step.created payload: %w", err)
	}

	step := &domain.Step{
		ID:          evt.StepID,
		ExecutionID: evt.ExecutionID,
		ToolID:      payload.ToolID,
		ToolVersion: payload.ToolVersion,
		Status:      domain.StepPending,
		Attempt:     payload.Attempt,
		MaxAttempts: payload.MaxAttempts,
		Arguments:   payload.Arguments,
		CreatedAt:   evt.Timestamp,
	}
	if !payload.Deadline.IsZero() {
		step.Deadline = &payload.Deadline
	}
	if state.Steps == nil {
		state.Steps = make(map[string]*domain.Step)
	}
	state.Steps[evt.StepID] = step
	if state.ActiveSteps == nil {
		state.ActiveSteps = make(map[string]*domain.Step)
	}
	state.ActiveSteps[evt.StepID] = step
	return nil
}

func applyStepDispatched(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.dispatched for unknown step %s", evt.StepID)
	}

	var payload domain.StepDispatchedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal step.dispatched payload: %w", err)
	}

	now := evt.Timestamp
	step.Status = domain.StepDispatched
	step.RunnerID = payload.RunnerID
	step.Deadline = &payload.Deadline
	step.DispatchedAt = &now
	return nil
}

func applyStepStarted(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.started for unknown step %s", evt.StepID)
	}
	now := evt.Timestamp
	step.Status = domain.StepRunning
	step.StartedAt = &now
	return nil
}

func applyStepCompleted(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.completed for unknown step %s", evt.StepID)
	}

	var payload domain.StepCompletedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal step.completed payload: %w", err)
	}

	now := evt.Timestamp
	step.Status = domain.StepSucceeded
	step.Result = payload.Result
	step.CompletedAt = &now
	delete(state.ActiveSteps, evt.StepID)

	entry := domain.HistoryEntry{
		StepID:      step.ID,
		ToolID:      step.ToolID,
		Status:      step.Status,
		Arguments:   step.Arguments,
		Result:      step.Result,
		CompletedAt: step.CompletedAt,
	}
	state.History = append(state.History, entry)
	return nil
}

func applyStepFailed(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.failed for unknown step %s", evt.StepID)
	}

	var payload domain.StepFailedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal step.failed payload: %w", err)
	}

	now := evt.Timestamp
	step.Status = domain.StepFailed
	step.Error = payload.Error
	step.Retryable = payload.Retryable
	step.CompletedAt = &now
	delete(state.ActiveSteps, evt.StepID)

	entry := domain.HistoryEntry{
		StepID:      step.ID,
		ToolID:      step.ToolID,
		Status:      step.Status,
		Arguments:   step.Arguments,
		Error:       step.Error,
		CompletedAt: step.CompletedAt,
	}
	state.History = append(state.History, entry)
	return nil
}

func applyStepTimedOut(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.timed_out for unknown step %s", evt.StepID)
	}
	now := evt.Timestamp
	step.Status = domain.StepTimedOut
	step.CompletedAt = &now
	delete(state.ActiveSteps, evt.StepID)

	entry := domain.HistoryEntry{
		StepID:      step.ID,
		ToolID:      step.ToolID,
		Status:      step.Status,
		Arguments:   step.Arguments,
		Error:       "step timed out",
		CompletedAt: step.CompletedAt,
	}
	state.History = append(state.History, entry)
	return nil
}

func applyStepRetried(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.retried for unknown step %s", evt.StepID)
	}

	var payload domain.StepRetriedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal step.retried payload: %w", err)
	}

	step.Status = domain.StepPending
	step.Attempt = payload.NextAttempt
	step.Error = ""
	step.CompletedAt = nil
	step.Retryable = false

	if state.ActiveSteps == nil {
		state.ActiveSteps = make(map[string]*domain.Step)
	}
	state.ActiveSteps[evt.StepID] = step

	return nil
}

func applyStepCancelled(state *domain.ExecutionState, evt *domain.Event) error {
	step := state.Steps[evt.StepID]
	if step == nil {
		return fmt.Errorf("step.cancelled for unknown step %s", evt.StepID)
	}

	reason := "execution cancelled"
	if evt.Payload != nil {
		var payload domain.StepCancelledPayload
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			return fmt.Errorf("unmarshal step.cancelled payload: %w", err)
		}
		if payload.Reason != "" {
			reason = payload.Reason
		}
	}

	now := evt.Timestamp
	step.Status = domain.StepCancelled
	step.CompletedAt = &now
	delete(state.ActiveSteps, evt.StepID)

	entry := domain.HistoryEntry{
		StepID:      step.ID,
		ToolID:      step.ToolID,
		Status:      step.Status,
		Arguments:   step.Arguments,
		Error:       reason,
		CompletedAt: step.CompletedAt,
	}
	state.History = append(state.History, entry)
	return nil
}
