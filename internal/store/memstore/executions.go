package memstore

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listExecutionsLocked(filter), nil
}

func (tx *txStore) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	return tx.listExecutionsLocked(filter), nil
}

func (s *Store) listExecutionsLocked(filter domain.ExecutionFilter) domain.ExecutionPage {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	var matched []domain.Execution
	for _, e := range s.executions {
		if filter.AgentID != "" && e.AgentID != filter.AgentID {
			continue
		}
		if filter.Session != "" && e.Session != filter.Session {
			continue
		}
		if filter.Status != "" && e.Status != filter.Status {
			continue
		}
		if filter.Cursor != "" && e.ID.String() >= filter.Cursor {
			continue
		}
		matched = append(matched, e)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID.String() > matched[j].ID.String() })

	var page domain.ExecutionPage
	if len(matched) > limit {
		matched = matched[:limit]
		page.NextCursor = matched[limit-1].ID.String()
	}
	page.Executions = matched
	return page
}

func (s *Store) CreateExecution(ctx context.Context, exec domain.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createExecutionLocked(ctx, exec)
}

func (s *Store) createExecutionLocked(ctx context.Context, exec domain.Execution) error {
	if _, ok := s.executions[exec.ID]; ok {
		return domain.ErrConflict
	}
	exec.Status = domain.ExecutionPending
	if exec.CreatedAt.IsZero() {
		exec.CreatedAt = time.Now().UTC()
	}
	exec.UpdatedAt = exec.CreatedAt
	s.executions[exec.ID] = exec
	return nil
}

func (s *Store) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getExecutionLocked(ctx, id)
}

func (s *Store) getExecutionLocked(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	exec, ok := s.executions[id]
	if !ok {
		return domain.Execution{}, domain.ErrNotFound
	}
	return exec, nil
}

func (s *Store) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateExecutionStatusLocked(ctx, id, status, output, reason)
}

func (s *Store) updateExecutionStatusLocked(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	exec, ok := s.executions[id]
	if !ok {
		return domain.ErrNotFound
	}
	if exec.Status.IsTerminal() {
		return domain.ErrExecutionTerminal
	}
	if exec.Session != "" && (status == domain.ExecutionRunning || status == domain.ExecutionBlocked) {
		for _, other := range s.executions {
			if other.ID != id && other.Session == exec.Session &&
				(other.Status == domain.ExecutionRunning || other.Status == domain.ExecutionBlocked) {
				return domain.ErrConflict
			}
		}
	}
	exec.Status = status
	if len(output) > 0 {
		exec.Output = output
	}
	if reason != "" {
		exec.FailureReason = reason
	}
	exec.UpdatedAt = time.Now().UTC()
	s.executions[id] = exec
	return nil
}

func (s *Store) ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listExpiredExecutionsLocked(ctx, now)
}

func (s *Store) listExpiredExecutionsLocked(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	var out []domain.Execution
	for _, exec := range s.executions {
		if exec.DeadlineAt == nil {
			continue
		}
		switch exec.Status {
		case domain.ExecutionPending, domain.ExecutionRunning, domain.ExecutionBlocked:
			if !exec.DeadlineAt.After(now) {
				out = append(out, exec)
			}
		}
	}
	return out, nil
}

func (s *Store) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteExecutionsCreatedBeforeLocked(ctx, before)
	return nil
}

func (s *Store) deleteExecutionsCreatedBeforeLocked(ctx context.Context, before time.Time) {
	kept := make(map[string]bool)
	for _, exec := range s.executions {
		if exec.Session != "" && (!exec.Status.IsTerminal() || !exec.CreatedAt.Before(before)) {
			kept[exec.Session] = true
		}
	}
	for id, exec := range s.executions {
		if !exec.Status.IsTerminal() || !exec.CreatedAt.Before(before) || kept[exec.Session] {
			continue
		}
		for stepID, step := range s.steps {
			if step.ExecutionID != id {
				continue
			}
			for appID, app := range s.approvals {
				if app.StepID == stepID {
					delete(s.approvals, appID)
				}
			}
			delete(s.steps, stepID)
		}
		for dispID, d := range s.dispatches {
			if d.ExecutionID == id {
				delete(s.dispatches, dispID)
			}
		}
		delete(s.events, id)
		delete(s.executions, id)
		delete(s.states, id)
		for childID, child := range s.executions {
			if child.ParentExecutionID != nil && *child.ParentExecutionID == id {
				child.ParentExecutionID = nil
			}
			if child.ForkedFrom != nil && *child.ForkedFrom == id {
				child.ForkedFrom = nil
			}
			s.executions[childID] = child
		}
	}
}

func (tx *txStore) CreateExecution(ctx context.Context, exec domain.Execution) error {
	return tx.createExecutionLocked(ctx, exec)
}

func (tx *txStore) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return tx.getExecutionLocked(ctx, id)
}

