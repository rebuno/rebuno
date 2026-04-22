package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

func (k *Kernel) SendSignal(
	ctx context.Context,
	executionID string,
	signalType string,
	payload json.RawMessage,
) error {
	if executionID == "" {
		return fmt.Errorf("%w: execution_id is required", domain.ErrValidation)
	}
	if signalType == "" {
		return fmt.Errorf("%w: signal_type is required", domain.ErrValidation)
	}

	release, err := k.locker.Acquire(ctx, "execution:"+executionID)
	if err != nil {
		return fmt.Errorf("acquiring lock for %s: %w", executionID, err)
	}
	defer release()

	state, err := k.projector.Project(ctx, executionID)
	if err != nil {
		return fmt.Errorf("projecting execution %s: %w", executionID, err)
	}
	if state.Execution.Status.IsTerminal() {
		return fmt.Errorf("%w: execution %s is in terminal state %s",
			domain.ErrTerminalExecution, executionID, state.Execution.Status)
	}

	signalPayload := domain.SignalReceivedPayload{
		SignalType: signalType,
		Payload:    payload,
	}

	signalEvt, err := k.EmitEvent(ctx, executionID, "", domain.EventSignalReceived,
		signalPayload, uuid.Nil, uuid.Nil)
	if err != nil {
		return fmt.Errorf("emitting signal.received: %w", err)
	}

	sig := domain.Signal{
		ID:          uuid.Must(uuid.NewV7()).String(),
		ExecutionID: executionID,
		SignalType:  signalType,
		Payload:     payload,
		CreatedAt:   time.Now(),
	}
	if err := k.signals.Publish(ctx, executionID, sig); err != nil {
		return fmt.Errorf("publishing signal: %w", err)
	}

	if state.Execution.Status == domain.ExecutionBlocked &&
		state.BlockedReason == "signal" && state.BlockedRef == signalType {
		resumePayload := domain.ExecutionResumedPayload{
			Reason: fmt.Sprintf("signal received: %s", signalType),
		}

		_, err := k.EmitEvent(ctx, executionID, "", domain.EventExecutionResumed,
			resumePayload, signalEvt.ID, signalEvt.CorrelationID)
		if err != nil {
			return fmt.Errorf("emitting execution.resumed: %w", err)
		}

		if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionRunning); err != nil {
			k.logger.Warn("failed to update execution status to running",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}

		updated, err := k.projector.Project(ctx, executionID)
		if err == nil {
			k.maybeCheckpoint(ctx, updated, domain.EventExecutionResumed)
		}

		k.logger.Info("execution resumed by signal",
			slog.String("execution_id", executionID),
			slog.String("signal_type", signalType),
		)
	}

	if state.Execution.Status == domain.ExecutionBlocked &&
		state.BlockedReason == "approval" && signalType == "step.approve" {
		if err := k.handleApprovalSignal(ctx, executionID, state, payload, signalEvt); err != nil {
			return err
		}
	}

	session, found, sessErr := k.sessions.GetByExecution(ctx, executionID)
	if sessErr != nil {
		k.logger.Warn("failed to lookup session for signal delivery",
			slog.String("execution_id", executionID),
			slog.String("error", sessErr.Error()),
		)
	}
	if found {
		signalMsg := map[string]any{"signal_type": signalType, "payload": payload}
		msgPayload, err := json.Marshal(signalMsg)
		if err != nil {
			k.logger.Error("failed to marshal signal message", slog.String("error", err.Error()))
		} else {
			k.agentHub.SendToSession(session.ID, store.AgentMessage{
				Type:    "signal.received",
				Payload: msgPayload,
			})
		}
	}

	return nil
}

