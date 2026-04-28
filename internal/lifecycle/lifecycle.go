// Package lifecycle manages background maintenance tasks: session reaping,
// terminal execution cleanup, timeout enforcement, and startup recovery.
package lifecycle

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/store"
)

const (
	sessionReaperInterval  = 30 * time.Second
	timeoutWatcherInterval = 30 * time.Second
	cleanupBatchSize       = 100
)

type EventEmitter interface {
	EmitEvent(
		ctx context.Context,
		executionID string,
		stepID string,
		eventType domain.EventType,
		payload any,
		causationID uuid.UUID,
		correlationID uuid.UUID,
	) (domain.Event, error)
}

type Deps struct {
	Events           store.EventStore
	Sessions         store.SessionStore
	Checkpoints      store.CheckpointStore
	Signals          store.SignalStore
	AgentHub         store.AgentHub
	Locker           store.Locker
	Projector        *projector.Projector
	Emitter          EventEmitter
	Logger           *slog.Logger
	Metrics          *observe.Metrics
	ExecutionTimeout time.Duration
}

type Manager struct {
	events           store.EventStore
	sessions         store.SessionStore
	checkpoints      store.CheckpointStore
	signals          store.SignalStore
	agentHub         store.AgentHub
	locker           store.Locker
	projector        *projector.Projector
	emitter          EventEmitter
	logger           *slog.Logger
	metrics          *observe.Metrics
	executionTimeout time.Duration
}

func NewManager(d Deps) *Manager {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		events:           d.Events,
		sessions:         d.Sessions,
		checkpoints:      d.Checkpoints,
		signals:          d.Signals,
		agentHub:         d.AgentHub,
		locker:           d.Locker,
		projector:        d.Projector,
		emitter:          d.Emitter,
		logger:           logger,
		metrics:          d.Metrics,
		executionTimeout: d.ExecutionTimeout,
	}
}

func (m *Manager) StartSessionReaper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(sessionReaperInterval)
		defer ticker.Stop()

		m.logger.Info("session reaper started", "interval", sessionReaperInterval)

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("session reaper stopped")
				return
			case <-ticker.C:
				m.reapSessions(ctx)
			}
		}
	}()
}

