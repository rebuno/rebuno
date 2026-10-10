package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
)

const executionColumns = `id, agent_id, input, status, output, failure_reason,
	created_at, updated_at, deadline_at, COALESCE(session, ''), parent_execution_id,
	forked_from, COALESCE(fork_seq, 0), COALESCE(policy_bundle, ''), COALESCE(idempotency_key, ''),
	spawned_by_execution_id, COALESCE(spawned_by_step_id, '')`

func (s *Store) CreateExecution(ctx context.Context, exec domain.Execution) error {
	return createExecution(ctx, s.q(ctx), exec)
}

func (q querier) CreateExecution(ctx context.Context, exec domain.Execution) error {
	return createExecution(ctx, q.q, exec)
}

func createExecution(ctx context.Context, q Querier, exec domain.Execution) error {
	var spawnedByID *uuid.UUID
	var spawnedByStep string
	if exec.SpawnedBy != nil {
		spawnedByID, spawnedByStep = &exec.SpawnedBy.ExecutionID, exec.SpawnedBy.StepID
	}
	createdAt := exec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := exec.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}

	_, err := q.Exec(ctx, `
		INSERT INTO executions (id, agent_id, input, status, output, failure_reason, created_at, updated_at, deadline_at, session,
			parent_execution_id, forked_from, fork_seq, policy_bundle, idempotency_key, spawned_by_execution_id, spawned_by_step_id)
		VALUES ($1, $2, $3::jsonb, $4, $5::jsonb, $6, $7, $8, $9, NULLIF($10, ''),
			$11::uuid, $12::uuid, NULLIF($13, 0), NULLIF($14, ''), NULLIF($15, ''), $16::uuid, NULLIF($17, ''))
	`, exec.ID.String(), exec.AgentID, rawArg(exec.Input), string(exec.Status),
		rawArg(exec.Output), exec.FailureReason, createdAt, updatedAt, timeArg(exec.DeadlineAt), exec.Session,
		uuidArg(exec.ParentExecutionID), uuidArg(exec.ForkedFrom), exec.ForkSeq, exec.PolicyBundle, exec.IdempotencyKey,
		uuidArg(spawnedByID), spawnedByStep,
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
	return getExecution(ctx, s.q(ctx), id)
}

func (q querier) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return getExecution(ctx, q.q, id)
}

