package memstore

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/domain"
)

func (s *Store) Upsert(ctx context.Context, step domain.Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertStepLocked(ctx, step)
	return nil
}

func (s *Store) upsertStepLocked(ctx context.Context, step domain.Step) {
	key := step.StepID
	existing, ok := s.steps[key]
	if ok {
		// Preserve non-overwritten immutable fields if not set.
		if step.ExecutionID == uuid.Nil {
			step.ExecutionID = existing.ExecutionID
		}
		if step.Kind == "" {
			step.Kind = existing.Kind
		}
		if step.Target == "" {
			step.Target = existing.Target
		}
		if step.ArgsHash == "" {
			step.ArgsHash = existing.ArgsHash
		}
		if step.Idempotency == "" {
			step.Idempotency = existing.Idempotency
		}
		// Terminal is source of truth: never overwrite terminal result/error.
		if existing.Status.IsTerminal() {
			step.Status = existing.Status
			step.Result = existing.Result
			step.Error = existing.Error
			step.CompletedAt = existing.CompletedAt
		}
	}
	s.steps[key] = step
}

func (s *Store) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getStepLocked(ctx, stepID)
}

func (s *Store) getStepLocked(ctx context.Context, stepID string) (domain.Step, error) {
	step, ok := s.steps[stepID]
	if !ok {
		return domain.Step{}, domain.ErrNotFound
	}
	return step, nil
}

func (s *Store) CountOccurrence(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countOccurrenceLocked(ctx, execID, kind, target, argsHash)
}

func (s *Store) countOccurrenceLocked(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	count := 0
	for _, step := range s.steps {
		if step.ExecutionID == execID && step.Kind == kind && step.Target == target && step.ArgsHash == argsHash {
			count++
		}
	}
	return count, nil
}

func (s *Store) ListByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Step
	for _, step := range s.steps {
		if step.ExecutionID == execID {
			out = append(out, step)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepID < out[j].StepID })
	return out, nil
}

func (tx *txStore) Upsert(ctx context.Context, step domain.Step) error {
	tx.upsertStepLocked(ctx, step)
	return nil
}

func (tx *txStore) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return tx.getStepLocked(ctx, stepID)
}

func (tx *txStore) CountOccurrence(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return tx.countOccurrenceLocked(ctx, execID, kind, target, argsHash)
}

func (tx *txStore) ListByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	var out []domain.Step
	for _, step := range tx.steps {
		if step.ExecutionID == execID {
			out = append(out, step)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepID < out[j].StepID })
	return out, nil
}
