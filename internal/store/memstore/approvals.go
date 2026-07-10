package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) CreateApproval(ctx context.Context, approval domain.Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createApprovalLocked(ctx, approval)
	return nil
}

func (s *Store) createApprovalLocked(ctx context.Context, approval domain.Approval) {
	if approval.CreatedAt.IsZero() {
		approval.CreatedAt = time.Now().UTC()
	}
	s.approvals[approval.ID] = approval
}

func (s *Store) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getApprovalLocked(ctx, id)
}

func (s *Store) getApprovalLocked(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	a, ok := s.approvals[id]
	if !ok {
		return domain.Approval{}, domain.ErrNotFound
	}
	return a, nil
}

func (s *Store) UpdateApproval(ctx context.Context, approval domain.Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.approvals[approval.ID]; !ok {
		return domain.ErrNotFound
	}
	s.approvals[approval.ID] = approval
	return nil
}

func (s *Store) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Approval
	for _, a := range s.approvals {
		if a.Status == domain.ApprovalPending {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) ListPendingApprovalsByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Approval
	for _, a := range s.approvals {
		if a.Status == domain.ApprovalPending && a.ExecutionID == execID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) ListExpiredApprovals(ctx context.Context, now time.Time) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Approval
	for _, a := range s.approvals {
		if a.Status == domain.ApprovalPending && !a.TimeoutAt.After(now) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (tx *txStore) CreateApproval(ctx context.Context, approval domain.Approval) error {
	tx.createApprovalLocked(ctx, approval)
	return nil
}

func (tx *txStore) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	return tx.getApprovalLocked(ctx, id)
}

func (tx *txStore) UpdateApproval(ctx context.Context, approval domain.Approval) error {
	if _, ok := tx.approvals[approval.ID]; !ok {
		return domain.ErrNotFound
	}
	tx.approvals[approval.ID] = approval
	return nil
}

func (tx *txStore) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	var out []domain.Approval
	for _, a := range tx.approvals {
		if a.Status == domain.ApprovalPending {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (tx *txStore) ListPendingApprovalsByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Approval, error) {
	var out []domain.Approval
	for _, a := range tx.approvals {
		if a.Status == domain.ApprovalPending && a.ExecutionID == execID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (tx *txStore) ListExpiredApprovals(ctx context.Context, now time.Time) ([]domain.Approval, error) {
	var out []domain.Approval
	for _, a := range tx.approvals {
		if a.Status == domain.ApprovalPending && !a.TimeoutAt.After(now) {
			out = append(out, a)
		}
	}
	return out, nil
}
