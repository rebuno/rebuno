package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

type EventStore struct {
	pool *pgxpool.Pool
}

func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{pool: pool}
}

func (s *EventStore) Append(ctx context.Context, event domain.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := s.appendInTx(ctx, tx, event); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *EventStore) AppendBatch(ctx context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, event := range events {
		if err := s.appendInTx(ctx, tx, event); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (s *EventStore) appendInTx(ctx context.Context, tx pgx.Tx, event domain.Event) error {
	var nextSeq int64
	err := tx.QueryRow(ctx,
		`SELECT next_sequence FROM executions WHERE id = $1 FOR UPDATE`,
		event.ExecutionID,
	).Scan(&nextSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("execution %s not found: %w", event.ExecutionID, domain.ErrNotFound)
		}
		return fmt.Errorf("lock execution row: %w", err)
	}

	event.Sequence = nextSeq

	tag, err := tx.Exec(ctx, `
		INSERT INTO events (id, execution_id, step_id, type, schema_version, payload, timestamp, sequence, idempotency_key, causation_id, correlation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (idempotency_key) DO NOTHING
	`,
		event.ID,
		event.ExecutionID,
		nullableText(event.StepID),
		string(event.Type),
		event.SchemaVersion,
		event.Payload,
		event.Timestamp,
		event.Sequence,
		nullableText(event.IdempotencyKey),
		event.CausationID,
		event.CorrelationID,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}

	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE executions SET next_sequence = next_sequence + 1 WHERE id = $1`,
			event.ExecutionID,
		); err != nil {
			return fmt.Errorf("increment next_sequence: %w", err)
		}
	}

	return nil
}

func (s *EventStore) GetByExecution(ctx context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, execution_id, step_id, type, schema_version, payload, timestamp,
		       sequence, idempotency_key, causation_id, correlation_id
		FROM events
		WHERE execution_id = $1 AND sequence > $2
		ORDER BY sequence ASC
		LIMIT $3
	`, executionID, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []domain.Event
	for rows.Next() {
		var e domain.Event
		var stepID, idempKey *string
		if err := rows.Scan(
			&e.ID, &e.ExecutionID, &stepID, &e.Type, &e.SchemaVersion,
			&e.Payload, &e.Timestamp, &e.Sequence, &idempKey,
			&e.CausationID, &e.CorrelationID,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if stepID != nil {
			e.StepID = *stepID
		}
		if idempKey != nil {
			e.IdempotencyKey = *idempKey
		}
		events = append(events, e)
	}

	return events, rows.Err()
}

func (s *EventStore) GetLatestSequence(ctx context.Context, executionID string) (int64, error) {
	var nextSeq int64
	err := s.pool.QueryRow(ctx,
		`SELECT next_sequence FROM executions WHERE id = $1`,
		executionID,
	).Scan(&nextSeq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
		}
		return 0, fmt.Errorf("query latest sequence: %w", err)
	}
	return nextSeq - 1, nil
}

func (s *EventStore) CreateExecution(ctx context.Context, id, agentID string, labels map[string]string) error {
	if labels == nil {
		labels = map[string]string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO executions (id, agent_id, status, next_sequence, labels, created_at, updated_at)
		VALUES ($1, $2, 'pending', 1, $3, now(), now())
	`, id, agentID, labels)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("execution %s already exists: %w", id, domain.ErrConflict)
		}
		return fmt.Errorf("insert execution: %w", err)
	}
	return nil
}

func (s *EventStore) UpdateExecutionStatus(ctx context.Context, executionID string, status domain.ExecutionStatus) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE executions SET status = $1, updated_at = now() WHERE id = $2`,
		string(status), executionID,
	)
	if err != nil {
		return fmt.Errorf("update execution status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
	}
	return nil
}

func (s *EventStore) GetExecution(ctx context.Context, executionID string) (*domain.ExecutionSummary, error) {
	var es domain.ExecutionSummary
	err := s.pool.QueryRow(ctx, `
		SELECT id, status, agent_id, labels, created_at, updated_at
		FROM executions
		WHERE id = $1
	`, executionID).Scan(
		&es.ID, &es.Status, &es.AgentID, &es.Labels,
		&es.CreatedAt, &es.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("query execution: %w", err)
	}
	return &es, nil
}

func (s *EventStore) ListExecutions(ctx context.Context, filter domain.ExecutionFilter, cursor string, limit int) ([]domain.ExecutionSummary, string, error) {
	if limit <= 0 {
		limit = 50
	}

	var args []any
	argIdx := 1
	var conditions []string

	if filter.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(filter.Status))
		argIdx++
	}

	if filter.AgentID != "" {
		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", argIdx))
		args = append(args, filter.AgentID)
		argIdx++
	}

	if len(filter.Labels) > 0 {
		conditions = append(conditions, fmt.Sprintf("labels @> $%d", argIdx))
		args = append(args, filter.Labels)
		argIdx++
	}

	if cursor != "" {
		cursorTime, cursorID, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf(
			"(created_at, id) < ($%d, $%d)", argIdx, argIdx+1,
		))
		args = append(args, cursorTime, cursorID)
		argIdx += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT id, status, agent_id, labels, created_at, updated_at
		FROM executions
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT $%d
	`, where, argIdx)
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("query executions: %w", err)
	}
	defer rows.Close()

	var results []domain.ExecutionSummary
	for rows.Next() {
		var es domain.ExecutionSummary
		if err := rows.Scan(
			&es.ID, &es.Status, &es.AgentID, &es.Labels,
			&es.CreatedAt, &es.UpdatedAt,
		); err != nil {
			return nil, "", fmt.Errorf("scan execution: %w", err)
		}
		results = append(results, es)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate executions: %w", err)
	}

	var nextCursor string
	if len(results) > limit {
		last := results[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		results = results[:limit]
	}

	return results, nextCursor, nil
}

func (s *EventStore) DeleteExecution(ctx context.Context, executionID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM executions WHERE id = $1`,
		executionID,
	)
	if err != nil {
		return fmt.Errorf("delete execution: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
	}
	return nil
}

func (s *EventStore) ListTerminalExecutions(ctx context.Context, olderThanSeconds int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM executions
		WHERE status IN ('completed', 'failed', 'cancelled')
		  AND updated_at < now() - make_interval(secs => $1::float8)
		ORDER BY updated_at ASC
		LIMIT $2
	`, float64(olderThanSeconds), limit)
	if err != nil {
		return nil, fmt.Errorf("query terminal executions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan execution id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *EventStore) ListActiveExecutionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM executions
		WHERE status NOT IN ('completed', 'failed', 'cancelled')
	`)
	if err != nil {
		return nil, fmt.Errorf("query active executions: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan execution id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func nullableText(val string) *string {
	if val == "" {
		return nil
	}
	return &val
}

func encodeCursor(createdAt time.Time, id string) string {
	raw := createdAt.Format(time.RFC3339Nano) + "|" + id
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("base64 decode: %w", err)
	}
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", errors.New("invalid cursor format")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("parse cursor time: %w", err)
	}
	return t, parts[1], nil
}