func (m *Manager) reapSessions(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	deleted, err := m.sessions.DeleteExpired(ctx, sessionReaperInterval)
	if err != nil {
		m.logger.Error("session reaper: failed to delete expired sessions",
			slog.String("error", err.Error()),
		)
		return
	}

	if deleted > 0 {
		m.logger.Info("session reaper: deleted expired sessions", slog.Int("count", deleted))
	}

	activeIDs, err := m.events.ListActiveExecutionIDs(ctx)
	if err != nil {
		m.logger.Error("session reaper: failed to list active executions",
			slog.String("error", err.Error()),
		)
		return
	}

	for _, execID := range activeIDs {
		_, found, err := m.sessions.GetByExecution(ctx, execID)
		if err != nil {
			m.logger.Warn("session reaper: failed to check session for execution",
				slog.String("execution_id", execID),
				slog.String("error", err.Error()),
			)
			continue
		}

		if found {
			continue
		}

		// Check if the agent still has a live SSE connection before treating
		// as orphaned. The session may have expired while blocked for approval
		// but the agent is still connected and will resume once unblocked.
		state, err := m.projector.Project(ctx, execID)
		if err != nil {
			m.logger.Warn("session reaper: failed to project execution",
				slog.String("execution_id", execID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if state.Execution.Status.IsTerminal() {
			continue
		}
		if state.Execution.Status == domain.ExecutionPending {
			if m.agentHub.HasConnections(state.AgentID) {
				m.logger.Info("session reaper: pending execution has connected agent, notifying",
					slog.String("execution_id", execID),
					slog.String("agent_id", state.AgentID),
				)
				m.notifyPendingExecution(execID, state.AgentID)
			}
			continue
		}
		if m.agentHub.HasConnections(state.AgentID) {
			m.logger.Debug("session reaper: agent still connected, skipping",
				slog.String("execution_id", execID),
				slog.String("agent_id", state.AgentID),
			)
			continue
		}

		m.handleOrphanedExecution(ctx, execID)
	}
}

func (m *Manager) notifyPendingExecution(executionID, agentID string) {
	claimPayload := map[string]string{
		"execution_id": executionID,
		"agent_id":     agentID,
	}
	payload, err := json.Marshal(claimPayload)
	if err != nil {
		m.logger.Error("failed to marshal pending notification payload",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	m.agentHub.Send(agentID, store.AgentMessage{
		Type:    "execution.pending",
		Payload: payload,
	})
}

func (m *Manager) handleOrphanedExecution(ctx context.Context, executionID string) {
	release, err := m.acquireLock(ctx, executionID)
	if err != nil {
		m.logger.Warn("session reaper: failed to acquire lock",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	defer release()

	state, err := m.projector.Project(ctx, executionID)
	if err != nil {
		m.logger.Warn("session reaper: failed to project orphaned execution",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	if state.Execution.Status.IsTerminal() || state.Execution.Status == domain.ExecutionPending {
		return
	}

	m.reassignIfNeeded(ctx, executionID, state.AgentID, "session reaper")
}

func (m *Manager) reassignIfNeeded(ctx context.Context, executionID, agentID, caller string) bool {
	state, err := m.projector.Project(ctx, executionID)
	if err != nil {
		m.logger.Warn(caller+": failed to project execution",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return false
	}

	switch state.Execution.Status {
	case domain.ExecutionRunning, domain.ExecutionBlocked:
		for stepID, step := range state.Steps {
			if step.Status.IsTerminal() {
				continue
			}
			if _, err := m.emitter.EmitEvent(ctx, executionID, stepID, domain.EventStepCancelled,
				domain.StepCancelledPayload{Reason: "agent disconnected"}, uuid.Nil, uuid.Nil); err != nil {
				m.logger.Warn(caller+": failed to cancel orphaned step",
					slog.String("execution_id", executionID),
					slog.String("step_id", stepID),
					slog.String("error", err.Error()),
				)
			}
		}
		if _, err := m.emitter.EmitEvent(ctx, executionID, "", domain.EventAgentTimeout,
			domain.AgentTimeoutPayload{SessionID: ""}, uuid.Nil, uuid.Nil); err != nil {
			m.logger.Warn(caller+": failed to emit agent.timeout for reassignment",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}
		if _, err := m.emitter.EmitEvent(ctx, executionID, "", domain.EventExecutionReset,
			domain.ExecutionResetPayload{
				Reason:     "recovery",
				FromStatus: string(state.Execution.Status),
			}, uuid.Nil, uuid.Nil); err != nil {
			m.logger.Warn(caller+": failed to emit execution.reset",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
		}
		if err := m.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionPending); err != nil {
			m.logger.Warn(caller+": failed to reset execution to pending",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
			return false
		}
	case domain.ExecutionPending:
	default:
		return false
	}

	if m.agentHub.HasConnections(agentID) {
		claimPayload := map[string]string{
			"execution_id": executionID,
			"agent_id":     agentID,
		}
		payload, err := json.Marshal(claimPayload)
		if err != nil {
			m.logger.Error("failed to marshal claim payload",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
			return false
		}
		if m.agentHub.Send(agentID, store.AgentMessage{
			Type:    "execution.pending",
			Payload: payload,
		}) {
			m.logger.Info(caller+": notified agent of pending execution via hub",
				slog.String("execution_id", executionID),
				slog.String("agent_id", agentID),
			)
			return true
		}
	}

	m.logger.Info(caller+": no agent connected, execution stays pending",
		slog.String("execution_id", executionID),
		slog.String("agent_id", agentID),
	)
	return false
}

func (m *Manager) StartTimeoutWatcher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(timeoutWatcherInterval)
		defer ticker.Stop()

		m.logger.Info("timeout watcher started", "interval", timeoutWatcherInterval)

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("timeout watcher stopped")
				return
			case <-ticker.C:
				m.checkTimeouts(ctx)
			}
		}
	}()
}

func (m *Manager) checkTimeouts(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	activeIDs, err := m.events.ListActiveExecutionIDs(ctx)
	if err != nil {
		m.logger.Error("timeout watcher: failed to list active executions",
			slog.String("error", err.Error()),
		)
		return
	}

	now := time.Now()
	for _, execID := range activeIDs {
		state, err := m.projector.Project(ctx, execID)
		if err != nil {
			m.logger.Warn("timeout watcher: failed to project execution",
				slog.String("execution_id", execID),
				slog.String("error", err.Error()),
			)
			continue
		}

		if state.Execution.Status.IsTerminal() {
			continue
		}

		stepTimedOut := false
		for stepID, step := range state.Steps {
			if step.Status.IsTerminal() {
				continue
			}
			if step.Deadline != nil && now.After(*step.Deadline) {
				m.failStepTimeout(ctx, execID, stepID)
				stepTimedOut = true
			}
		}
		if stepTimedOut {
			continue
		}

		if m.executionTimeout > 0 && !state.Execution.CreatedAt.IsZero() {
			deadline := state.Execution.CreatedAt.Add(m.executionTimeout)
			if now.After(deadline) {
				m.failExecutionTimeout(ctx, execID)
			}
		}
	}
}

func (m *Manager) recoverExecution(ctx context.Context, executionID, agentID string) bool {
	release, err := m.acquireLock(ctx, executionID)
	if err != nil {
		m.logger.Warn("recovery: failed to acquire lock",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return false
	}
	defer release()

	return m.reassignIfNeeded(ctx, executionID, agentID, "recovery")
}

func (m *Manager) acquireLock(ctx context.Context, executionID string) (func(), error) {
	if m.locker == nil {
		return func() {}, nil
	}
	return m.locker.Acquire(ctx, "execution:"+executionID)
}

func (m *Manager) failStepTimeout(ctx context.Context, executionID, stepID string) {
	release, err := m.acquireLock(ctx, executionID)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to acquire lock",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	defer release()

	state, err := m.projector.Project(ctx, executionID)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to re-project after lock",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	if state.Execution.Status.IsTerminal() {
		return
	}
	step, ok := state.Steps[stepID]
	if !ok || step.Status.IsTerminal() {
		return
	}

	_, err = m.emitter.EmitEvent(ctx, executionID, stepID, domain.EventStepTimedOut,
		domain.StepTimedOutPayload{}, uuid.Nil, uuid.Nil)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to emit step.timed_out",
			slog.String("execution_id", executionID),
			slog.String("step_id", stepID),
			slog.String("error", err.Error()),
		)
		return
	}

	for otherID, otherStep := range state.Steps {
		if otherID == stepID || otherStep.Status.IsTerminal() {
			continue
		}
		if _, err := m.emitter.EmitEvent(ctx, executionID, otherID, domain.EventStepCancelled,
			domain.StepCancelledPayload{Reason: "sibling step timed out"}, uuid.Nil, uuid.Nil); err != nil {
			m.logger.Warn("timeout watcher: failed to cancel sibling step",
				slog.String("execution_id", executionID),
				slog.String("step_id", otherID),
				slog.String("error", err.Error()),
			)
		}
	}

	_, err = m.emitter.EmitEvent(ctx, executionID, "", domain.EventExecutionFailed,
		domain.ExecutionFailedPayload{Error: "step timed out"}, uuid.Nil, uuid.Nil)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to emit execution.failed",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	if err := m.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionFailed); err != nil {
		m.logger.Warn("timeout watcher: failed to update execution status",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
	}

	if m.metrics != nil {
		m.metrics.ActiveExecutions.Dec()
	}

	m.logger.Info("timeout watcher: step timed out, execution failed",
		slog.String("execution_id", executionID),
		slog.String("step_id", stepID),
	)
}

func (m *Manager) failExecutionTimeout(ctx context.Context, executionID string) {
	release, err := m.acquireLock(ctx, executionID)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to acquire lock for execution timeout",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	defer release()

	state, err := m.projector.Project(ctx, executionID)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to re-project after lock for execution timeout",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	if state.Execution.Status.IsTerminal() {
		return
	}

	for stepID, step := range state.Steps {
		if step.Status.IsTerminal() {
			continue
		}
		if _, err := m.emitter.EmitEvent(ctx, executionID, stepID, domain.EventStepCancelled,
			domain.StepCancelledPayload{Reason: "execution timed out"}, uuid.Nil, uuid.Nil); err != nil {
			m.logger.Warn("timeout watcher: failed to cancel step",
				slog.String("execution_id", executionID),
				slog.String("step_id", stepID),
				slog.String("error", err.Error()),
			)
		}
	}

	_, err = m.emitter.EmitEvent(ctx, executionID, "", domain.EventExecutionFailed,
		domain.ExecutionFailedPayload{Error: "execution exceeded maximum duration"}, uuid.Nil, uuid.Nil)
	if err != nil {
		m.logger.Warn("timeout watcher: failed to emit execution.failed",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}

	if err := m.events.UpdateExecutionStatus(ctx, executionID, domain.ExecutionFailed); err != nil {
		m.logger.Warn("timeout watcher: failed to update execution status",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
	}

	if m.metrics != nil {
		m.metrics.ActiveExecutions.Dec()
	}

	m.logger.Info("timeout watcher: execution timed out",
		slog.String("execution_id", executionID),
	)
}

func (m *Manager) StartCleanup(ctx context.Context, retentionPeriod, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		m.logger.Info("cleanup worker started",
			"retention", retentionPeriod,
			"interval", interval,
		)

		for {
			select {
			case <-ctx.Done():
				m.logger.Info("cleanup worker stopped")
				return
			case <-ticker.C:
				m.cleanupTerminalExecutions(ctx, retentionPeriod)
			}
		}
	}()
}

func (m *Manager) cleanupTerminalExecutions(ctx context.Context, retentionPeriod time.Duration) {
	if ctx.Err() != nil {
		return
	}
	olderThanSecs := int64(retentionPeriod.Seconds())

	ids, err := m.events.ListTerminalExecutions(ctx, olderThanSecs, cleanupBatchSize)
	if err != nil {
		m.logger.Error("cleanup: failed to list terminal executions",
			slog.String("error", err.Error()),
		)
		return
	}

	if len(ids) == 0 {
		return
	}

	deleted := 0
	for _, id := range ids {
		if err := m.events.DeleteExecution(ctx, id); err != nil {
			m.logger.Warn("cleanup: failed to delete execution",
				slog.String("execution_id", id),
				slog.String("error", err.Error()),
			)
			continue
		}

		if err := m.checkpoints.Delete(ctx, id); err != nil {
			m.logger.Warn("cleanup: failed to delete checkpoint",
				slog.String("execution_id", id),
				slog.String("error", err.Error()),
			)
		}

		if err := m.signals.Clear(ctx, id); err != nil {
			m.logger.Warn("cleanup: failed to clear signals",
				slog.String("execution_id", id),
				slog.String("error", err.Error()),
			)
		}

		deleted++
	}

	if deleted > 0 {
		m.logger.Info("cleanup: deleted terminal executions",
			slog.Int("count", deleted),
		)
	}
}

func (m *Manager) RecoverActiveExecutions(ctx context.Context) {
	if purged, err := m.sessions.DeleteAll(ctx); err != nil {
		m.logger.Error("recovery: failed to purge stale sessions",
			slog.String("error", err.Error()),
		)
	} else if purged > 0 {
		m.logger.Info("recovery: purged stale sessions", slog.Int("count", purged))
	}

	ids, err := m.events.ListActiveExecutionIDs(ctx)
	if err != nil {
		m.logger.Error("recovery: failed to list active execution IDs",
			slog.String("error", err.Error()),
		)
		return
	}

	m.logger.Info("recovery: active executions discovered",
		slog.Int("count", len(ids)),
	)

	activeCount := 0
	recovered := 0
	for _, execID := range ids {
		state, err := m.projector.Project(ctx, execID)
		if err != nil {
			m.logger.Warn("recovery: failed to project execution",
				slog.String("execution_id", execID),
				slog.String("error", err.Error()),
			)
			continue
		}

		if state.Execution.Status.IsTerminal() {
			if err := m.events.UpdateExecutionStatus(ctx, execID, state.Execution.Status); err != nil {
				m.logger.Warn("recovery: failed to reconcile terminal status",
					slog.String("execution_id", execID),
					slog.String("status", string(state.Execution.Status)),
					slog.String("error", err.Error()),
				)
			}
			continue
		}

		activeCount++

		if m.recoverExecution(ctx, execID, state.AgentID) {
			recovered++
		}
	}

	if m.metrics != nil {
		m.metrics.ActiveExecutions.Set(float64(activeCount))
	}

	if recovered > 0 {
		m.logger.Info("recovery: reassigned orphaned executions",
			slog.Int("count", recovered),
		)
	}
}
