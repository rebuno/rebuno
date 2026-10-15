package memstore

import (
	"context"
	"fmt"
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
	existing := s.agents[agent.ID]
	agent.AgentMetadata = existing.AgentMetadata
	if agent.PolicyBundle == "" {
		agent.PolicyBundle = existing.PolicyBundle
	}
	s.agents[agent.ID] = agent
	return nil
}

func (s *Store) SetPolicyBundle(ctx context.Context, id string, bundle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	a.PolicyBundle = bundle
	s.agents[id] = a
	return nil
}

func (s *Store) SetAgentMetadata(ctx context.Context, id string, metadata domain.AgentMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	a.AgentMetadata = metadata
	s.agents[id] = a
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
	for _, e := range s.executions {
		if e.AgentID == id {
			return fmt.Errorf("%w: agent %q has executions", domain.ErrConflict, id)
		}
	}
	delete(s.agents, id)
	return nil
}

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
