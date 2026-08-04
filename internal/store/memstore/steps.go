package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
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

type counterKey struct {
	dispatchID uuid.UUID
	kind       domain.StepKind
	target     string
	argsHash   string
}

func (s *Store) DispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dispatchOccurrenceLocked(dispatchID, kind, target, argsHash)
}

func (s *Store) dispatchOccurrenceLocked(dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	consumed, ok := s.counters[counterKey{dispatchID, kind, target, argsHash}]
	if !ok {
		return 0, nil
	}
	return consumed + 1, nil
}

func (s *Store) AdvanceDispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string, consumed int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.advanceDispatchOccurrenceLocked(dispatchID, kind, target, argsHash, consumed)
}

func (s *Store) clearCountersLocked(dispatchID uuid.UUID) {
	for k := range s.counters {
		if k.dispatchID == dispatchID {
			delete(s.counters, k)
		}
	}
}

func (s *Store) advanceDispatchOccurrenceLocked(dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string, consumed int) error {
	key := counterKey{dispatchID, kind, target, argsHash}
	if prev, ok := s.counters[key]; ok && prev > consumed {
		return nil // keep monotonic, mirroring the GREATEST upsert in postgres
	}
	s.counters[key] = consumed
	return nil
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

func (s *Store) ListStalledSteps(ctx context.Context, cutoff time.Time) ([]domain.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listStalledStepsLocked(cutoff), nil
}

func (s *Store) listStalledStepsLocked(cutoff time.Time) []domain.Step {
	var out []domain.Step
	for _, step := range s.steps {
		if step.Status != domain.StepExecuting {
			continue
		}
		if step.StartedAt == nil || !step.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, step)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepID < out[j].StepID })
	return out
}

func (tx *txStore) Upsert(ctx context.Context, step domain.Step) error {
	tx.upsertStepLocked(ctx, step)
	return nil
}

func (tx *txStore) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return tx.getStepLocked(ctx, stepID)
}

func (tx *txStore) DispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error) {
	return tx.dispatchOccurrenceLocked(dispatchID, kind, target, argsHash)
}

func (tx *txStore) AdvanceDispatchOccurrence(ctx context.Context, dispatchID uuid.UUID, kind domain.StepKind, target, argsHash string, consumed int) error {
	return tx.advanceDispatchOccurrenceLocked(dispatchID, kind, target, argsHash, consumed)
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

func (tx *txStore) ListStalledSteps(ctx context.Context, cutoff time.Time) ([]domain.Step, error) {
	return tx.listStalledStepsLocked(cutoff), nil
}
