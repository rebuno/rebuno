package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type RunnerStore struct {
	mu   sync.RWMutex
	data map[string]domain.Runner
}

func NewRunnerStore() *RunnerStore {
	return &RunnerStore{data: make(map[string]domain.Runner)}
}

func (s *RunnerStore) Register(_ context.Context, runner domain.Runner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[runner.ID] = runner
	return nil
}

func (s *RunnerStore) Get(_ context.Context, runnerID string) (*domain.Runner, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.data[runnerID]
	if !ok {
		return nil, false, nil
	}
	c := r
	return &c, true, nil
}

func (s *RunnerStore) List(_ context.Context) ([]domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	runners := make([]domain.Runner, 0, len(s.data))
	for _, r := range s.data {
		runners = append(runners, r)
	}
	return runners, nil
}

func (s *RunnerStore) UpdateHeartbeat(_ context.Context, runnerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[runnerID]
	if !ok {
		return nil
	}
	r.LastHeartbeat = time.Now()
	s.data[runnerID] = r
	return nil
}

func (s *RunnerStore) Delete(_ context.Context, runnerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, runnerID)
	return nil
}
