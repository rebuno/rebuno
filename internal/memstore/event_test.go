package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func TestEventStore_CreateAndGetExecution(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	if err := s.CreateExecution(ctx, "exec-1", "agent-1", nil); err != nil {
		t.Fatalf("CreateExecution: %v", err)
	}

	got, err := s.GetExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", got.AgentID)
	}
	if got.Status != domain.ExecutionPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestEventStore_GetExecutionNotFound(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_, err := s.GetExecution(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent execution")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestEventStore_AppendAndGetByExecution(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	ev := domain.Event{
		ID:             uuid.New(),
		ExecutionID:    "exec-1",
		Type:           domain.EventExecutionCreated,
		SchemaVersion:  1,
		Timestamp:      time.Now(),
		Payload:        json.RawMessage(`{}`),
		CausationID:    uuid.New(),
		CorrelationID:  uuid.New(),
		IdempotencyKey: "key-1",
	}

	if err := s.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	events, err := s.GetByExecution(ctx, "exec-1", 0, 100)
	if err != nil {
		t.Fatalf("GetByExecution: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", events[0].Sequence)
	}
}

func TestEventStore_AppendIdempotent(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	ev := domain.Event{
		ID:             uuid.New(),
		ExecutionID:    "exec-1",
		Type:           domain.EventExecutionCreated,
		Timestamp:      time.Now(),
		Payload:        json.RawMessage(`{}`),
		IdempotencyKey: "key-1",
	}

	_ = s.Append(ctx, ev)
	_ = s.Append(ctx, ev) // duplicate — should be ignored

	events, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (idempotent)", len(events))
	}
}

func TestEventStore_AppendBatch(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	events := make([]domain.Event, 3)
	for i := range events {
		events[i] = domain.Event{
			ID:             uuid.New(),
			ExecutionID:    "exec-1",
			Type:           domain.EventStepCreated,
			Timestamp:      time.Now(),
			Payload:        json.RawMessage(`{}`),
			IdempotencyKey: fmt.Sprintf("batch-%d", i),
		}
	}

	if err := s.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	got, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
}

func TestEventStore_GetByExecutionPagination(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	for i := range 5 {
		_ = s.Append(ctx, domain.Event{
			ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
			Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
			IdempotencyKey: fmt.Sprintf("key-%d", i),
		})
	}

	// Get after sequence 2, limit 2
	events, _ := s.GetByExecution(ctx, "exec-1", 2, 2)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Sequence != 3 {
		t.Errorf("first event sequence = %d, want 3", events[0].Sequence)
	}
}

func TestEventStore_GetLatestSequence(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	seq, _ := s.GetLatestSequence(ctx, "exec-1")
	if seq != 0 {
		t.Errorf("initial sequence = %d, want 0", seq)
	}

	_ = s.Append(ctx, domain.Event{ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventExecutionCreated, Timestamp: time.Now(), Payload: json.RawMessage(`{}`), IdempotencyKey: "k1"})

	seq, _ = s.GetLatestSequence(ctx, "exec-1")
	if seq != 1 {
		t.Errorf("sequence = %d, want 1", seq)
	}
}

func TestEventStore_UpdateExecutionStatus(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)
	_ = s.UpdateExecutionStatus(ctx, "exec-1", domain.ExecutionRunning)

	got, _ := s.GetExecution(ctx, "exec-1")
	if got.Status != domain.ExecutionRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
}

func TestEventStore_ListActiveExecutionIDs(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "a", nil)
	_ = s.CreateExecution(ctx, "exec-2", "a", nil)
	_ = s.CreateExecution(ctx, "exec-3", "a", nil)
	_ = s.UpdateExecutionStatus(ctx, "exec-2", domain.ExecutionCompleted)

	ids, _ := s.ListActiveExecutionIDs(ctx)
	if len(ids) != 2 {
		t.Fatalf("got %d active, want 2", len(ids))
	}
}

func TestEventStore_ListExecutionsCursorPagination(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	for i := range 5 {
		_ = s.CreateExecution(ctx, fmt.Sprintf("exec-%d", i), "agent-1", nil)
		time.Sleep(time.Millisecond) // ensure distinct timestamps
	}

	// First page
	results, cursor, _ := s.ListExecutions(ctx, domain.ExecutionFilter{}, "", 2)
	if len(results) != 2 {
		t.Fatalf("page 1: got %d, want 2", len(results))
	}
	if cursor == "" {
		t.Fatal("expected cursor for next page")
	}

	// Second page
	results2, _, _ := s.ListExecutions(ctx, domain.ExecutionFilter{}, cursor, 2)
	if len(results2) != 2 {
		t.Fatalf("page 2: got %d, want 2", len(results2))
	}
	if results2[0].ID == results[0].ID {
		t.Error("page 2 returned same results as page 1")
	}
}

func TestEventStore_ListExecutionsFilter(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-a", nil)
	_ = s.CreateExecution(ctx, "exec-2", "agent-b", nil)
	_ = s.UpdateExecutionStatus(ctx, "exec-1", domain.ExecutionRunning)

	// Filter by status
	results, _, _ := s.ListExecutions(ctx, domain.ExecutionFilter{Status: domain.ExecutionRunning}, "", 50)
	if len(results) != 1 || results[0].ID != "exec-1" {
		t.Errorf("status filter: got %v", results)
	}

	// Filter by agent
	results, _, _ = s.ListExecutions(ctx, domain.ExecutionFilter{AgentID: "agent-b"}, "", 50)
	if len(results) != 1 || results[0].ID != "exec-2" {
		t.Errorf("agent filter: got %v", results)
	}
}

