package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
)

func (s *Store) CreateExecution(ctx context.Context, exec domain.Execution) error {
	return createExecution(ctx, s.pool, exec)
}

func (q querier) CreateExecution(ctx context.Context, exec domain.Execution) error {
	return createExecution(ctx, q.q, exec)
}

func createExecution(ctx context.Context, q Querier, exec domain.Execution) error {
	createdAt := exec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := exec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	_, err := q.Exec(ctx, `
		INSERT INTO executions (id, agent_id, agent_version, input, status, output, failure_reason, created_at, updated_at, deadline_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6::jsonb, $7, $8, $9, $10)
	`, exec.ID.String(), exec.AgentID, exec.AgentVersion, rawArg(exec.Input), string(exec.Status),
		rawArg(exec.Output), exec.FailureReason, createdAt, updatedAt, timeArg(exec.DeadlineAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		if isForeignKeyViolation(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("create execution: %w", err)
	}
	return nil
}

func (s *Store) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return getExecution(ctx, s.pool, id)
}

func (q querier) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return getExecution(ctx, q.q, id)
}

func getExecution(ctx context.Context, q Querier, id uuid.UUID) (domain.Execution, error) {
	row := q.QueryRow(ctx, `
		SELECT id, agent_id, agent_version, input, status, output, failure_reason, created_at, updated_at, deadline_at
		FROM executions
		WHERE id = $1
	`, id.String())
	exec, err := scanExecution(row)
	if err != nil {
		return domain.Execution{}, mapNotFound(err)
	}
	return exec, nil
}

func (s *Store) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	return listExecutions(ctx, s.pool, filter)
}

func (q querier) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	return listExecutions(ctx, q.q, filter)
}

func listExecutions(ctx context.Context, q Querier, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	var cursor any
	if filter.Cursor != "" {
		cursor = filter.Cursor
	}
	// Fetch one extra row to detect whether a further page exists. Keyset
	// pagination on id DESC; id is UUIDv7 so id order is creation order.
	rows, err := q.Query(ctx, `
		SELECT id, agent_id, agent_version, input, status, output, failure_reason, created_at, updated_at, deadline_at
		FROM executions
		WHERE ($1 = '' OR agent_id = $1)
		  AND ($2 = '' OR status = $2)
		  AND ($3::uuid IS NULL OR id < $3::uuid)
		ORDER BY id DESC
		LIMIT $4
	`, filter.AgentID, string(filter.Status), cursor, limit+1)
	if err != nil {
		return domain.ExecutionPage{}, fmt.Errorf("list executions: %w", err)
	}
	defer rows.Close()
	var out []domain.Execution
	for rows.Next() {
		exec, err := scanExecution(rows)
		if err != nil {
			return domain.ExecutionPage{}, err
		}
		out = append(out, exec)
	}
	if err := rows.Err(); err != nil {
		return domain.ExecutionPage{}, fmt.Errorf("list executions rows: %w", err)
	}
	var page domain.ExecutionPage
	if len(out) > limit {
		out = out[:limit]
		page.NextCursor = out[limit-1].ID.String()
	}
	page.Executions = out
	return page, nil
}

func (s *Store) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	return updateExecutionStatus(ctx, s.pool, id, status, output, reason)
}

func (q querier) UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	return updateExecutionStatus(ctx, q.q, id, status, output, reason)
}

func updateExecutionStatus(ctx context.Context, q Querier, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error {
	now := time.Now().UTC()
	outputArg := rawArg(output)
	res, err := q.Exec(ctx, `
		UPDATE executions
		SET status = $2,
		    output = COALESCE($3::jsonb, output),
		    failure_reason = CASE WHEN $4 = '' THEN failure_reason ELSE $4 END,
		    updated_at = $5
		WHERE id = $1
		  AND status NOT IN ('completed', 'failed', 'cancelled')
	`, id.String(), string(status), outputArg, reason, now)
	if err != nil {
		return fmt.Errorf("update execution status: %w", err)
	}
	if res.RowsAffected() == 0 {
		exec, err := getExecution(ctx, q, id)
		if err != nil {
			return err
		}
		if exec.Status.IsTerminal() {
			return domain.ErrExecutionTerminal
		}
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	return listExpiredExecutions(ctx, s.pool, now)
}

func (q querier) ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	return listExpiredExecutions(ctx, q.q, now)
}

func listExpiredExecutions(ctx context.Context, q Querier, now time.Time) ([]domain.Execution, error) {
	rows, err := q.Query(ctx, `
		SELECT id, agent_id, agent_version, input, status, output, failure_reason, created_at, updated_at, deadline_at
		FROM executions
		WHERE status IN ('pending','running','blocked')
		  AND deadline_at <= $1
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list expired executions: %w", err)
	}
	defer rows.Close()
	var out []domain.Execution
	for rows.Next() {
		exec, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, exec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list expired executions rows: %w", err)
	}
	return out, nil
}

func (s *Store) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	return deleteExecutionsCreatedBefore(ctx, s.pool, before)
}

func (q querier) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	return deleteExecutionsCreatedBefore(ctx, q.q, before)
}

func deleteExecutionsCreatedBefore(ctx context.Context, q Querier, before time.Time) error {
	if _, err := q.Exec(ctx, `DELETE FROM executions WHERE created_at < $1`, before); err != nil {
		return fmt.Errorf("delete executions: %w", err)
	}
	return nil
}

func scanExecution(row pgx.Row) (domain.Execution, error) {
	var exec domain.Execution
	var idStr, status string
	var input, output *string

	if err := row.Scan(
		&idStr, &exec.AgentID, &exec.AgentVersion, &input, &status,
		&output, &exec.FailureReason, &exec.CreatedAt, &exec.UpdatedAt, &exec.DeadlineAt,
	); err != nil {
		return domain.Execution{}, err
	}

	id, err := parseUUID(idStr)
	if err != nil {
		return domain.Execution{}, fmt.Errorf("parse execution id: %w", err)
	}
	exec.ID = id
	exec.Status = domain.ExecutionStatus(status)
	exec.Input = rawFromPtr(input)
	exec.Output = rawFromPtr(output)
	return exec, nil
}
