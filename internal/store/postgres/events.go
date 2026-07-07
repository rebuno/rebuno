package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/store"
)

func (s *Store) Append(ctx context.Context, execID uuid.UUID, eventType string, payload any) (domain.Event, error) {
	events, err := s.AppendBatch(ctx, execID, []store.EventRecord{{Type: eventType, Payload: payload}})
	if err != nil {
		return domain.Event{}, err
	}
	if len(events) == 0 {
		return domain.Event{}, fmt.Errorf("append returned no event")
	}
	return events[0], nil
}

// AppendBatch runs in its own transaction so the per-execution row lock taken in
// appendBatch is held across the SELECT MAX(seq) and the INSERTs. This serializes
// concurrent appenders for the same execution at the database level — including
// the dispatch-delivery path, which appends events without holding the kernel's
// per-execution advisory lock — keeping event_seq contiguous and gap-free.
func (s *Store) AppendBatch(ctx context.Context, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	if len(records) == 0 {
		return nil, nil
	}
	var out []domain.Event
	err := s.RunInTx(ctx, func(tx store.TxStore) error {
		var err error
		out, err = tx.AppendBatch(ctx, execID, records)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (q querier) Append(ctx context.Context, execID uuid.UUID, eventType string, payload any) (domain.Event, error) {
	events, err := appendBatch(ctx, q.q, execID, []store.EventRecord{{Type: eventType, Payload: payload}})
	if err != nil {
		return domain.Event{}, err
	}
	if len(events) == 0 {
		return domain.Event{}, fmt.Errorf("append returned no event")
	}
	return events[0], nil
}

func (q querier) AppendBatch(ctx context.Context, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	return appendBatch(ctx, q.q, execID, records)
}

func appendBatch(ctx context.Context, q Querier, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	if len(records) == 0 {
		return nil, nil
	}

	// Serialize concurrent appenders for this execution by row-locking the parent
	// execution row, then read the current high-water sequence. The lock is held
	// until the surrounding transaction commits (see Store.AppendBatch), so the
	// MAX read and the inserts below cannot interleave with another appender.
	// Note: FOR UPDATE cannot be combined with an aggregate, hence the two steps.
	if _, err := q.Exec(ctx, `SELECT id FROM executions WHERE id = $1 FOR UPDATE`, execID.String()); err != nil {
		return nil, fmt.Errorf("lock execution for event append: %w", err)
	}
	var maxSeq int64
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(MAX(event_seq), 0) FROM events WHERE execution_id = $1
	`, execID.String()).Scan(&maxSeq); err != nil {
		return nil, fmt.Errorf("acquire event sequence: %w", err)
	}
	startSeq := maxSeq + 1

	now := time.Now().UTC()
	out := make([]domain.Event, len(records))
	for i, rec := range records {
		payload, err := marshalPayload(rec.Payload)
		if err != nil {
			return nil, err
		}
		seq := startSeq + int64(i)
		if _, err := q.Exec(ctx, `
			INSERT INTO events (execution_id, event_seq, type, payload, occurred_at)
			VALUES ($1, $2, $3, $4::jsonb, $5)
		`, execID.String(), seq, rec.Type, payload, now); err != nil {
			return nil, fmt.Errorf("insert event: %w", err)
		}
		out[i] = domain.Event{
			ExecutionID: execID,
			EventSeq:    seq,
			Type:        rec.Type,
			Payload:     json.RawMessage(payload),
			OccurredAt:  now,
		}
	}
	return out, nil
}

func (s *Store) GetEvents(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	return getEvents(ctx, s.pool, execID, afterSeq, limit)
}

func (q querier) GetEvents(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	return getEvents(ctx, q.q, execID, afterSeq, limit)
}

func getEvents(ctx context.Context, q Querier, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	query := `
		SELECT event_seq, type, payload, occurred_at
		FROM events
		WHERE execution_id = $1 AND event_seq > $2
		ORDER BY event_seq
	`
	args := []any{execID.String(), afterSeq}
	if limit > 0 {
		query += " LIMIT $3"
		args = append(args, limit)
	}

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows, execID)
}

func scanEvents(rows pgx.Rows, execID uuid.UUID) ([]domain.Event, error) {
	var out []domain.Event
	for rows.Next() {
		var seq int64
		var eventType string
		var payload string
		var occurredAt time.Time
		if err := rows.Scan(&seq, &eventType, &payload, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, domain.Event{
			ExecutionID: execID,
			EventSeq:    seq,
			Type:        eventType,
			Payload:     json.RawMessage(payload),
			OccurredAt:  occurredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return out, nil
}

func (s *Store) GetLatestSequence(ctx context.Context, execID uuid.UUID) (int64, error) {
	return getLatestSequence(ctx, s.pool, execID)
}

func (q querier) GetLatestSequence(ctx context.Context, execID uuid.UUID) (int64, error) {
	return getLatestSequence(ctx, q.q, execID)
}

func getLatestSequence(ctx context.Context, q Querier, execID uuid.UUID) (int64, error) {
	var seq int64
	err := q.QueryRow(ctx, `SELECT COALESCE(MAX(event_seq), 0) FROM events WHERE execution_id = $1`, execID.String()).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("query latest sequence: %w", err)
	}
	return seq, nil
}
