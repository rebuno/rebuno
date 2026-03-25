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

func (k *Kernel) ProcessIntent(ctx context.Context, req domain.IntentRequest) (domain.IntentResult, error) {
	release, err := k.locker.Acquire(ctx, "execution:"+req.ExecutionID)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("acquiring lock for %s: %w", req.ExecutionID, err)
	}
	defer release()

	session, err := k.validateSession(ctx, req.SessionID, req.ExecutionID)
	if err != nil {
		return domain.IntentResult{}, err
	}

	if err := k.sessions.Extend(ctx, req.SessionID, k.config.AgentTimeout); err != nil {
		k.logger.Warn("failed to extend session",
			slog.String("session_id", req.SessionID),
			slog.String("error", err.Error()),
		)
	}

	state, err := k.projector.Project(ctx, req.ExecutionID)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("projecting execution %s: %w", req.ExecutionID, err)
	}

	if state.Tainted {
		return domain.IntentResult{}, fmt.Errorf("%w: %s", domain.ErrExecutionTainted, state.TaintedReason)
	}

	if state.Execution.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: execution %s is already %s",
			domain.ErrTerminalExecution, req.ExecutionID, state.Execution.Status)
	}

	if err := validateIntent(req.Intent); err != nil {
		return domain.IntentResult{}, err
	}

	var policyTimeoutMs int64
	if req.Intent.Type == domain.IntentInvokeTool {
		policyInput := domain.PolicyInput{
			Action:      "tool.invoke",
			ToolID:      req.Intent.ToolID,
			Labels:      state.Execution.Labels,
			ExecutionID: req.ExecutionID,
			AgentID:     session.AgentID,
			Arguments:   req.Intent.Arguments,
			StepCount:   len(state.Steps),
			DurationMs:  time.Since(state.Execution.CreatedAt).Milliseconds(),
		}

		policyStart := time.Now()
		result, err := k.policy.Evaluate(ctx, policyInput)
		if k.metrics != nil {
			k.metrics.PolicyEvalDuration.WithLabelValues("tool.invoke").Observe(time.Since(policyStart).Seconds())
		}
		if err != nil {
			return domain.IntentResult{}, fmt.Errorf("evaluating policy: %w", err)
		}

		if result.Decision == domain.PolicyDeny {
			deniedPayload := domain.IntentDeniedPayload{
				IntentType:     string(req.Intent.Type),
				ToolID:         req.Intent.ToolID,
				Arguments:      req.Intent.Arguments,
				IdempotencyKey: req.Intent.IdempotencyKey,
				Reason:         result.Reason,
				RuleID:         result.RuleID,
			}
			_, _ = k.EmitEvent(ctx, req.ExecutionID, "", domain.EventIntentDenied,
				deniedPayload, uuid.Nil, uuid.Nil)

			if k.metrics != nil {
				k.metrics.IntentsTotal.WithLabelValues(string(req.Intent.Type), "denied").Inc()
			}

			return domain.IntentResult{
				Accepted: false,
				Error:    fmt.Sprintf("policy denied: %s (rule: %s)", result.Reason, result.RuleID),
			}, nil
		}

		if result.Decision == domain.PolicyRequireApproval {
			return k.executeRequireApproval(ctx, req, state, result.Reason)
		}
		policyTimeoutMs = result.TimeoutMs
	}

	intentDetails := k.buildIntentDetails(req.Intent)
	acceptedPayload := domain.IntentAcceptedPayload{
		IntentType: string(req.Intent.Type),
		Details:    intentDetails,
	}
	acceptedEvt, err := k.EmitEvent(ctx, req.ExecutionID, "", domain.EventIntentAccepted,
		acceptedPayload, uuid.Nil, uuid.Nil)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("emitting intent.accepted: %w", err)
	}
	correlationID := acceptedEvt.CorrelationID

	if k.metrics != nil {
		k.metrics.IntentsTotal.WithLabelValues(string(req.Intent.Type), "accepted").Inc()
	}

	switch req.Intent.Type {
	case domain.IntentInvokeTool:
		return k.executeInvokeTool(ctx, req, state, correlationID, policyTimeoutMs)
	case domain.IntentComplete:
		return k.executeComplete(ctx, req, state, correlationID)
	case domain.IntentFail:
		return k.executeFail(ctx, req, state, correlationID)
	case domain.IntentWait:
		return k.executeWait(ctx, req, state, correlationID)
	default:
		return domain.IntentResult{}, fmt.Errorf("%w: unknown intent type %s",
			domain.ErrValidation, req.Intent.Type)
	}
}

