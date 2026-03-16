package memstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestRunnerStore_RegisterAndGet(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	r := domain.Runner{
		ID:           "runner-1",
		Name:         "test-runner",
		Capabilities: []string{"cap-a"},
		Status:       domain.RunnerStatusOnline,
		RegisteredAt: time.Now().UTC(),
	}

	if err := s.Register(ctx, r); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok, err := s.Get(ctx, "runner-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ID != "runner-1" {
		t.Errorf("ID: got %q want runner-1", got.ID)
	}
	if got.Name != "test-runner" {
		t.Errorf("Name: got %q want test-runner", got.Name)
	}
}

func TestRunnerStore_GetNotFound(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	got, ok, err := s.Get(ctx, "missing")
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

func TestRunnerStore_RegisterUpserts(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	r1 := domain.Runner{ID: "runner-1", Name: "first", RegisteredAt: time.Now()}
	r2 := domain.Runner{ID: "runner-1", Name: "second", RegisteredAt: time.Now()}

	_ = s.Register(ctx, r1)
	_ = s.Register(ctx, r2)

	got, ok, err := s.Get(ctx, "runner-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Name != "second" {
		t.Errorf("Name: got %q want second", got.Name)
	}
}

func TestRunnerStore_List(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	for _, id := range []string{"r1", "r2", "r3"} {
		_ = s.Register(ctx, domain.Runner{ID: id, RegisteredAt: time.Now()})
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 runners, got %d", len(list))
	}
}

func TestRunnerStore_UpdateHeartbeat(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	r := domain.Runner{
		ID:            "runner-1",
		LastHeartbeat: time.Time{},
		RegisteredAt:  time.Now(),
	}
	_ = s.Register(ctx, r)

	before := time.Now()
	if err := s.UpdateHeartbeat(ctx, "runner-1"); err != nil {
		t.Fatalf("UpdateHeartbeat: %v", err)
	}

	got, _, _ := s.Get(ctx, "runner-1")
	if got.LastHeartbeat.Before(before) {
		t.Errorf("LastHeartbeat not updated: got %v", got.LastHeartbeat)
	}
}

func TestRunnerStore_UpdateHeartbeatMissingIsNoop(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	if err := s.UpdateHeartbeat(ctx, "no-such-runner"); err != nil {
		t.Errorf("expected nil for missing runner, got %v", err)
	}
}

func TestRunnerStore_Delete(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	_ = s.Register(ctx, domain.Runner{ID: "runner-1", RegisteredAt: time.Now()})

	if err := s.Delete(ctx, "runner-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, _ := s.Get(ctx, "runner-1")
	if ok {
		t.Fatal("expected found=false after delete")
	}
}

func TestRunnerStore_ListEmpty(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestRunnerStore_DeleteDoesNotAffectOthers(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	_ = s.Register(ctx, domain.Runner{ID: "r1", Name: "one", RegisteredAt: time.Now()})
	_ = s.Register(ctx, domain.Runner{ID: "r2", Name: "two", RegisteredAt: time.Now()})

	_ = s.Delete(ctx, "r1")

	_, ok1, _ := s.Get(ctx, "r1")
	_, ok2, _ := s.Get(ctx, "r2")
	if ok1 {
		t.Error("r1 should be deleted")
	}
	if !ok2 {
		t.Error("r2 should still exist")
	}
}

func TestRunnerStore_GetReturnsCopy(t *testing.T) {
	s := memstore.NewRunnerStore()
	ctx := context.Background()

	_ = s.Register(ctx, domain.Runner{ID: "r1", Name: "original", RegisteredAt: time.Now()})

	got, _, _ := s.Get(ctx, "r1")
	got.Name = "mutated"

	got2, _, _ := s.Get(ctx, "r1")
	if got2.Name != "original" {
		t.Errorf("Get should return a copy; mutation leaked: got %q want %q", got2.Name, "original")
	}
}
