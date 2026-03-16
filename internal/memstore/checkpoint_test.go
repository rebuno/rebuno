package memstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestCheckpointStore_SaveAndGet(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	cp := domain.Checkpoint{
		ExecutionID: "exec-1",
		Sequence:    42,
		StateData:   json.RawMessage(`{"key":"value"}`),
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := s.Get(ctx, "exec-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ExecutionID != cp.ExecutionID {
		t.Errorf("ExecutionID: got %q want %q", got.ExecutionID, cp.ExecutionID)
	}
	if got.Sequence != cp.Sequence {
		t.Errorf("Sequence: got %d want %d", got.Sequence, cp.Sequence)
	}
}

func TestCheckpointStore_GetNotFound(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	got, ok, err := s.Get(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("expected found=false")
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestCheckpointStore_SaveOverwrites(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	cp1 := domain.Checkpoint{ExecutionID: "exec-1", Sequence: 1, CreatedAt: time.Now()}
	cp2 := domain.Checkpoint{ExecutionID: "exec-1", Sequence: 2, CreatedAt: time.Now()}

	_ = s.Save(ctx, cp1)
	_ = s.Save(ctx, cp2)

	got, ok, err := s.Get(ctx, "exec-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Sequence != 2 {
		t.Errorf("Sequence: got %d want 2", got.Sequence)
	}
}

func TestCheckpointStore_Delete(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	cp := domain.Checkpoint{ExecutionID: "exec-1", Sequence: 1, CreatedAt: time.Now()}
	_ = s.Save(ctx, cp)

	if err := s.Delete(ctx, "exec-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, err := s.Get(ctx, "exec-1")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected found=false after delete")
	}
}

func TestCheckpointStore_MultipleExecutions(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	cp1 := domain.Checkpoint{ExecutionID: "exec-1", Sequence: 10, CreatedAt: time.Now()}
	cp2 := domain.Checkpoint{ExecutionID: "exec-2", Sequence: 20, CreatedAt: time.Now()}
	_ = s.Save(ctx, cp1)
	_ = s.Save(ctx, cp2)

	got1, ok1, _ := s.Get(ctx, "exec-1")
	got2, ok2, _ := s.Get(ctx, "exec-2")
	if !ok1 || !ok2 {
		t.Fatal("both checkpoints should exist")
	}
	if got1.Sequence != 10 || got2.Sequence != 20 {
		t.Errorf("checkpoints mixed up: got seq %d and %d", got1.Sequence, got2.Sequence)
	}

	// Deleting one should not affect the other
	_ = s.Delete(ctx, "exec-1")
	_, ok1After, _ := s.Get(ctx, "exec-1")
	_, ok2After, _ := s.Get(ctx, "exec-2")
	if ok1After {
		t.Error("exec-1 should be deleted")
	}
	if !ok2After {
		t.Error("exec-2 should still exist")
	}
}

func TestCheckpointStore_GetReturnsCopy(t *testing.T) {
	s := memstore.NewCheckpointStore()
	ctx := context.Background()

	cp := domain.Checkpoint{ExecutionID: "exec-1", Sequence: 1, CreatedAt: time.Now()}
	_ = s.Save(ctx, cp)

	got, _, _ := s.Get(ctx, "exec-1")
	got.Sequence = 999

	got2, _, _ := s.Get(ctx, "exec-1")
	if got2.Sequence != 1 {
		t.Errorf("Get should return a copy; mutation leaked: got %d want 1", got2.Sequence)
	}
}
