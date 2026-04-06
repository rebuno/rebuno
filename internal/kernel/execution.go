package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type CreateExecutionRequest struct {
	AgentID string            `json:"agent_id"`
	Input   json.RawMessage   `json:"input"`
	Labels  map[string]string `json:"labels"`
}

func (k *Kernel) CreateExecution(ctx context.Context, req CreateExecutionRequest) (string, error) {
	if req.AgentID == "" {
		return "", fmt.Errorf("%w: agent_id is required", domain.ErrValidation)
	}
	executionID := uuid.Must(uuid.NewV7()).String()

	if err := k.events.CreateExecution(ctx, executionID, req.AgentID, req.Labels); err != nil {
		return "", fmt.Errorf("creating execution row: %w", err)
	}

	payload := domain.ExecutionCreatedPayload{
		AgentID: req.AgentID,
		Input:   req.Input,
		Labels:  req.Labels,
	}

	_, err := k.EmitEvent(ctx, executionID, "", domain.EventExecutionCreated, payload, uuid.Nil, uuid.Nil)
	if err != nil {
		return "", fmt.Errorf("emitting execution.created: %w", err)
	}

	if k.metrics != nil {
		k.metrics.ExecutionsTotal.WithLabelValues("created").Inc()
		k.metrics.ActiveExecutions.Inc()
	}

	k.logger.Info("execution created",
		slog.String("execution_id", executionID),
		slog.String("agent_id", req.AgentID),
	)

	k.tryAssignExecution(ctx, executionID, req.AgentID)

	return executionID, nil
}

func (k *Kernel) tryAssignExecution(ctx context.Context, executionID, agentID string) {
	connInfo, connected := k.agentHub.PickConnection(agentID)
	if !connected {
		k.logger.Debug("no agent connected, execution stays pending",
			slog.String("execution_id", executionID),
			slog.String("agent_id", agentID),
		)
		return
	}

	result, err := k.buildClaimResult(ctx, executionID, agentID, connInfo.ConsumerID)
	if err != nil {
		k.logger.Error("failed to build claim result for assignment",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	payload, err := json.Marshal(result)
	if err != nil {
		k.logger.Error("failed to marshal claim result",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	k.agentHub.SendTo(connInfo.ConsumerID, agentID, store.AgentMessage{
		Type:    "execution.assigned",
		Payload: payload,
	})

	k.logger.Info("execution assigned via SSE",
		slog.String("execution_id", executionID),
		slog.String("agent_id", agentID),
		slog.String("consumer_id", connInfo.ConsumerID),
	)
}

func (k *Kernel) AssignPendingExecutions(ctx context.Context, agentID string) {
	ids, err := k.events.ListActiveExecutionIDs(ctx)
	if err != nil {
		k.logger.Error("failed to list active executions for assignment",
			slog.String("agent_id", agentID),
			slog.String("error", err.Error()),
		)
		return
	}

	for _, execID := range ids {
		state, err := k.projector.Project(ctx, execID)
		if err != nil {
			continue
		}
		if state.AgentID != agentID {
			continue
		}
		if state.Execution.Status != domain.ExecutionPending {
			continue
		}

		_, found, _ := k.sessions.GetByExecution(ctx, execID)
		if found {
			continue
		}

		k.tryAssignExecution(ctx, execID, agentID)
	}
}

func (k *Kernel) GetExecution(ctx context.Context, executionID string) (*domain.ExecutionState, error) {
	state, err := k.projector.Project(ctx, executionID)
	if err != nil {
		return nil, fmt.Errorf("projecting execution %s: %w", executionID, err)
	}

	if state.LastSequence == 0 && state.Execution.Status == "" {
		return nil, fmt.Errorf("%w: execution %s", domain.ErrNotFound, executionID)
	}

	return state, nil
}

func (k *Kernel) CancelExecution(ctx context.Context, executionID string) error {
	release, err := k.locker.Acquire(ctx, "execution:"+executionID)
	if err != nil {
		return fmt.Errorf("acquiring lock for %s: %w", executionID, err)
	}
	defer release()

	state, err := k.projector.Project(ctx, executionID)
	if err != nil {
		return fmt.Errorf("projecting execution %s: %w", executionID, err)
	}

	if state.LastSequence == 0 && state.Execution.Status == "" {
		return fmt.Errorf("%w: execution %s", domain.ErrNotFound, executionID)
	}

	if state.Execution.Status.IsTerminal() {
		return fmt.Errorf("%w: execution %s is already %s",
			domain.ErrTerminalExecution, executionID, state.Execution.Status)
	}

	for stepID, step := range state.Steps {
		if step.Status.IsTerminal() {
			continue
		}
		_, err := k.EmitEvent(ctx, executionID, stepID, domain.EventStepCancelled,
			domain.StepCancelledPayload{Reason: "execution cancelled"}, uuid.Nil, uuid.Nil)
		if err != nil {
			k.logger.Warn("cancel: failed to emit step.cancelled",
				slog.String("execution_id", executionID),
				slog.String("step_id", stepID),
				slog.String("error", err.Error()),
			)
		}
	}

	payload := domain.ExecutionCancelledPayload{
		Reason: "cancelled by request",
	}
	_, err = k.EmitEvent(ctx, executionID, "", domain.EventExecutionCancelled, payload, uuid.Nil, uuid.Nil)
	if err != nil {
		return fmt.Errorf("emitting execution.cancelled: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionCancelled); err != nil {
		return fmt.Errorf("updating execution status: %w", err)
	}

	if k.metrics != nil {
		k.metrics.ExecutionsTotal.WithLabelValues("cancelled").Inc()
		k.metrics.ActiveExecutions.Dec()
	}

	k.logger.Info("execution cancelled", slog.String("execution_id", executionID))
	return nil
}
