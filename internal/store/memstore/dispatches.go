package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func timePtr(t time.Time) *time.Time { return &t }

func (s *Store) Enqueue(ctx context.Context, d domain.Dispatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueLocked(ctx, d)
	return nil
}

func (s *Store) enqueueLocked(ctx context.Context, d domain.Dispatch) {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = d.CreatedAt
	}
	s.dispatches[d.ID] = d
}

func (s *Store) Claim(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimLocked(ctx, replica, batch, now)
}

func (s *Store) claimLocked(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	var pending []domain.Dispatch
	for _, d := range s.dispatches {
		if (d.Status == domain.DispatchPending || d.Status == domain.DispatchFailed) && !d.NextAttemptAt.After(now) {
			pending = append(pending, d)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].NextAttemptAt.Before(pending[j].NextAttemptAt) })
	if batch > 0 && len(pending) > batch {
		pending = pending[:batch]
	}
	for i := range pending {
		// Each delivery attempt gets a fresh occurrence namespace. A reclaimed
		// dispatch keeps its id, so without this the resumed agent would resume
		// counting instead of replaying, and re-run every recorded effect.
		s.clearCountersLocked(pending[i].ID)
		pending[i].Attempt++
		pending[i].Status = domain.DispatchInFlight
		locked := replica
		pending[i].LockedBy = &locked
		pending[i].LockedAt = timePtr(now)
		pending[i].UpdatedAt = now
		s.dispatches[pending[i].ID] = pending[i]
	}
	return pending, nil
}

func (s *Store) Ack(ctx context.Context, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ackLocked(ctx, id, attempt, status, nextAttemptAt)
}

func (s *Store) ackLocked(ctx context.Context, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	d, ok := s.dispatches[id]
	if !ok || d.Attempt != attempt || d.Status != domain.DispatchInFlight {
		return domain.ErrLeaseSuperseded
	}
	d.Status = status
	d.LockedBy = nil
	d.LockedAt = nil
	if nextAttemptAt != nil {
		d.NextAttemptAt = *nextAttemptAt
	}
	d.UpdatedAt = time.Now().UTC()
	s.dispatches[id] = d
	return nil
}

func (s *Store) Retire(ctx context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retireLocked(id)
}

func (s *Store) retireLocked(id uuid.UUID) error {
	d, ok := s.dispatches[id]
	if !ok {
		return domain.ErrNotFound
	}
	d.Status = domain.DispatchExhausted
	d.LockedBy = nil
	d.LockedAt = nil
	d.UpdatedAt = time.Now().UTC()
	s.dispatches[id] = d
	return nil
}

func (s *Store) GetDispatch(ctx context.Context, id uuid.UUID) (domain.Dispatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getDispatchLocked(id)
}

func (s *Store) getDispatchLocked(id uuid.UUID) (domain.Dispatch, error) {
	d, ok := s.dispatches[id]
	if !ok {
		return domain.Dispatch{}, domain.ErrNotFound
	}
	return d, nil
}

func (s *Store) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listDispatchesByExecutionLocked(ctx, execID)
}

func (s *Store) listDispatchesByExecutionLocked(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	var out []domain.Dispatch
	for _, d := range s.dispatches {
		if d.ExecutionID == execID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) RenewLease(ctx context.Context, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewLeaseLocked(execID, lease, now)
}

func (s *Store) renewLeaseLocked(execID uuid.UUID, lease domain.Lease, now time.Time) error {
	d, ok := s.dispatches[lease.DispatchID]
	if !ok || d.ExecutionID != execID || d.Attempt != lease.Attempt || d.Status != domain.DispatchInFlight {
		return domain.ErrLeaseSuperseded
	}
	d.LockedAt = timePtr(now)
	d.UpdatedAt = now
	s.dispatches[lease.DispatchID] = d
	return nil
}

func (s *Store) CheckLease(ctx context.Context, execID uuid.UUID, lease domain.Lease) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkLeaseLocked(execID, lease)
}

func (s *Store) checkLeaseLocked(execID uuid.UUID, lease domain.Lease) error {
	d, ok := s.dispatches[lease.DispatchID]
	if !ok || d.ExecutionID != execID || d.Attempt != lease.Attempt {
		return domain.ErrLeaseSuperseded
	}
	return nil
}

func (s *Store) ReclaimStalled(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaimStalledLocked(ctx, now, leaseTimeout, batch)
}

func (s *Store) reclaimStalledLocked(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	cutoff := now.Add(-leaseTimeout)
	var stalled []domain.Dispatch
	for _, d := range s.dispatches {
		if d.Status != domain.DispatchInFlight {
			continue
		}
		if d.LockedAt == nil || d.LockedAt.Before(cutoff) {
			stalled = append(stalled, d)
		}
	}
	sort.Slice(stalled, func(i, j int) bool { return (*stalled[i].LockedAt).Before(*stalled[j].LockedAt) })
	if batch > 0 && len(stalled) > batch {
		stalled = stalled[:batch]
	}
	for i := range stalled {
		stalled[i].Status = domain.DispatchPending
		stalled[i].LockedBy = nil
		stalled[i].LockedAt = nil
		stalled[i].NextAttemptAt = now
		stalled[i].UpdatedAt = now
		s.dispatches[stalled[i].ID] = stalled[i]
	}
	return stalled, nil
}

func (tx *txStore) Enqueue(ctx context.Context, d domain.Dispatch) error {
	tx.enqueueLocked(ctx, d)
	return nil
}

func (tx *txStore) Claim(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	return tx.claimLocked(ctx, replica, batch, now)
}

func (tx *txStore) Ack(ctx context.Context, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	return tx.ackLocked(ctx, id, attempt, status, nextAttemptAt)
}

func (tx *txStore) Retire(ctx context.Context, id uuid.UUID) error {
	return tx.retireLocked(id)
}

func (tx *txStore) GetDispatch(ctx context.Context, id uuid.UUID) (domain.Dispatch, error) {
	return tx.getDispatchLocked(id)
}

func (tx *txStore) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	return tx.listDispatchesByExecutionLocked(ctx, execID)
}

func (tx *txStore) RenewLease(ctx context.Context, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	return tx.renewLeaseLocked(execID, lease, now)
}

func (tx *txStore) CheckLease(ctx context.Context, execID uuid.UUID, lease domain.Lease) error {
	return tx.checkLeaseLocked(execID, lease)
}

func (tx *txStore) ReclaimStalled(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	return tx.reclaimStalledLocked(ctx, now, leaseTimeout, batch)
}
