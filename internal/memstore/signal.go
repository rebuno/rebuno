package memstore

import (
	"context"
	"sync"

	"github.com/rebuno/rebuno/internal/domain"
)

type SignalStore struct {
	mu   sync.RWMutex
	data map[string][]domain.Signal
}

func NewSignalStore() *SignalStore {
	return &SignalStore{data: make(map[string][]domain.Signal)}
}

func (s *SignalStore) Publish(_ context.Context, executionID string, signal domain.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[executionID] = append(s.data[executionID], signal)
	return nil
}

// Does NOT clear the list (matches Postgres behaviour).
func (s *SignalStore) GetPending(_ context.Context, executionID string) ([]domain.Signal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.data[executionID]
	if len(src) == 0 {
		return []domain.Signal{}, nil
	}
	cp := make([]domain.Signal, len(src))
	copy(cp, src)
	return cp, nil
}

func (s *SignalStore) Clear(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, executionID)
	return nil
}
