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

type StepResultRequest struct {
	ExecutionID string          `json:"execution_id"`
	SessionID   string          `json:"session_id"`
	StepID      string          `json:"step_id"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func (k *Kernel) SubmitStepResult(ctx context.Context, req StepResultRequest) error {
	release, err := k.locker.Acquire(ctx, "execution:"+req.ExecutionID)
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer release()

	if _, err := k.validateSession(ctx, req.SessionID, req.ExecutionID); err != nil {
		return err
	}

	if err := k.sessions.Extend(ctx, req.SessionID, k.config.AgentTimeout); err != nil {
		k.logger.Warn("failed to extend session",
			slog.String("session_id", req.SessionID),
			slog.String("error", err.Error()),
		)
	}

	state, err := k.projector.Project(ctx, req.ExecutionID)
	if err != nil {
		return fmt.Errorf("projecting execution %s: %w", req.ExecutionID, err)
	}
	if state.Tainted {
		return domain.ErrExecutionTainted
	}

	step, ok := state.Steps[req.StepID]
	if !ok {
		return fmt.Errorf("%w: step %s", domain.ErrNotFound, req.StepID)
	}
	if step.Status.IsTerminal() {
		return domain.ErrStepAlreadyResolved
	}

	correlationID := uuid.Must(uuid.NewV7())

	var stepStatus domain.StepStatus
	if req.Success {
		_, err := k.EmitEvent(ctx, req.ExecutionID, req.StepID,
			domain.EventStepCompleted,
			domain.StepCompletedPayload{Result: req.Data},
			uuid.Nil, correlationID)
		if err != nil {
			return err
		}
		stepStatus = domain.StepSucceeded

		if k.metrics != nil {
			k.metrics.StepDuration.WithLabelValues(step.ToolID).Observe(time.Since(step.CreatedAt).Seconds())
		}
	} else {
		_, err := k.EmitEvent(ctx, req.ExecutionID, req.StepID,
			domain.EventStepFailed,
			domain.StepFailedPayload{Error: req.Error, Retryable: false},
			uuid.Nil, correlationID)
		if err != nil {
			return err
		}
		stepStatus = domain.StepFailed
	}

	session, found, sessErr := k.sessions.GetByExecution(ctx, req.ExecutionID)
	if sessErr != nil {
		k.logger.Warn("failed to lookup session for step result delivery",
			slog.String("execution_id", req.ExecutionID),
			slog.String("error", sessErr.Error()),
		)
	}
	if found {
		result := StepResult{
			ExecutionID: req.ExecutionID,
			StepID:      req.StepID,
			Status:      stepStatus,
			Result:      req.Data,
			Error:       req.Error,
		}
		payload, err := json.Marshal(result)
		if err != nil {
			k.logger.Error("failed to marshal step result", slog.String("error", err.Error()))
			return err
		}
		k.agentHub.SendToSession(session.ID, store.AgentMessage{
			Type:    "tool.result",
			Payload: payload,
		})
	}

	return nil
}
