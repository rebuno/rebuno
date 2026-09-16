package memstore

import (
	"context"
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
		if filter.ConcurrencyKey != "" && e.ConcurrencyKey != filter.ConcurrencyKey {
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
	if exec.ConcurrencyKey != "" && (status == domain.ExecutionRunning || status == domain.ExecutionBlocked) {
		for _, other := range s.executions {
			if other.ID != id && other.ConcurrencyKey == exec.ConcurrencyKey &&
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
	for id, exec := range s.executions {
		if !exec.Status.IsTerminal() || !exec.CreatedAt.Before(before) {
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

func (s *Store) ListIdleConcurrencyKeys(_ context.Context, now time.Time) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listIdleConcurrencyKeysLocked(now), nil
}

func (tx *txStore) ListIdleConcurrencyKeys(_ context.Context, now time.Time) ([]string, error) {
	return tx.listIdleConcurrencyKeysLocked(now), nil
}

func (s *Store) listIdleConcurrencyKeysLocked(now time.Time) []string {
	queued := make(map[string]bool)
	active := make(map[string]bool)
	for _, exec := range s.executions {
		if exec.ConcurrencyKey == "" {
			continue
		}
		switch exec.Status {
		case domain.ExecutionRunning, domain.ExecutionBlocked:
			active[exec.ConcurrencyKey] = true
		case domain.ExecutionPending:
			if exec.DeadlineAt == nil || exec.DeadlineAt.After(now) {
				queued[exec.ConcurrencyKey] = true
			}
		}
	}
	var keys []string
	for key := range queued {
		if !active[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) NextPendingByKey(_ context.Context, key string, now time.Time) (domain.Execution, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextPendingByKeyLocked(key, now)
}

func (tx *txStore) NextPendingByKey(_ context.Context, key string, now time.Time) (domain.Execution, error) {
	return tx.nextPendingByKeyLocked(key, now)
}

func (s *Store) nextPendingByKeyLocked(key string, now time.Time) (domain.Execution, error) {
	var next domain.Execution
	for _, exec := range s.executions {
		if exec.ConcurrencyKey != key || exec.Status != domain.ExecutionPending {
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
