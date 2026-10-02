package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
)

func putResource(ctx context.Context, q Querier, r domain.Resource) error {
	_, err := q.Exec(ctx, `
		INSERT INTO execution_resources (
			execution_id, key, driver_id, config, coverage_reuse, every_steps,
			on_completion, registered_seq, generation, count, binding, checkpoint_ref
		) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11::jsonb, $12)
		ON CONFLICT (execution_id, key) DO UPDATE SET
			generation     = EXCLUDED.generation,
			count          = EXCLUDED.count,
			binding        = EXCLUDED.binding,
			checkpoint_ref = EXCLUDED.checkpoint_ref
	`, r.ExecutionID.String(), r.Key, r.DriverID, rawArg(r.Config), r.CoverageReuse, r.EverySteps,
		r.OnCompletion, r.RegisteredSeq, r.Generation, r.Count, rawArg(r.Binding), r.CheckpointRef)
	if err != nil {
		return fmt.Errorf("put resource: %w", err)
	}
	return nil
}

func listResources(ctx context.Context, q Querier, execID uuid.UUID) ([]domain.Resource, error) {
	rows, err := q.Query(ctx, `
		SELECT key, driver_id, config, coverage_reuse, every_steps, on_completion,
		       registered_seq, generation, count, binding, checkpoint_ref
		FROM execution_resources
		WHERE execution_id = $1
		ORDER BY registered_seq, key
	`, execID.String())
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Resource, error) {
		r := domain.Resource{ExecutionID: execID}
		var config, binding *string
		err := row.Scan(&r.Key, &r.DriverID, &config, &r.CoverageReuse, &r.EverySteps, &r.OnCompletion,
			&r.RegisteredSeq, &r.Generation, &r.Count, &binding, &r.CheckpointRef)
		r.Config = rawFromPtr(config)
		r.Binding = rawFromPtr(binding)
		return r, err
	})
}

func addCheckpoint(ctx context.Context, q Querier, c domain.ResourceCheckpoint) error {
	_, err := q.Exec(ctx, `
		INSERT INTO resource_checkpoints (
			execution_id, key, covered_seq, generation, checkpoint_ref, invalidated_seq
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6::bigint, 0))
	`, c.ExecutionID.String(), c.Key, c.CoveredSeq, c.Generation, c.Ref, c.InvalidatedSeq)
	if err != nil {
		return fmt.Errorf("add checkpoint: %w", err)
	}
	return nil
}

func listCheckpoints(ctx context.Context, q Querier, execID uuid.UUID) ([]domain.ResourceCheckpoint, error) {
	rows, err := q.Query(ctx, `
		SELECT c.key, c.covered_seq, c.generation, c.checkpoint_ref,
		       COALESCE(c.invalidated_seq, 0)
		FROM resource_checkpoints c
		WHERE c.execution_id = $1
		ORDER BY c.covered_seq, c.key
	`, execID.String())
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ResourceCheckpoint, error) {
		c := domain.ResourceCheckpoint{ExecutionID: execID}
		err := row.Scan(&c.Key, &c.CoveredSeq, &c.Generation, &c.Ref, &c.InvalidatedSeq)
		return c, err
	})
}

func invalidateCheckpoints(ctx context.Context, q Querier, execID uuid.UUID, key string, seq int64) error {
	_, err := q.Exec(ctx, `
		UPDATE resource_checkpoints SET invalidated_seq = $3
		WHERE execution_id = $1 AND key = $2 AND invalidated_seq IS NULL
	`, execID.String(), key, seq)
	if err != nil {
		return fmt.Errorf("invalidate checkpoints: %w", err)
	}
	return nil
}

func (s *Store) PutResource(ctx context.Context, r domain.Resource) error {
	return putResource(ctx, s.q(ctx), r)
}

func (q querier) PutResource(ctx context.Context, r domain.Resource) error {
	return putResource(ctx, q.q, r)
}

func (s *Store) ListResources(ctx context.Context, execID uuid.UUID) ([]domain.Resource, error) {
	return listResources(ctx, s.q(ctx), execID)
}

func (q querier) ListResources(ctx context.Context, execID uuid.UUID) ([]domain.Resource, error) {
	return listResources(ctx, q.q, execID)
}

func (s *Store) AddCheckpoint(ctx context.Context, c domain.ResourceCheckpoint) error {
	return addCheckpoint(ctx, s.q(ctx), c)
}

func (q querier) AddCheckpoint(ctx context.Context, c domain.ResourceCheckpoint) error {
	return addCheckpoint(ctx, q.q, c)
}

func (s *Store) ListCheckpoints(ctx context.Context, execID uuid.UUID) ([]domain.ResourceCheckpoint, error) {
	return listCheckpoints(ctx, s.q(ctx), execID)
}

func (q querier) ListCheckpoints(ctx context.Context, execID uuid.UUID) ([]domain.ResourceCheckpoint, error) {
	return listCheckpoints(ctx, q.q, execID)
}

func (s *Store) InvalidateCheckpoints(ctx context.Context, execID uuid.UUID, key string, seq int64) error {
	return invalidateCheckpoints(ctx, s.q(ctx), execID, key, seq)
}

func (q querier) InvalidateCheckpoints(ctx context.Context, execID uuid.UUID, key string, seq int64) error {
	return invalidateCheckpoints(ctx, q.q, execID, key, seq)
}