func TestEventStore_DeleteExecution(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)
	_ = s.Append(ctx, domain.Event{ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventExecutionCreated, Timestamp: time.Now(), Payload: json.RawMessage(`{}`), IdempotencyKey: "k1"})

	_ = s.DeleteExecution(ctx, "exec-1")

	_, err := s.GetExecution(ctx, "exec-1")
	if err == nil {
		t.Fatal("expected error after delete")
	}

	events, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	if len(events) != 0 {
		t.Fatal("expected events deleted")
	}
}

func TestEventStore_ListTerminalExecutions(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "a", nil)
	_ = s.UpdateExecutionStatus(ctx, "exec-1", domain.ExecutionCompleted)

	// Not old enough
	ids, _ := s.ListTerminalExecutions(ctx, 3600, 10)
	if len(ids) != 0 {
		t.Fatalf("got %d, want 0 (too recent)", len(ids))
	}

	// Any age
	ids, _ = s.ListTerminalExecutions(ctx, 0, 10)
	if len(ids) != 1 {
		t.Fatalf("got %d, want 1", len(ids))
	}
}

func TestEventStore_CreateExecutionDuplicate(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)
	// Second create overwrites — verify it doesn't error and updates agent
	_ = s.CreateExecution(ctx, "exec-1", "agent-2", nil)

	got, err := s.GetExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.AgentID != "agent-2" {
		t.Errorf("AgentID = %q, want agent-2", got.AgentID)
	}
}

func TestEventStore_UpdateExecutionStatusNotFound(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	err := s.UpdateExecutionStatus(ctx, "nonexistent", domain.ExecutionRunning)
	if err == nil {
		t.Fatal("expected error for nonexistent execution")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestEventStore_AppendBatchWithDuplicateKeys(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	// First append
	_ = s.Append(ctx, domain.Event{
		ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
		Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
		IdempotencyKey: "dup-key",
	})

	// Batch with one duplicate and one new
	batch := []domain.Event{
		{ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
			Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
			IdempotencyKey: "dup-key"},
		{ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
			Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
			IdempotencyKey: "new-key"},
	}

	if err := s.AppendBatch(ctx, batch); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	events, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (1 original + 1 new)", len(events))
	}
}

func TestEventStore_DeleteExecutionCleansIdempotencyKeys(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)
	_ = s.Append(ctx, domain.Event{
		ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventExecutionCreated,
		Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
		IdempotencyKey: "reusable-key",
	})

	_ = s.DeleteExecution(ctx, "exec-1")

	// After deletion, the idempotency key should be freed
	_ = s.CreateExecution(ctx, "exec-2", "agent-1", nil)
	err := s.Append(ctx, domain.Event{
		ID: uuid.New(), ExecutionID: "exec-2", Type: domain.EventExecutionCreated,
		Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
		IdempotencyKey: "reusable-key",
	})
	if err != nil {
		t.Fatalf("Append with reused key after delete: %v", err)
	}

	events, _ := s.GetByExecution(ctx, "exec-2", 0, 100)
	if len(events) != 1 {
		t.Fatalf("expected 1 event with reused key, got %d", len(events))
	}
}

func TestEventStore_ListExecutionsInvalidCursor(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_, _, err := s.ListExecutions(ctx, domain.ExecutionFilter{}, "not-valid-base64!!!", 50)
	if err == nil {
		t.Fatal("expected error for invalid cursor")
	}
}

func TestEventStore_ListTerminalExecutionsRespectsLimit(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	for i := range 5 {
		id := fmt.Sprintf("exec-%d", i)
		_ = s.CreateExecution(ctx, id, "agent-1", nil)
		_ = s.UpdateExecutionStatus(ctx, id, domain.ExecutionCompleted)
	}

	ids, _ := s.ListTerminalExecutions(ctx, 0, 2)
	if len(ids) > 2 {
		t.Errorf("expected at most 2 results with limit=2, got %d", len(ids))
	}
}

func TestEventStore_SequenceMonotonicallyIncreases(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	for i := range 10 {
		_ = s.Append(ctx, domain.Event{
			ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
			Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
			IdempotencyKey: fmt.Sprintf("seq-key-%d", i),
		})
	}

	events, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	for i := 1; i < len(events); i++ {
		if events[i].Sequence <= events[i-1].Sequence {
			t.Errorf("sequence not monotonic: events[%d].Sequence=%d <= events[%d].Sequence=%d",
				i, events[i].Sequence, i-1, events[i-1].Sequence)
		}
	}
}

func TestEventStore_AppendWithoutIdempotencyKey(t *testing.T) {
	s := NewEventStore()
	ctx := context.Background()

	_ = s.CreateExecution(ctx, "exec-1", "agent-1", nil)

	ev := domain.Event{
		ID: uuid.New(), ExecutionID: "exec-1", Type: domain.EventStepCreated,
		Timestamp: time.Now(), Payload: json.RawMessage(`{}`),
		// IdempotencyKey intentionally empty
	}

	// Two appends with empty key should both succeed (no dedup)
	_ = s.Append(ctx, ev)
	ev.ID = uuid.New()
	_ = s.Append(ctx, ev)

	events, _ := s.GetByExecution(ctx, "exec-1", 0, 100)
	if len(events) != 2 {
		t.Fatalf("expected 2 events with empty idempotency key, got %d", len(events))
	}
}
