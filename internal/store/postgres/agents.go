package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	return registerAgent(ctx, s.pool, agent)
}

func (q querier) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	return registerAgent(ctx, q.q, agent)
}

func registerAgent(ctx context.Context, q Querier, agent domain.Agent) error {
	registeredAt := agent.RegisteredAt
	if registeredAt.IsZero() {
		registeredAt = time.Now().UTC()
	}

	_, err := q.Exec(ctx, `
		INSERT INTO agents (id, webhook_url, secret, registered_at, policy_bundle)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			webhook_url = EXCLUDED.webhook_url,
			secret = EXCLUDED.secret,
			registered_at = EXCLUDED.registered_at,
			policy_bundle = EXCLUDED.policy_bundle
	`, agent.ID, agent.WebhookURL, agent.Secret, registeredAt, agent.PolicyBundle)
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	return nil
}

func (s *Store) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return getAgent(ctx, s.pool, id)
}

func (q querier) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return getAgent(ctx, q.q, id)
}

func getAgent(ctx context.Context, q Querier, id string) (domain.Agent, error) {
	var agent domain.Agent
	row := q.QueryRow(ctx, `SELECT id, webhook_url, secret, registered_at, COALESCE(policy_bundle, '') FROM agents WHERE id = $1`, id)
	err := row.Scan(&agent.ID, &agent.WebhookURL, &agent.Secret, &agent.RegisteredAt, &agent.PolicyBundle)
	if err != nil {
		return domain.Agent{}, mapNotFound(err)
	}
	return agent, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, webhook_url, secret, registered_at, COALESCE(policy_bundle, '')
		FROM agents
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []domain.Agent
	for rows.Next() {
		var agent domain.Agent
		if err := rows.Scan(&agent.ID, &agent.WebhookURL, &agent.Secret, &agent.RegisteredAt, &agent.PolicyBundle); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agents rows: %w", err)
	}
	return out, nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	return deleteAgent(ctx, s.pool, id)
}

func (q querier) DeleteAgent(ctx context.Context, id string) error {
	return deleteAgent(ctx, q.q, id)
}

func deleteAgent(ctx context.Context, q Querier, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
