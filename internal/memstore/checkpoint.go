package memstore

import (
	"context"
	"sync"

	"github.com/rebuno/rebuno/internal/domain"
)

type CheckpointStore struct {
	mu   sync.RWMutex
	data map[string]domain.Checkpoint
}

func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{data: make(map[string]domain.Checkpoint)}
}

func (s *CheckpointStore) Get(_ context.Context, executionID string) (*domain.Checkpoint, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp, ok := s.data[executionID]
	if !ok {
		return nil, false, nil
	}
	c := cp
	return &c, true, nil
}

func (s *CheckpointStore) Save(_ context.Context, checkpoint domain.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[checkpoint.ExecutionID] = checkpoint
	return nil
}

func (s *CheckpointStore) Delete(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, executionID)
	return nil
}
