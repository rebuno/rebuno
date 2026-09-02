package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
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
		),
		cleared AS (
			DELETE FROM dispatch_step_counters
			WHERE dispatch_id IN (SELECT id FROM claimed)
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

func (s *Store) Ack(ctx context.Context, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	return ackDispatch(ctx, s.pool, id, attempt, status, nextAttemptAt)
}

func (q querier) Ack(ctx context.Context, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	return ackDispatch(ctx, q.q, id, attempt, status, nextAttemptAt)
}

func ackDispatch(ctx context.Context, q Querier, id uuid.UUID, attempt int, status domain.DispatchStatus, nextAttemptAt *time.Time) error {
	now := time.Now().UTC()
	res, err := q.Exec(ctx, `
		UPDATE dispatches
		SET status = $2,
		    locked_by = NULL,
		    locked_at = NULL,
		    next_attempt_at = COALESCE($3, next_attempt_at),
		    updated_at = $4
		WHERE id = $1 AND attempt = $5 AND status = 'in_flight'
	`, id.String(), string(status), timeArg(nextAttemptAt), now, attempt)
	if err != nil {
		return fmt.Errorf("ack dispatch: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrLeaseSuperseded
	}
	return nil
}

func (s *Store) Retire(ctx context.Context, id uuid.UUID) error {
	return retireDispatch(ctx, s.pool, id)
}

func (q querier) Retire(ctx context.Context, id uuid.UUID) error {
	return retireDispatch(ctx, q.q, id)
}

func retireDispatch(ctx context.Context, q Querier, id uuid.UUID) error {
	now := time.Now().UTC()
	res, err := q.Exec(ctx, `
		UPDATE dispatches
		SET status = 'exhausted',
		    locked_by = NULL,
		    locked_at = NULL,
		    updated_at = $2
		WHERE id = $1
	`, id.String(), now)
	if err != nil {
		return fmt.Errorf("retire dispatch: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) GetDispatch(ctx context.Context, id uuid.UUID) (domain.Dispatch, error) {
	return getDispatch(ctx, s.pool, id)
}

func (q querier) GetDispatch(ctx context.Context, id uuid.UUID) (domain.Dispatch, error) {
	return getDispatch(ctx, q.q, id)
}

func getDispatch(ctx context.Context, q Querier, id uuid.UUID) (domain.Dispatch, error) {
	row := q.QueryRow(ctx, `
		SELECT id, execution_id, status, attempt, max_attempts, next_attempt_at,
		       locked_by, locked_at, created_at, updated_at
		FROM dispatches
		WHERE id = $1
	`, id.String())
	d, err := scanDispatch(row)
	if err == pgx.ErrNoRows {
		return domain.Dispatch{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Dispatch{}, fmt.Errorf("get dispatch: %w", err)
	}
	return d, nil
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

func (s *Store) RenewLease(ctx context.Context, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	return renewLease(ctx, s.pool, execID, lease, now)
}

func (q querier) RenewLease(ctx context.Context, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	return renewLease(ctx, q.q, execID, lease, now)
}

func renewLease(ctx context.Context, q Querier, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	res, err := q.Exec(ctx, `
		UPDATE dispatches
		SET locked_at = $4, updated_at = $4
		WHERE id = $1 AND execution_id = $2 AND attempt = $3 AND status = 'in_flight'
	`, lease.DispatchID.String(), execID.String(), lease.Attempt, now)
	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrLeaseSuperseded
	}
	return nil
}

func (s *Store) CheckLease(ctx context.Context, execID uuid.UUID, lease domain.Lease) error {
	return checkLease(ctx, s.pool, execID, lease)
}

func (q querier) CheckLease(ctx context.Context, execID uuid.UUID, lease domain.Lease) error {
	return checkLease(ctx, q.q, execID, lease)
}

func checkLease(ctx context.Context, q Querier, execID uuid.UUID, lease domain.Lease) error {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT true FROM dispatches
		WHERE id = $1 AND execution_id = $2 AND attempt = $3
		FOR UPDATE
	`, lease.DispatchID.String(), execID.String(), lease.Attempt).Scan(&ok)
	if err == pgx.ErrNoRows {
		return domain.ErrLeaseSuperseded
	}
	if err != nil {
		return fmt.Errorf("check lease: %w", err)
	}
	return nil
}

func (s *Store) ReclaimStalled(ctx context.Context, now time.Time, defaultLeaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	return reclaimStalled(ctx, s.pool, now, defaultLeaseTimeout, batch)
}

func (q querier) ReclaimStalled(ctx context.Context, now time.Time, defaultLeaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	return reclaimStalled(ctx, q.q, now, defaultLeaseTimeout, batch)
}

// reclaimStalled expires each lease against its agent's timeout, falling back to
// defaultLeaseTimeout. FOR UPDATE OF d locks only dispatches.
func reclaimStalled(ctx context.Context, q Querier, now time.Time, defaultLeaseTimeout time.Duration, batch int) ([]domain.Dispatch, error) {
	rows, err := q.Query(ctx, `
		WITH stalled AS (
			SELECT d.id
			FROM dispatches d
			JOIN executions e ON e.id = d.execution_id
			JOIN agents a ON a.id = e.agent_id
			WHERE d.status = 'in_flight'
			  AND d.locked_at < $1::timestamptz - make_interval(secs =>
			        COALESCE(a.lease_timeout_seconds, $3))
			ORDER BY d.locked_at
			LIMIT $2
			FOR UPDATE OF d SKIP LOCKED
		)
		UPDATE dispatches d
		SET status = 'pending',
		    locked_by = NULL,
		    locked_at = NULL,
		    next_attempt_at = $1,
		    updated_at = $1
		FROM stalled c
		WHERE d.id = c.id
		RETURNING d.id, d.execution_id, d.status, d.attempt, d.max_attempts, d.next_attempt_at,
		          d.locked_by, d.locked_at, d.created_at, d.updated_at
	`, now, batch, defaultLeaseTimeout.Seconds())
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
