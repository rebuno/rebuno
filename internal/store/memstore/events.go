package memstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/store"
)

func (s *Store) Append(ctx context.Context, execID uuid.UUID, eventType string, payload any) (domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, err := s.nextSeqLocked(ctx, execID)
	if err != nil {
		return domain.Event{}, err
	}
	return s.appendLocked(ctx, execID, seq, eventType, payload)
}

func (s *Store) AppendBatch(ctx context.Context, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendBatchLocked(ctx, execID, records)
}

func (s *Store) appendBatchLocked(ctx context.Context, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	seq, err := s.nextSeqLocked(ctx, execID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Event, 0, len(records))
	for _, r := range records {
		ev, err := s.appendLocked(ctx, execID, seq, r.Type, r.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
		seq++
	}
	return out, nil
}

func (s *Store) nextSeqLocked(ctx context.Context, execID uuid.UUID) (int64, error) {
	log := s.events[execID]
	if len(log) == 0 {
		return 1, nil
	}
	return log[len(log)-1].EventSeq + 1, nil
}

func (s *Store) appendLocked(ctx context.Context, execID uuid.UUID, seq int64, eventType string, payload any) (domain.Event, error) {
	if ctx.Err() != nil {
		return domain.Event{}, ctx.Err()
	}
	var raw json.RawMessage
	switch v := payload.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case nil:
		raw = json.RawMessage("{}")
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return domain.Event{}, fmt.Errorf("marshal payload: %w", err)
		}
		raw = b
	}
	ev := domain.Event{
		ExecutionID: execID,
		EventSeq:    seq,
		Type:        eventType,
		Payload:     raw,
		OccurredAt:  time.Now().UTC(),
	}
	s.events[execID] = append(s.events[execID], ev)
	return ev, nil
}

func (s *Store) GetEvents(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getEventsLocked(ctx, execID, afterSeq, limit)
}

func (s *Store) getEventsLocked(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	log := s.events[execID]
	var out []domain.Event
	for _, ev := range log {
		if ev.EventSeq > afterSeq {
			out = append(out, ev)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *Store) GetLatestSequence(ctx context.Context, execID uuid.UUID) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	log := s.events[execID]
	if len(log) == 0 {
		return 0, nil
	}
	return log[len(log)-1].EventSeq, nil
}

func (tx *txStore) Append(ctx context.Context, execID uuid.UUID, eventType string, payload any) (domain.Event, error) {
	seq, err := tx.nextSeqLocked(ctx, execID)
	if err != nil {
		return domain.Event{}, err
	}
	return tx.appendLocked(ctx, execID, seq, eventType, payload)
}

func (tx *txStore) AppendBatch(ctx context.Context, execID uuid.UUID, records []store.EventRecord) ([]domain.Event, error) {
	return tx.appendBatchLocked(ctx, execID, records)
}

func (tx *txStore) GetEvents(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	return tx.getEventsLocked(ctx, execID, afterSeq, limit)
}

func (tx *txStore) GetLatestSequence(ctx context.Context, execID uuid.UUID) (int64, error) {
	log := tx.events[execID]
	if len(log) == 0 {
		return 0, nil
	}
	return log[len(log)-1].EventSeq, nil
}