func (k *Kernel) executeInvokeTool(
	ctx context.Context,
	req domain.IntentRequest,
	state *domain.ExecutionState,
	correlationID uuid.UUID,
	policyTimeoutMs int64,
) (domain.IntentResult, error) {
	if state.CurrentStep != nil && !state.CurrentStep.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: a tool is already in progress (step %s), cannot invoke another",
			domain.ErrConflict, state.CurrentStep.ID)
	}

	stepID := uuid.Must(uuid.NewV7()).String()
	stepTimeout := k.config.StepTimeout
	if policyTimeoutMs > 0 {
		stepTimeout = time.Duration(policyTimeoutMs) * time.Millisecond
	}
	deadline := time.Now().Add(stepTimeout)

	stepCreatedPayload := domain.StepCreatedPayload{
		ToolID:      req.Intent.ToolID,
		Arguments:   req.Intent.Arguments,
		MaxAttempts: 1,
		Attempt:     1,
	}

	if req.Intent.Remote {
		jobID := uuid.Must(uuid.NewV7())
		stepCreatedPayload.MaxAttempts = 3

		job := domain.Job{
			ID:          jobID,
			ExecutionID: req.ExecutionID,
			StepID:      stepID,
			Attempt:     1,
			ToolID:      req.Intent.ToolID,
			Arguments:   req.Intent.Arguments,
			Deadline:    deadline,
		}

		stepDispatchedPayload := domain.StepDispatchedPayload{
			RunnerID: "",
			JobID:    jobID.String(),
			Deadline: deadline,
		}

		_, err := k.EmitEvents(ctx, req.ExecutionID, correlationID, []eventEntry{
			{stepID: stepID, eventType: domain.EventStepCreated, payload: stepCreatedPayload},
			{stepID: stepID, eventType: domain.EventStepDispatched, payload: stepDispatchedPayload},
		})
		if err != nil {
			return domain.IntentResult{}, fmt.Errorf("emitting step events: %w", err)
		}

		payload, err := json.Marshal(job)
		if err != nil {
			return domain.IntentResult{}, fmt.Errorf("marshaling job: %w", err)
		}
		msg := store.RunnerMessage{Type: "job.assigned", Payload: payload}
		info, dispatched := k.runnerHub.Dispatch(req.Intent.ToolID, msg)
		if dispatched {
			k.runnerHub.MarkBusy(info.RunnerID, info.ConsumerID)
		} else {
			k.enqueuePendingJob(job)
		}
	} else {
		_, err := k.EmitEvent(ctx, req.ExecutionID, stepID, domain.EventStepCreated,
			stepCreatedPayload, uuid.Nil, correlationID)
		if err != nil {
			return domain.IntentResult{}, fmt.Errorf("emitting step.created: %w", err)
		}
	}

	updated, err := k.projector.Project(ctx, req.ExecutionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventStepCreated)
	}

	return domain.IntentResult{
		Accepted: true,
		StepID:   stepID,
	}, nil
}

func (k *Kernel) executeRequireApproval(
	ctx context.Context,
	req domain.IntentRequest,
	state *domain.ExecutionState,
	reason string,
) (domain.IntentResult, error) {
	if state.CurrentStep != nil && !state.CurrentStep.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: a tool is already in progress (step %s), cannot invoke another",
			domain.ErrConflict, state.CurrentStep.ID)
	}

	stepID := uuid.Must(uuid.NewV7()).String()

	stepCreatedPayload := domain.StepCreatedPayload{
		ToolID:      req.Intent.ToolID,
		Arguments:   req.Intent.Arguments,
		MaxAttempts: 1,
		Attempt:     1,
	}

	approvalPayload := domain.StepApprovalRequiredPayload{
		ToolID:    req.Intent.ToolID,
		Arguments: req.Intent.Arguments,
		Remote:    req.Intent.Remote,
		Reason:    reason,
	}

	blockedPayload := domain.ExecutionBlockedPayload{
		Reason:    "approval",
		Ref:       stepID,
		ToolID:    req.Intent.ToolID,
		Arguments: req.Intent.Arguments,
		Remote:    req.Intent.Remote,
	}

	_, err := k.EmitEvents(ctx, req.ExecutionID, uuid.Nil, []eventEntry{
		{stepID: stepID, eventType: domain.EventStepCreated, payload: stepCreatedPayload},
		{stepID: stepID, eventType: domain.EventStepApprovalRequired, payload: approvalPayload},
		{stepID: "", eventType: domain.EventExecutionBlocked, payload: blockedPayload},
	})
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("emitting approval events: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, req.ExecutionID, domain.ExecutionBlocked); err != nil {
		k.logger.Warn("failed to update execution status to blocked",
			slog.String("execution_id", req.ExecutionID),
			slog.String("error", err.Error()),
		)
	}

	if k.metrics != nil {
		k.metrics.IntentsTotal.WithLabelValues(string(req.Intent.Type), "pending_approval").Inc()
	}

	updated, err := k.projector.Project(ctx, req.ExecutionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionBlocked)
	}

	return domain.IntentResult{
		Accepted:        true,
		StepID:          stepID,
		PendingApproval: true,
	}, nil
}

