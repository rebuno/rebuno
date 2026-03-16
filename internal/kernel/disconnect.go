package kernel

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

func (k *Kernel) HandleAgentDisconnect(ctx context.Context, sessionID string) {
	session, found, err := k.sessions.Get(ctx, sessionID)
	if err != nil {
		k.logger.Warn("disconnect: failed to get session",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return
	}
	if !found {
		return
	}

	executionID := session.ExecutionID

	release, err := k.locker.Acquire(ctx, "execution:"+executionID)
	if err != nil {
		k.logger.Warn("disconnect: failed to acquire execution lock",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	defer release()

	if err := k.sessions.Delete(ctx, sessionID); err != nil {
		k.logger.Warn("disconnect: failed to delete session",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
	}

	state, err := k.projector.Project(ctx, executionID)
	if err != nil {
		k.logger.Warn("disconnect: failed to project execution",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	if state.Execution.Status.IsTerminal() {
		return
	}

	correlationID := uuid.Must(uuid.NewV7())

	_, err = k.EmitEvent(ctx, executionID, "", domain.EventAgentTimeout,
		domain.AgentTimeoutPayload{SessionID: sessionID}, uuid.Nil, correlationID)
	if err != nil {
		k.logger.Warn("disconnect: failed to emit agent.timeout",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
	}

	switch state.Execution.Status {
	case domain.ExecutionRunning:
		_, err = k.EmitEvent(ctx, executionID, "", domain.EventExecutionReset,
			domain.ExecutionResetPayload{
				Reason:     "agent_disconnect",
				FromStatus: string(state.Execution.Status),
			}, uuid.Nil, correlationID)
		if err != nil {
			k.logger.Warn("disconnect: failed to emit execution.reset",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}
		if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionPending); err != nil {
			k.logger.Warn("disconnect: failed to reset execution to pending",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}
		k.logger.Info("disconnect: execution reset to pending, awaiting reassignment",
			slog.String("execution_id", executionID),
		)
	case domain.ExecutionBlocked:
		k.logger.Info("disconnect: execution is blocked, leaving as-is",
			slog.String("execution_id", executionID),
		)
	default:
		k.logger.Debug("disconnect: execution in state, no action needed",
			slog.String("execution_id", executionID),
			slog.String("status", string(state.Execution.Status)),
		)
	}
}
