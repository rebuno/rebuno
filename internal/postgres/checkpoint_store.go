package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

type CheckpointStore struct {
	pool *pgxpool.Pool
}

func NewCheckpointStore(pool *pgxpool.Pool) *CheckpointStore {
	return &CheckpointStore{pool: pool}
}

func (s *CheckpointStore) Get(ctx context.Context, executionID string) (*domain.Checkpoint, bool, error) {
	var cp domain.Checkpoint
	err := s.pool.QueryRow(ctx, `
		SELECT execution_id, sequence, state_data, created_at
		FROM checkpoints
		WHERE execution_id = $1
	`, executionID).Scan(&cp.ExecutionID, &cp.Sequence, &cp.StateData, &cp.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query checkpoint: %w", err)
	}
	return &cp, true, nil
}

func (s *CheckpointStore) Save(ctx context.Context, checkpoint domain.Checkpoint) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO checkpoints (execution_id, sequence, state_data, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (execution_id) DO UPDATE
		SET sequence   = EXCLUDED.sequence,
		    state_data = EXCLUDED.state_data,
		    created_at = EXCLUDED.created_at
	`, checkpoint.ExecutionID, checkpoint.Sequence, checkpoint.StateData, checkpoint.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert checkpoint: %w", err)
	}
	return nil
}

func (s *CheckpointStore) Delete(ctx context.Context, executionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM checkpoints WHERE execution_id = $1`,
		executionID,
	)
	if err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return nil
}
