package memstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestSignalStore_PublishAndGetPending(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	sig := domain.Signal{
		ID:          "sig-1",
		ExecutionID: "exec-1",
		SignalType:  "pause",
		Payload:     json.RawMessage(`{}`),
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.Publish(ctx, "exec-1", sig); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	pending, err := s.GetPending(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(pending))
	}
	if pending[0].ID != "sig-1" {
		t.Errorf("ID: got %q want %q", pending[0].ID, "sig-1")
	}
}

func TestSignalStore_GetPendingEmpty(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	pending, err := s.GetPending(ctx, "no-such-exec")
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected empty slice, got %d items", len(pending))
	}
}

func TestSignalStore_GetPendingDoesNotClear(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	sig := domain.Signal{ID: "sig-1", ExecutionID: "exec-1", SignalType: "wake", CreatedAt: time.Now()}
	_ = s.Publish(ctx, "exec-1", sig)

	_, _ = s.GetPending(ctx, "exec-1")

	pending, _ := s.GetPending(ctx, "exec-1")
	if len(pending) != 1 {
		t.Fatalf("GetPending should not clear: expected 1, got %d", len(pending))
	}
}

func TestSignalStore_Clear(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	_ = s.Publish(ctx, "exec-1", domain.Signal{ID: "a", ExecutionID: "exec-1", CreatedAt: time.Now()})
	_ = s.Publish(ctx, "exec-1", domain.Signal{ID: "b", ExecutionID: "exec-1", CreatedAt: time.Now()})

	if err := s.Clear(ctx, "exec-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	pending, err := s.GetPending(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetPending after clear: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected empty after clear, got %d", len(pending))
	}
}

func TestSignalStore_IsolationBetweenExecutions(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	_ = s.Publish(ctx, "exec-1", domain.Signal{ID: "a", ExecutionID: "exec-1", CreatedAt: time.Now()})
	_ = s.Publish(ctx, "exec-2", domain.Signal{ID: "b", ExecutionID: "exec-2", CreatedAt: time.Now()})

	p1, _ := s.GetPending(ctx, "exec-1")
	p2, _ := s.GetPending(ctx, "exec-2")
	if len(p1) != 1 || p1[0].ID != "a" {
		t.Errorf("exec-1 signals: got %v", p1)
	}
	if len(p2) != 1 || p2[0].ID != "b" {
		t.Errorf("exec-2 signals: got %v", p2)
	}

	// Clearing one execution should not affect the other
	_ = s.Clear(ctx, "exec-1")
	p2After, _ := s.GetPending(ctx, "exec-2")
	if len(p2After) != 1 {
		t.Errorf("exec-2 should still have 1 signal after clearing exec-1, got %d", len(p2After))
	}
}

func TestSignalStore_OrderPreserved(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	ids := []string{"first", "second", "third"}
	for _, id := range ids {
		_ = s.Publish(ctx, "exec-1", domain.Signal{ID: id, ExecutionID: "exec-1", CreatedAt: time.Now()})
	}

	pending, _ := s.GetPending(ctx, "exec-1")
	for i, id := range ids {
		if pending[i].ID != id {
			t.Errorf("position %d: got %q want %q", i, pending[i].ID, id)
		}
	}
}

func TestSignalStore_GetPendingReturnsCopy(t *testing.T) {
	s := memstore.NewSignalStore()
	ctx := context.Background()

	_ = s.Publish(ctx, "exec-1", domain.Signal{ID: "a", ExecutionID: "exec-1", CreatedAt: time.Now()})

	p1, _ := s.GetPending(ctx, "exec-1")
	p1[0].ID = "mutated"

	p2, _ := s.GetPending(ctx, "exec-1")
	if p2[0].ID != "a" {
		t.Errorf("GetPending should return a copy; mutation leaked: got %q want %q", p2[0].ID, "a")
	}
}