func getExecution(ctx context.Context, q Querier, id uuid.UUID) (domain.Execution, error) {
	row := q.QueryRow(ctx, `
		SELECT `+executionColumns+`
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
	return listExecutions(ctx, s.q(ctx), filter)
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
	rows, err := q.Query(ctx, `
		SELECT `+executionColumns+`
		FROM executions
		WHERE ($1 = '' OR agent_id = $1)
		  AND ($2 = '' OR status = $2)
		  AND ($3::uuid IS NULL OR id < $3::uuid)
		  AND ($5 = '' OR session = $5)
		  AND ($6 = '' OR idempotency_key = $6)
		  AND ($7::uuid IS NULL OR spawned_by_execution_id = $7::uuid)
		  AND ($8::uuid IS NULL OR parent_execution_id = $8::uuid)
		  AND ($9::uuid IS NULL OR forked_from = $9::uuid)
		ORDER BY id DESC
		LIMIT $4
	`, filter.AgentID, string(filter.Status), cursor, limit+1, filter.Session, filter.IdempotencyKey,
		uuidArg(filter.SpawnedBy), uuidArg(filter.ParentExecutionID), uuidArg(filter.ForkedFrom))
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
	return updateExecutionStatus(ctx, s.q(ctx), id, status, output, reason)
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
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
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
	return listExpiredExecutions(ctx, s.q(ctx), now)
}

func (q querier) ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error) {
	return listExpiredExecutions(ctx, q.q, now)
}

func listExpiredExecutions(ctx context.Context, q Querier, now time.Time) ([]domain.Execution, error) {
	rows, err := q.Query(ctx, `
		SELECT `+executionColumns+`
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

func (s *Store) ListUnsettledSubagents(ctx context.Context) ([]domain.Execution, error) {
	return listUnsettledSubagents(ctx, s.q(ctx))
}

func (q querier) ListUnsettledSubagents(ctx context.Context) ([]domain.Execution, error) {
	return listUnsettledSubagents(ctx, q.q)
}

func listUnsettledSubagents(ctx context.Context, q Querier) ([]domain.Execution, error) {
	rows, err := q.Query(ctx, `
		SELECT `+executionColumns+`
		FROM executions
		WHERE id IN (
			SELECT c.id
			FROM executions c
			JOIN executions p ON p.id = c.spawned_by_execution_id
			JOIN steps s ON s.step_id = c.spawned_by_step_id
			WHERE (c.status IN ('completed', 'failed', 'cancelled')
			       AND s.status = 'executing' AND p.status NOT IN ('completed', 'failed', 'cancelled'))
			   OR (c.status NOT IN ('completed', 'failed', 'cancelled')
			       AND p.status IN ('completed', 'failed', 'cancelled'))
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("list unsettled subagents: %w", err)
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
	return out, rows.Err()
}

func (s *Store) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	return deleteExecutionsCreatedBefore(ctx, s.q(ctx), before)
}

func (q querier) DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error {
	return deleteExecutionsCreatedBefore(ctx, q.q, before)
}

func deleteExecutionsCreatedBefore(ctx context.Context, q Querier, before time.Time) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM executions e
		WHERE e.created_at < $1
		  AND e.status IN ('completed', 'failed', 'cancelled')
		  AND (e.session IS NULL OR NOT EXISTS (
			SELECT 1 FROM executions kept
			WHERE kept.session = e.session
			  AND (kept.created_at >= $1 OR kept.status NOT IN ('completed', 'failed', 'cancelled'))
		  ))
	`, before); err != nil {
		return fmt.Errorf("delete executions: %w", err)
	}
	return nil
}

func scanExecution(row pgx.Row) (domain.Execution, error) {
	var exec domain.Execution
	var idStr, status string
	var parentID, forkedFrom, spawnedByID, input, output *string
	var spawnedByStep string

	if err := row.Scan(
		&idStr, &exec.AgentID, &input, &status,
		&output, &exec.FailureReason, &exec.CreatedAt, &exec.UpdatedAt, &exec.DeadlineAt,
		&exec.Session, &parentID, &forkedFrom, &exec.ForkSeq, &exec.PolicyBundle, &exec.IdempotencyKey,
		&spawnedByID, &spawnedByStep,
	); err != nil {
		return domain.Execution{}, err
	}
	var err error
	if exec.ParentExecutionID, err = optionalUUID(parentID); err != nil {
		return domain.Execution{}, fmt.Errorf("parse parent execution id: %w", err)
	}
	if exec.ForkedFrom, err = optionalUUID(forkedFrom); err != nil {
		return domain.Execution{}, fmt.Errorf("parse forked_from: %w", err)
	}
	spawnedBy, err := optionalUUID(spawnedByID)
	if err != nil {
		return domain.Execution{}, fmt.Errorf("parse spawned_by: %w", err)
	}
	if spawnedBy != nil {
		exec.SpawnedBy = &domain.SpawnedBy{ExecutionID: *spawnedBy, StepID: spawnedByStep}
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

func uuidArg(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func optionalUUID(s *string) (*uuid.UUID, error) {
	if s == nil {
		return nil, nil
	}
	id, err := parseUUID(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *Store) ListIdleSessions(ctx context.Context, now time.Time) ([]string, error) {
	return listIdleSessions(ctx, s.q(ctx), now)
}

func (q querier) ListIdleSessions(ctx context.Context, now time.Time) ([]string, error) {
	return listIdleSessions(ctx, q.q, now)
}

func listIdleSessions(ctx context.Context, q Querier, now time.Time) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT e.session
		FROM executions e
		WHERE e.status = 'pending'
		  AND e.session IS NOT NULL
		  AND (e.deadline_at IS NULL OR e.deadline_at > $1)
		  AND NOT EXISTS (
			SELECT 1 FROM executions active
			WHERE active.session = e.session
			  AND active.status IN ('running', 'blocked')
		  )
	`, now)
	if err != nil {
		return nil, fmt.Errorf("list idle sessions: %w", err)
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list idle sessions rows: %w", err)
	}
	return sessions, nil
}

func (s *Store) NextPendingInSession(ctx context.Context, session string, now time.Time) (domain.Execution, error) {
	return nextPendingInSession(ctx, s.q(ctx), session, now)
}

func (q querier) NextPendingInSession(ctx context.Context, session string, now time.Time) (domain.Execution, error) {
	return nextPendingInSession(ctx, q.q, session, now)
}

func nextPendingInSession(ctx context.Context, q Querier, session string, now time.Time) (domain.Execution, error) {
	row := q.QueryRow(ctx, `
		SELECT `+executionColumns+`
		FROM executions
		WHERE session = $1
		  AND status = 'pending'
		  AND (deadline_at IS NULL OR deadline_at > $2)
		ORDER BY id
		LIMIT 1
		FOR UPDATE
	`, session, now)
	exec, err := scanExecution(row)
	if err != nil {
		return domain.Execution{}, mapNotFound(err)
	}
	return exec, nil
}

func (s *Store) SessionHead(ctx context.Context, session, agentID string) (domain.Execution, error) {
	return sessionHead(ctx, s.q(ctx), session, agentID)
}

func (q querier) SessionHead(ctx context.Context, session, agentID string) (domain.Execution, error) {
	return sessionHead(ctx, q.q, session, agentID)
}

func sessionHead(ctx context.Context, q Querier, session, agentID string) (domain.Execution, error) {
	row := q.QueryRow(ctx, `
		SELECT `+executionColumns+`
		FROM executions
		WHERE session = $1
		  AND agent_id = $2
		  AND status = 'completed'
		ORDER BY id DESC
		LIMIT 1
	`, session, agentID)
	exec, err := scanExecution(row)
	if err != nil {
		return domain.Execution{}, mapNotFound(err)
	}
	return exec, nil
}

func (s *Store) SetExecutionParent(ctx context.Context, id, parent uuid.UUID) error {
	return setExecutionColumn(ctx, s.q(ctx), id, "parent_execution_id = $2::uuid", parent.String())
}

func (q querier) SetExecutionParent(ctx context.Context, id, parent uuid.UUID) error {
	return setExecutionColumn(ctx, q.q, id, "parent_execution_id = $2::uuid", parent.String())
}

func (s *Store) SetExecutionState(ctx context.Context, id uuid.UUID, state []byte) error {
	return setExecutionState(ctx, s.q(ctx), id, state)
}

func (q querier) SetExecutionState(ctx context.Context, id uuid.UUID, state []byte) error {
	return setExecutionState(ctx, q.q, id, state)
}

func setExecutionState(ctx context.Context, q Querier, id uuid.UUID, state []byte) error {
	chunks, err := writeChunks(ctx, q, state)
	if err != nil {
		return err
	}
	return setExecutionColumn(ctx, q, id, "state = NULL, state_chunks = $2", chunks)
}

func (s *Store) ExecutionState(ctx context.Context, id uuid.UUID) (json.RawMessage, error) {
	return executionState(ctx, s.q(ctx), id)
}

func (q querier) ExecutionState(ctx context.Context, id uuid.UUID) (json.RawMessage, error) {
	return executionState(ctx, q.q, id)
}

func executionState(ctx context.Context, q Querier, id uuid.UUID) (json.RawMessage, error) {
	var inline *string
	var chunks [][]byte
	if err := q.QueryRow(ctx, `SELECT state, state_chunks FROM executions WHERE id = $1`, id.String()).Scan(&inline, &chunks); err != nil {
		return nil, mapNotFound(err)
	}
	if chunks == nil {
		return rawFromPtr(inline), nil
	}
	state, err := readChunks(ctx, q, chunks)
	if err != nil {
		return nil, err
	}
	return state[0], nil
}

func setExecutionColumn(ctx context.Context, q Querier, id uuid.UUID, assignment string, value any) error {
	res, err := q.Exec(ctx, `UPDATE executions SET `+assignment+` WHERE id = $1`, id.String(), value)
	if err != nil {
		if isForeignKeyViolation(err) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("update execution: %w", err)
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
