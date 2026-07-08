package memstore

import (
	"context"
	"sort"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agent.RegisteredAt.IsZero() {
		agent.RegisteredAt = time.Now().UTC()
	}
	s.agents[agent.ID] = agent
	return nil
}

func (s *Store) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	return a, nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, id)
	return nil
}

// ListAgents returns all registered agents sorted by ID.
func (s *Store) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Agent, 0, len(s.agents))
	for _, a := range s.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
