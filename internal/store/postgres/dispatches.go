package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/kernel/internal/domain"
)

func (s *Store) Enqueue(ctx context.Context, d domain.Dispatch) error {
	return enqueueDispatch(ctx, s.pool, d)
}

func (q querier) Enqueue(ctx context.Context, d domain.Dispatch) error {
	return enqueueDispatch(ctx, q.q, d)
}

func enqueueDispatch(ctx context.Context, q Querier, d domain.Dispatch) error {
	createdAt := d.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := d.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	nextAttemptAt := d.NextAttemptAt
	if nextAttemptAt.IsZero() {
		nextAttemptAt = createdAt
	}

	_, err := q.Exec(ctx, `
		INSERT INTO dispatches (id, execution_id, status, attempt, max_attempts, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`,
		d.ID.String(), d.ExecutionID.String(), string(d.Status), d.Attempt, d.MaxAttempts, nextAttemptAt, createdAt, updatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		if isForeignKeyViolation(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("enqueue dispatch: %w", err)
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	return claimDispatches(ctx, s.pool, replica, batch, now)
}

func (q querier) Claim(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	return claimDispatches(ctx, q.q, replica, batch, now)
}

func claimDispatches(ctx context.Context, q Querier, replica string, batch int, now time.Time) ([]domain.Dispatch, error) {
	rows, err := q.Query(ctx, `
		WITH claimed AS (
			SELECT id
			FROM dispatches
			WHERE status IN ('pending', 'failed') AND next_attempt_at <= $1
			ORDER BY next_attempt_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE dispatches d
		SET status = 'in_flight',
		    attempt = attempt + 1,
		    locked_by = $3,
		    locked_at = $4,
		    updated_at = $4
		FROM claimed c
		WHERE d.id = c.id
		RETURNING d.id, d.execution_id, d.status, d.attempt, d.max_attempts, d.next_attempt_at,
		          d.locked_by, d.locked_at, d.created_at, d.updated_at
	`, now, batch, replica, now)
	if err != nil {
		return nil, fmt.Errorf("claim dispatches: %w", err)
	}
	defer rows.Close()
	return scanDispatches(rows)
}

func (s *Store) Ack(ctx context.Context, id uuid.UUID, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	return ackDispatch(ctx, s.pool, id, status, nextAttemptAt)
}

func (q querier) Ack(ctx context.Context, id uuid.UUID, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	return ackDispatch(ctx, q.q, id, status, nextAttemptAt)
}

func ackDispatch(ctx context.Context, q Querier, id uuid.UUID, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	now := time.Now().UTC()
	res, err := q.Exec(ctx, `
		UPDATE dispatches
		SET status = $2,
		    locked_by = NULL,
		    locked_at = NULL,
		    next_attempt_at = $3,
		    updated_at = $4
		WHERE id = $1
	`, id.String(), string(status), timeArg(nextAttemptAt), now)
	if err != nil {
		return fmt.Errorf("ack dispatch: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	return listDispatchesByExecution(ctx, s.pool, execID)
}

func (q querier) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	return listDispatchesByExecution(ctx, q.q, execID)
}

func listDispatchesByExecution(ctx context.Context, q Querier, execID uuid.UUID) ([]domain.Dispatch, error) {
	rows, err := q.Query(ctx, `
		SELECT id, execution_id, status, attempt, max_attempts, next_attempt_at,
		       locked_by, locked_at, created_at, updated_at
		FROM dispatches
		WHERE execution_id = $1
		ORDER BY created_at
	`, execID.String())
	if err != nil {
		return nil, fmt.Errorf("list dispatches: %w", err)
	}
	defer rows.Close()
	return scanDispatches(rows)
}

func (s *Store) ReclaimStalled(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	return reclaimStalled(ctx, s.pool, now, leaseTimeout, batch)
}

func (q querier) ReclaimStalled(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	return reclaimStalled(ctx, q.q, now, leaseTimeout, batch)
}

func reclaimStalled(ctx context.Context, q Querier, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	cutoff := now.Add(-leaseTimeout)
	rows, err := q.Query(ctx, `
		WITH stalled AS (
			SELECT id
			FROM dispatches
			WHERE status = 'in_flight' AND locked_at < $1
			ORDER BY locked_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE dispatches d
		SET status = 'pending',
		    locked_by = NULL,
		    locked_at = NULL,
		    next_attempt_at = $2,
		    updated_at = $2
		FROM stalled c
		WHERE d.id = c.id
		RETURNING d.id, d.execution_id, d.status, d.attempt, d.max_attempts, d.next_attempt_at,
		          d.locked_by, d.locked_at, d.created_at, d.updated_at
	`, cutoff, now, batch)
	if err != nil {
		return nil, fmt.Errorf("reclaim stalled dispatches: %w", err)
	}
	defer rows.Close()
	return scanDispatches(rows)
}

func scanDispatch(row pgx.Row) (domain.Dispatch, error) {
	var d domain.Dispatch
	var idStr, execIDStr, status string
	var lockedBy *string

	if err := row.Scan(
		&idStr, &execIDStr, &status, &d.Attempt, &d.MaxAttempts, &d.NextAttemptAt,
		&lockedBy, &d.LockedAt, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return domain.Dispatch{}, err
	}

	id, err := parseUUID(idStr)
	if err != nil {
		return domain.Dispatch{}, fmt.Errorf("parse dispatch id: %w", err)
	}
	execID, err := parseUUID(execIDStr)
	if err != nil {
		return domain.Dispatch{}, fmt.Errorf("parse execution id: %w", err)
	}

	d.ID = id
	d.ExecutionID = execID
	d.Status = domain.DispatchStatus(status)
	d.LockedBy = lockedBy
	return d, nil
}

func scanDispatches(rows pgx.Rows) ([]domain.Dispatch, error) {
	var out []domain.Dispatch
	for rows.Next() {
		d, err := scanDispatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan dispatch: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dispatches: %w", err)
	}
	return out, nil
}
