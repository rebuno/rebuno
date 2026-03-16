package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

type RunnerStore struct {
	pool *pgxpool.Pool
}

func NewRunnerStore(pool *pgxpool.Pool) *RunnerStore {
	return &RunnerStore{pool: pool}
}

func (s *RunnerStore) Register(ctx context.Context, runner domain.Runner) error {
	capabilitiesJSON, err := json.Marshal(runner.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO runners (id, name, capabilities, status, last_heartbeat, registered_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE
		SET name           = EXCLUDED.name,
		    capabilities   = EXCLUDED.capabilities,
		    metadata       = EXCLUDED.metadata,
		    last_heartbeat = EXCLUDED.last_heartbeat,
		    status         = EXCLUDED.status
	`,
		runner.ID, runner.Name, capabilitiesJSON,
		runner.Status, runner.LastHeartbeat, runner.RegisteredAt, runner.Metadata,
	)
	if err != nil {
		return fmt.Errorf("upsert runner: %w", err)
	}
	return nil
}

func (s *RunnerStore) Get(ctx context.Context, runnerID string) (*domain.Runner, bool, error) {
	var r domain.Runner
	var capJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, capabilities, status, last_heartbeat, registered_at, metadata
		FROM runners
		WHERE id = $1
	`, runnerID).Scan(
		&r.ID, &r.Name, &capJSON,
		&r.Status, &r.LastHeartbeat, &r.RegisteredAt, &r.Metadata,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query runner: %w", err)
	}

	if capJSON != nil {
		if err := json.Unmarshal(capJSON, &r.Capabilities); err != nil {
			return nil, false, fmt.Errorf("unmarshal capabilities: %w", err)
		}
	}

	return &r, true, nil
}

func (s *RunnerStore) List(ctx context.Context) ([]domain.Runner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, capabilities, status, last_heartbeat, registered_at, metadata
		FROM runners
		ORDER BY registered_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query runners: %w", err)
	}
	defer rows.Close()

	var runners []domain.Runner
	for rows.Next() {
		var r domain.Runner
		var capJSON []byte
		if err := rows.Scan(
			&r.ID, &r.Name, &capJSON,
			&r.Status, &r.LastHeartbeat, &r.RegisteredAt, &r.Metadata,
		); err != nil {
			return nil, fmt.Errorf("scan runner: %w", err)
		}
		if capJSON != nil {
			if err := json.Unmarshal(capJSON, &r.Capabilities); err != nil {
				return nil, fmt.Errorf("unmarshal capabilities: %w", err)
			}
		}
		runners = append(runners, r)
	}
	return runners, rows.Err()
}

func (s *RunnerStore) UpdateHeartbeat(ctx context.Context, runnerID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE runners SET last_heartbeat = now(), status = 'online' WHERE id = $1`,
		runnerID,
	)
	if err != nil {
		return fmt.Errorf("update heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("runner %s not found: %w", runnerID, domain.ErrNotFound)
	}
	return nil
}

func (s *RunnerStore) Delete(ctx context.Context, runnerID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM runners WHERE id = $1`,
		runnerID,
	)
	if err != nil {
		return fmt.Errorf("delete runner: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("runner %s not found: %w", runnerID, domain.ErrNotFound)
	}
	return nil
}
