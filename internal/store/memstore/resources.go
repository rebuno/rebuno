package memstore

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) putResourceLocked(r domain.Resource) {
	for i, existing := range s.resources {
		if existing.ExecutionID == r.ExecutionID && existing.Key == r.Key {
			s.resources[i] = r
			return
		}
	}
	s.resources = append(s.resources, r)
}

func (s *Store) listResourcesLocked(execID uuid.UUID) []domain.Resource {
	var out []domain.Resource
	for _, r := range s.resources {
		if r.ExecutionID == execID {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b domain.Resource) int { return int(a.RegisteredSeq - b.RegisteredSeq) })
	return out
}

func (s *Store) listCheckpointsLocked(execID uuid.UUID) []domain.ResourceCheckpoint {
	var out []domain.ResourceCheckpoint
	for _, c := range s.checkpoints {
		if c.ExecutionID != execID {
			continue
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b domain.ResourceCheckpoint) int { return int(a.CoveredSeq - b.CoveredSeq) })
	return out
}

func (s *Store) invalidateCheckpointsLocked(execID uuid.UUID, key string, seq int64) {
	for i, c := range s.checkpoints {
		if c.ExecutionID == execID && c.Key == key && c.InvalidatedSeq == 0 {
			s.checkpoints[i].InvalidatedSeq = seq
		}
	}
}

func (s *Store) PutResource(_ context.Context, r domain.Resource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putResourceLocked(r)
	return nil
}

func (tx *txStore) PutResource(_ context.Context, r domain.Resource) error {
	tx.putResourceLocked(r)
	return nil
}

func (s *Store) ListResources(_ context.Context, execID uuid.UUID) ([]domain.Resource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listResourcesLocked(execID), nil
}

func (tx *txStore) ListResources(_ context.Context, execID uuid.UUID) ([]domain.Resource, error) {
	return tx.listResourcesLocked(execID), nil
}

func (s *Store) AddCheckpoint(_ context.Context, c domain.ResourceCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints = append(s.checkpoints, c)
	return nil
}

func (tx *txStore) AddCheckpoint(_ context.Context, c domain.ResourceCheckpoint) error {
	tx.checkpoints = append(tx.checkpoints, c)
	return nil
}

func (s *Store) ListCheckpoints(_ context.Context, execID uuid.UUID) ([]domain.ResourceCheckpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listCheckpointsLocked(execID), nil
}

func (tx *txStore) ListCheckpoints(_ context.Context, execID uuid.UUID) ([]domain.ResourceCheckpoint, error) {
	return tx.listCheckpointsLocked(execID), nil
}

func (s *Store) InvalidateCheckpoints(_ context.Context, execID uuid.UUID, key string, seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidateCheckpointsLocked(execID, key, seq)
	return nil
}

func (tx *txStore) InvalidateCheckpoints(_ context.Context, execID uuid.UUID, key string, seq int64) error {
	tx.invalidateCheckpointsLocked(execID, key, seq)
	return nil
}