func (k *Kernel) executeComplete(
	ctx context.Context,
	req domain.IntentRequest,
	state *domain.ExecutionState,
	correlationID uuid.UUID,
) (domain.IntentResult, error) {
	if state.Execution.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: execution %s is already %s",
			domain.ErrTerminalExecution, req.ExecutionID, state.Execution.Status)
	}

	payload := domain.ExecutionCompletedPayload{
		Output: req.Intent.Output,
	}

	_, err := k.EmitEvent(ctx, req.ExecutionID, "", domain.EventExecutionCompleted,
		payload, uuid.Nil, correlationID)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("emitting execution.completed: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, req.ExecutionID, domain.ExecutionCompleted); err != nil {
		k.logger.Warn("failed to update execution status to completed",
			slog.String("execution_id", req.ExecutionID),
			slog.String("error", err.Error()),
		)
	}

	if k.metrics != nil {
		k.metrics.ExecutionsTotal.WithLabelValues("completed").Inc()
		k.metrics.ActiveExecutions.Dec()
	}

	updated, err := k.projector.Project(ctx, req.ExecutionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionCompleted)
	}

	return domain.IntentResult{Accepted: true}, nil
}

func (k *Kernel) executeFail(
	ctx context.Context,
	req domain.IntentRequest,
	state *domain.ExecutionState,
	correlationID uuid.UUID,
) (domain.IntentResult, error) {
	if state.Execution.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: execution %s is already %s",
			domain.ErrTerminalExecution, req.ExecutionID, state.Execution.Status)
	}

	payload := domain.ExecutionFailedPayload{
		Error: req.Intent.Error,
	}

	_, err := k.EmitEvent(ctx, req.ExecutionID, "", domain.EventExecutionFailed,
		payload, uuid.Nil, correlationID)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("emitting execution.failed: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, req.ExecutionID, domain.ExecutionFailed); err != nil {
		k.logger.Warn("failed to update execution status to failed",
			slog.String("execution_id", req.ExecutionID),
			slog.String("error", err.Error()),
		)
	}

	if k.metrics != nil {
		k.metrics.ExecutionsTotal.WithLabelValues("failed").Inc()
		k.metrics.ActiveExecutions.Dec()
	}

	updated, err := k.projector.Project(ctx, req.ExecutionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionFailed)
	}

	return domain.IntentResult{Accepted: true}, nil
}

func (k *Kernel) executeWait(
	ctx context.Context,
	req domain.IntentRequest,
	state *domain.ExecutionState,
	correlationID uuid.UUID,
) (domain.IntentResult, error) {
	if state.Execution.Status.IsTerminal() {
		return domain.IntentResult{}, fmt.Errorf("%w: execution %s is already %s",
			domain.ErrTerminalExecution, req.ExecutionID, state.Execution.Status)
	}

	payload := domain.ExecutionBlockedPayload{
		Reason: "signal",
		Ref:    req.Intent.SignalType,
	}

	_, err := k.EmitEvent(ctx, req.ExecutionID, "", domain.EventExecutionBlocked,
		payload, uuid.Nil, correlationID)
	if err != nil {
		return domain.IntentResult{}, fmt.Errorf("emitting execution.blocked: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, req.ExecutionID, domain.ExecutionBlocked); err != nil {
		k.logger.Warn("failed to update execution status to blocked",
			slog.String("execution_id", req.ExecutionID),
			slog.String("error", err.Error()),
		)
	}

	updated, err := k.projector.Project(ctx, req.ExecutionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionBlocked)
	}

	return domain.IntentResult{Accepted: true}, nil
}

func (k *Kernel) buildIntentDetails(intent domain.Intent) json.RawMessage {
	var details map[string]any

	switch intent.Type {
	case domain.IntentInvokeTool:
		details = map[string]any{"tool_id": intent.ToolID}
		if intent.Arguments != nil {
			details["arguments"] = intent.Arguments
		}
		if intent.IdempotencyKey != "" {
			details["idempotency_key"] = intent.IdempotencyKey
		}
	case domain.IntentComplete:
		if intent.Output != nil {
			details = map[string]any{"output": intent.Output}
		}
	case domain.IntentFail:
		details = map[string]any{"error": intent.Error}
	case domain.IntentWait:
		details = map[string]any{"signal_type": intent.SignalType}
	}

	if len(details) == 0 {
		return nil
	}

	data, err := json.Marshal(details)
	if err != nil {
		k.logger.Error("failed to marshal intent details", slog.String("error", err.Error()))
		return nil
	}
	return data
}

func validateIntent(intent domain.Intent) error {
	switch intent.Type {
	case domain.IntentInvokeTool:
		if intent.ToolID == "" {
			return fmt.Errorf("%w: invoke_tool intent requires tool_id", domain.ErrValidation)
		}
	case domain.IntentComplete:
	case domain.IntentFail:
		if intent.Error == "" {
			return fmt.Errorf("%w: fail intent requires error", domain.ErrValidation)
		}
	case domain.IntentWait:
		if intent.SignalType == "" {
			return fmt.Errorf("%w: wait intent requires signal_type", domain.ErrValidation)
		}
	default:
		return fmt.Errorf("%w: unknown intent type %s", domain.ErrValidation, intent.Type)
	}
	return nil
}
