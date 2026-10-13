package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	return registerAgent(ctx, s.q(ctx), agent)
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
		INSERT INTO agents (id, webhook_url, secret, registered_at, policy_bundle, lease_timeout_seconds)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET
			webhook_url = EXCLUDED.webhook_url,
			secret = EXCLUDED.secret,
			registered_at = EXCLUDED.registered_at,
			policy_bundle = EXCLUDED.policy_bundle,
			lease_timeout_seconds = EXCLUDED.lease_timeout_seconds
	`, agent.ID, agent.WebhookURL, agent.Secret, registeredAt, agent.PolicyBundle, agent.LeaseTimeoutSeconds)
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	return nil
}

func (s *Store) SetAgentMetadata(ctx context.Context, id string, metadata domain.AgentMetadata) error {
	res, err := s.q(ctx).Exec(ctx, `
		UPDATE agents SET description = $2, input_schema = $3, output_schema = $4 WHERE id = $1
	`, id, metadata.Description, rawArg(metadata.InputSchema), rawArg(metadata.OutputSchema))
	if err != nil {
		return fmt.Errorf("set agent metadata: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

const agentColumns = `id, webhook_url, secret, registered_at, policy_bundle, lease_timeout_seconds,
	description, input_schema, output_schema`

func scanAgent(row pgx.Row) (domain.Agent, error) {
	var agent domain.Agent
	var inputSchema, outputSchema *string
	if err := row.Scan(&agent.ID, &agent.WebhookURL, &agent.Secret, &agent.RegisteredAt, &agent.PolicyBundle,
		&agent.LeaseTimeoutSeconds, &agent.Description, &inputSchema, &outputSchema); err != nil {
		return domain.Agent{}, err
	}
	agent.InputSchema = rawFromPtr(inputSchema)
	agent.OutputSchema = rawFromPtr(outputSchema)
	return agent, nil
}

func (s *Store) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return getAgent(ctx, s.q(ctx), id)
}

func (q querier) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return getAgent(ctx, q.q, id)
}

func getAgent(ctx context.Context, q Querier, id string) (domain.Agent, error) {
	agent, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1`, id))
	if err != nil {
		return domain.Agent{}, mapNotFound(err)
	}
	return agent, nil
}

func (s *Store) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	rows, err := s.q(ctx).Query(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var out []domain.Agent
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
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
	return deleteAgent(ctx, s.q(ctx), id)
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
