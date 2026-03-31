package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

type ClaimResult struct {
	ExecutionID string                `json:"execution_id"`
	SessionID   string                `json:"session_id"`
	AgentID     string                `json:"agent_id"`
	Input       json.RawMessage       `json:"input"`
	Labels      map[string]string     `json:"labels,omitempty"`
	History     []domain.HistoryEntry `json:"history,omitempty"`
}

type StepResult struct {
	ExecutionID string            `json:"execution_id"`
	StepID      string            `json:"step_id"`
	Status      domain.StepStatus `json:"status"`
	Result      json.RawMessage   `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
}

func (k *Kernel) buildClaimResult(ctx context.Context, executionID, agentID, consumerID string) (*ClaimResult, error) {
	release, err := k.locker.Acquire(ctx, "execution:"+executionID)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock for %s: %w", executionID, err)
	}
	defer release()

	state, err := k.projector.Project(ctx, executionID)
	if err != nil {
		return nil, fmt.Errorf("projecting execution %s: %w", executionID, err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		return nil, fmt.Errorf("%w: execution %s is %s, not pending",
			domain.ErrConflict, executionID, state.Execution.Status)
	}

	sessionID := uuid.Must(uuid.NewV7()).String()
	session := domain.Session{
		ID:          sessionID,
		ExecutionID: executionID,
		AgentID:     agentID,
		ConsumerID:  consumerID,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(k.config.AgentTimeout),
	}

	if err := k.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	if hub, ok := k.agentHub.(interface {
		SetSession(agentID, consumerID, sessionID string)
	}); ok {
		hub.SetSession(agentID, consumerID, sessionID)
	}

	startedPayload := domain.ExecutionStartedPayload{
		SessionID:  sessionID,
		ConsumerID: consumerID,
	}
	_, err = k.EmitEvent(ctx, executionID, "", domain.EventExecutionStarted,
		startedPayload, uuid.Nil, uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("emitting execution.started: %w", err)
	}

	if err := k.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionRunning); err != nil {
		k.logger.Warn("failed to update execution status to running",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
	}

	history := k.buildFilteredHistory(state)

	updated, err := k.projector.Project(ctx, executionID)
	if err == nil {
		k.maybeCheckpoint(ctx, updated, domain.EventExecutionStarted)
	}

	return &ClaimResult{
		ExecutionID: executionID,
		SessionID:   sessionID,
		AgentID:     agentID,
		Input:       state.Execution.Input,
		Labels:      state.Execution.Labels,
		History:     history,
	}, nil
}

func (k *Kernel) validateSession(ctx context.Context, sessionID, executionID string) (*domain.Session, error) {
	session, found, err := k.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("fetching session %s: %w", sessionID, err)
	}
	if !found {
		return nil, fmt.Errorf("%w: session %s", domain.ErrSessionNotFound, sessionID)
	}
	if session.IsExpired() {
		return nil, fmt.Errorf("%w: session %s", domain.ErrSessionExpired, sessionID)
	}
	if session.ExecutionID != executionID {
		return nil, fmt.Errorf("%w: session %s does not belong to execution %s",
			domain.ErrValidation, sessionID, executionID)
	}
	return session, nil
}

func (k *Kernel) buildFilteredHistory(state *domain.ExecutionState) []domain.HistoryEntry {
	cfg := domain.DefaultSnapshotConfig()
	history := state.History

	if len(history) > cfg.MaxHistoryEntries {
		history = history[len(history)-cfg.MaxHistoryEntries:]
	}

	totalBytes := 0
	for i := len(history) - 1; i >= 0; i-- {
		entryBytes, err := json.Marshal(history[i])
		if err != nil {
			k.logger.Error("failed to marshal history entry", slog.String("error", err.Error()))
			continue
		}
		totalBytes += len(entryBytes)
		if totalBytes > cfg.MaxHistoryBytes {
			history = history[i+1:]
			break
		}
	}

	return history
}