func (tx *txStore) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	return tx.updateExecutionStatusLocked(ctx, id, status, output, reason)
}

func (tx *txStore) ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	return tx.listExpiredExecutionsLocked(ctx, now)
}

func (tx *txStore) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	tx.deleteExecutionsCreatedBeforeLocked(ctx, before)
	return nil
}

func (s *Store) ListIdleSessions(_ context.Context, now time.Time) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listIdleSessionsLocked(now), nil
}

func (tx *txStore) ListIdleSessions(_ context.Context, now time.Time) ([]string, error) {
	return tx.listIdleSessionsLocked(now), nil
}

func (s *Store) listIdleSessionsLocked(now time.Time) []string {
	queued := make(map[string]bool)
	active := make(map[string]bool)
	for _, exec := range s.executions {
		if exec.Session == "" {
			continue
		}
		switch exec.Status {
		case domain.ExecutionRunning, domain.ExecutionBlocked:
			active[exec.Session] = true
		case domain.ExecutionPending:
			if exec.DeadlineAt == nil || exec.DeadlineAt.After(now) {
				queued[exec.Session] = true
			}
		}
	}
	var sessions []string
	for session := range queued {
		if !active[session] {
			sessions = append(sessions, session)
		}
	}
	sort.Strings(sessions)
	return sessions
}

func (s *Store) NextPendingInSession(_ context.Context, session string, now time.Time) (domain.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextPendingInSessionLocked(session, now)
}

func (tx *txStore) NextPendingInSession(_ context.Context, session string, now time.Time) (domain.Execution, error) {
	return tx.nextPendingInSessionLocked(session, now)
}

func (s *Store) nextPendingInSessionLocked(session string, now time.Time) (domain.Execution, error) {
	var next domain.Execution
	for _, exec := range s.executions {
		if exec.Session != session || exec.Status != domain.ExecutionPending {
			continue
		}
		if exec.DeadlineAt != nil && !exec.DeadlineAt.After(now) {
			continue
		}
		if next.ID == uuid.Nil || exec.ID.String() < next.ID.String() {
			next = exec
		}
	}
	if next.ID == uuid.Nil {
		return domain.Execution{}, domain.ErrNotFound
	}
	return next, nil
}

func (s *Store) SessionHead(_ context.Context, session, agentID string) (domain.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionHeadLocked(session, agentID)
}

func (tx *txStore) SessionHead(_ context.Context, session, agentID string) (domain.Execution, error) {
	return tx.sessionHeadLocked(session, agentID)
}

func (s *Store) sessionHeadLocked(session, agentID string) (domain.Execution, error) {
	var head domain.Execution
	for _, exec := range s.executions {
		if exec.Session != session || exec.AgentID != agentID || exec.Status != domain.ExecutionCompleted {
			continue
		}
		if head.ID == uuid.Nil || exec.ID.String() > head.ID.String() {
			head = exec
		}
	}
	if head.ID == uuid.Nil {
		return domain.Execution{}, domain.ErrNotFound
	}
	return head, nil
}

func (s *Store) SetExecutionParent(_ context.Context, id, parent uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setExecutionParentLocked(id, parent)
}

func (tx *txStore) SetExecutionParent(_ context.Context, id, parent uuid.UUID) error {
	return tx.setExecutionParentLocked(id, parent)
}

func (s *Store) setExecutionParentLocked(id, parent uuid.UUID) error {
	exec, ok := s.executions[id]
	if !ok {
		return domain.ErrNotFound
	}
	if _, ok := s.executions[parent]; !ok {
		return domain.ErrNotFound
	}
	exec.ParentExecutionID = &parent
	s.executions[id] = exec
	return nil
}

func (s *Store) SetExecutionState(_ context.Context, id uuid.UUID, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setExecutionStateLocked(id, state)
}

func (tx *txStore) SetExecutionState(_ context.Context, id uuid.UUID, state []byte) error {
	return tx.setExecutionStateLocked(id, state)
}

func (s *Store) setExecutionStateLocked(id uuid.UUID, state []byte) error {
	if _, ok := s.executions[id]; !ok {
		return domain.ErrNotFound
	}
	s.states[id] = state
	return nil
}

func (s *Store) ExecutionState(_ context.Context, id uuid.UUID) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executionStateLocked(id)
}

func (tx *txStore) ExecutionState(_ context.Context, id uuid.UUID) (json.RawMessage, error) {
	return tx.executionStateLocked(id)
}

func (s *Store) executionStateLocked(id uuid.UUID) (json.RawMessage, error) {
	if _, ok := s.executions[id]; !ok {
		return nil, domain.ErrNotFound
	}
	return s.states[id], nil
}

func (s *Store) DeleteUnreferencedChunks(context.Context) error {
	return nil
}

func (tx *txStore) DeleteUnreferencedChunks(context.Context) error {
	return nil
}
