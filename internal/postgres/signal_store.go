package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

type SignalStore struct {
	pool *pgxpool.Pool
}

func NewSignalStore(pool *pgxpool.Pool) *SignalStore {
	return &SignalStore{pool: pool}
}

func (s *SignalStore) Publish(ctx context.Context, executionID string, signal domain.Signal) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO signals (id, execution_id, signal_type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, signal.ID, executionID, signal.SignalType, signal.Payload, signal.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert signal: %w", err)
	}
	return nil
}

func (s *SignalStore) GetPending(ctx context.Context, executionID string) ([]domain.Signal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, execution_id, signal_type, payload, created_at
		FROM signals
		WHERE execution_id = $1
		ORDER BY created_at ASC
	`, executionID)
	if err != nil {
		return nil, fmt.Errorf("query pending signals: %w", err)
	}
	defer rows.Close()

	var signals []domain.Signal
	for rows.Next() {
		var sig domain.Signal
		if err := rows.Scan(
			&sig.ID, &sig.ExecutionID, &sig.SignalType,
			&sig.Payload, &sig.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan signal: %w", err)
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

func (s *SignalStore) Clear(ctx context.Context, executionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM signals WHERE execution_id = $1`,
		executionID,
	)
	if err != nil {
		return fmt.Errorf("delete signals: %w", err)
	}
	return nil
}