func (k *Kernel) handleApprovalSignal(
	ctx context.Context,
	executionID string,
	state *domain.ExecutionState,
	payload json.RawMessage,
	signalEvt domain.Event,
) error {
	var approvalPayload struct {
		StepID   string `json:"step_id"`
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &approvalPayload); err != nil {
		return fmt.Errorf("parsing approval payload: %w", err)
	}

	if approvalPayload.StepID != state.BlockedRef {
		return fmt.Errorf("%w: approval step_id %q does not match blocked ref %q",
			domain.ErrValidation, approvalPayload.StepID, state.BlockedRef)
	}

	stepID := approvalPayload.StepID

	if !approvalPayload.Approved {
		reason := approvalPayload.Reason
		if reason == "" {
			reason = "approval denied by human"
		}
		failPayload := domain.StepFailedPayload{
			Error:     reason,
			Retryable: false,
		}
		resumePayload := domain.ExecutionResumedPayload{
			Reason: "approval denied",
		}
		_, err := k.EmitEvents(ctx, executionID, signalEvt.CorrelationID, []eventEntry{
			{stepID: stepID, eventType: domain.EventStepFailed, payload: failPayload},
			{stepID: "", eventType: domain.EventExecutionResumed, payload: resumePayload},
		})
		if err != nil {
			return fmt.Errorf("emitting denial events: %w", err)
		}

		if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionRunning); err != nil {
			k.logger.Warn("failed to update execution status to running",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}

		k.sendApprovalResolved(ctx, executionID, stepID, false)

		k.logger.Info("approval denied",
			slog.String("execution_id", executionID),
			slog.String("step_id", stepID),
		)
		return nil
	}

	approval := state.PendingApprovals[stepID]
	isRemote := approval != nil && approval.Remote

	if isRemote {
		step := state.Steps[stepID]
		if step == nil {
			return fmt.Errorf("%w: step %s not found in state", domain.ErrNotFound, stepID)
		}

		jobID := uuid.Must(uuid.NewV7())
		deadline := time.Now().Add(k.config.StepTimeout)

		job := domain.Job{
			ID:          jobID,
			ExecutionID: executionID,
			StepID:      stepID,
			Attempt:     step.Attempt,
			ToolID:      step.ToolID,
			Arguments:   step.Arguments,
			Deadline:    deadline,
		}

		dispatchPayload := domain.StepDispatchedPayload{
			RunnerID: "",
			JobID:    jobID.String(),
			Deadline: deadline,
		}
		_, err := k.EmitEvent(ctx, executionID, stepID, domain.EventStepDispatched,
			dispatchPayload, signalEvt.ID, signalEvt.CorrelationID)
		if err != nil {
			return fmt.Errorf("emitting step.dispatched after approval: %w", err)
		}

		jobPayload, err := json.Marshal(job)
		if err != nil {
			return fmt.Errorf("marshaling job: %w", err)
		}
		msg := store.RunnerMessage{Type: "job.assigned", Payload: jobPayload}
		info, dispatched := k.runnerHub.Dispatch(step.ToolID, msg)
		if dispatched {
			k.runnerHub.MarkBusy(info.RunnerID, info.ConsumerID)
		} else {
			k.enqueuePendingJob(job)
		}
	} else {
		deadline := time.Now().Add(k.config.StepTimeout)
		dispatchPayload := domain.StepDispatchedPayload{
			Deadline: deadline,
		}
		_, err := k.EmitEvent(ctx, executionID, stepID, domain.EventStepDispatched,
			dispatchPayload, signalEvt.ID, signalEvt.CorrelationID)
		if err != nil {
			return fmt.Errorf("emitting deadline refresh after approval: %w", err)
		}
	}

	resumePayload := domain.ExecutionResumedPayload{
		Reason: "approval granted",
	}
	_, emitErr := k.EmitEvent(ctx, executionID, "", domain.EventExecutionResumed,
		resumePayload, signalEvt.ID, signalEvt.CorrelationID)
	if emitErr != nil {
		return fmt.Errorf("emitting approval resume: %w", emitErr)
	}

	if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionRunning); err != nil {
		k.logger.Warn("failed to update execution status to running",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
	}

	k.sendApprovalResolved(ctx, executionID, stepID, true)

	// Extend the session TTL since the agent may have been waiting for human approval.
	session, found, sessErr := k.sessions.GetByExecution(ctx, executionID)
	if sessErr == nil && found {
		if err := k.sessions.Extend(ctx, session.ID, k.config.AgentTimeout); err != nil {
			k.logger.Warn("failed to extend session after approval",
				slog.String("session_id", session.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	updated, err := k.projector.Project(ctx, executionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionResumed)
	}

	k.logger.Info("approval granted",
		slog.String("execution_id", executionID),
		slog.String("step_id", stepID),
		slog.Bool("remote", isRemote),
	)
	return nil
}

func (k *Kernel) sendApprovalResolved(ctx context.Context, executionID, stepID string, approved bool) {
	session, found, err := k.sessions.GetByExecution(ctx, executionID)
	if err != nil || !found {
		return
	}

	msg := map[string]any{
		"execution_id": executionID,
		"step_id":      stepID,
		"approved":     approved,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	k.agentHub.SendToSession(session.ID, store.AgentMessage{
		Type:    "approval.resolved",
		Payload: payload,
	})
}
