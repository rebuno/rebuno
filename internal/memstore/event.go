package memstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type EventStore struct {
	mu          sync.RWMutex
	events      map[string][]domain.Event
	executions  map[string]domain.ExecutionSummary
	sequences   map[string]int64
	idempotency map[string]bool
}

func NewEventStore() *EventStore {
	return &EventStore{
		events:      make(map[string][]domain.Event),
		executions:  make(map[string]domain.ExecutionSummary),
		sequences:   make(map[string]int64),
		idempotency: make(map[string]bool),
	}
}

func (s *EventStore) Append(_ context.Context, event domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(event)
}

func (s *EventStore) AppendBatch(_ context.Context, events []domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range events {
		if err := s.appendLocked(ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *EventStore) appendLocked(event domain.Event) error {
	if event.IdempotencyKey != "" && s.idempotency[event.IdempotencyKey] {
		return nil
	}

	s.sequences[event.ExecutionID]++
	event.Sequence = s.sequences[event.ExecutionID]

	s.events[event.ExecutionID] = append(s.events[event.ExecutionID], event)

	if event.IdempotencyKey != "" {
		s.idempotency[event.IdempotencyKey] = true
	}
	return nil
}

func (s *EventStore) GetByExecution(_ context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := s.events[executionID]
	var result []domain.Event
	for _, ev := range all {
		if ev.Sequence > afterSequence {
			result = append(result, ev)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *EventStore) GetLatestSequence(_ context.Context, executionID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sequences[executionID], nil
}

func (s *EventStore) CreateExecution(_ context.Context, id, agentID string, labels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if labels == nil {
		labels = map[string]string{}
	}
	now := time.Now()
	s.executions[id] = domain.ExecutionSummary{
		ID:        id,
		Status:    domain.ExecutionPending,
		AgentID:   agentID,
		Labels:    labels,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return nil
}

func (s *EventStore) UpdateExecutionStatus(_ context.Context, executionID string, status domain.ExecutionStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exec, ok := s.executions[executionID]
	if !ok {
		return fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
	}
	exec.Status = status
	exec.UpdatedAt = time.Now()
	s.executions[executionID] = exec
	return nil
}

func (s *EventStore) GetExecution(_ context.Context, executionID string) (*domain.ExecutionSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exec, ok := s.executions[executionID]
	if !ok {
		return nil, fmt.Errorf("execution %s not found: %w", executionID, domain.ErrNotFound)
	}
	return &exec, nil
}

func (s *EventStore) ListExecutions(_ context.Context, filter domain.ExecutionFilter, cursor string, limit int) ([]domain.ExecutionSummary, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 50
	}

	var all []domain.ExecutionSummary
	for _, exec := range s.executions {
		if filter.Status != "" && exec.Status != filter.Status {
			continue
		}
		if filter.AgentID != "" && exec.AgentID != filter.AgentID {
			continue
		}
		if len(filter.Labels) > 0 {
			match := true
			for k, v := range filter.Labels {
				if exec.Labels[k] != v {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		all = append(all, exec)
	}

	// Sort by created_at DESC, then ID DESC for stable ordering
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	if cursor != "" {
		cursorTime, cursorID, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %w", err)
		}
		idx := 0
		for idx < len(all) {
			e := all[idx]
			if e.CreatedAt.Before(cursorTime) || (e.CreatedAt.Equal(cursorTime) && e.ID <= cursorID) {
				break
			}
			idx++
		}
		all = all[idx:]
	}

	var nextCursor string
	if len(all) > limit {
		last := all[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		all = all[:limit]
	}

	return all, nextCursor, nil
}

func (s *EventStore) ListActiveExecutionIDs(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, exec := range s.executions {
		if !exec.Status.IsTerminal() {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *EventStore) DeleteExecution(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ev := range s.events[executionID] {
		if ev.IdempotencyKey != "" {
			delete(s.idempotency, ev.IdempotencyKey)
		}
	}

	delete(s.events, executionID)
	delete(s.executions, executionID)
	delete(s.sequences, executionID)
	return nil
}

func (s *EventStore) ListTerminalExecutions(_ context.Context, olderThanSeconds int64, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(olderThanSeconds) * time.Second)
	var ids []string
	for id, exec := range s.executions {
		if exec.Status.IsTerminal() && !exec.UpdatedAt.After(cutoff) {
			ids = append(ids, id)
			if len(ids) >= limit {
				break
			}
		}
	}
	return ids, nil
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
