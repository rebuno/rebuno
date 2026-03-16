package memstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestSessionStore_CreateAndGet(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:          "sess-1",
		ExecutionID: "exec-1",
		AgentID:     "agent-1",
		ConsumerID:  "consumer-1",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().Add(time.Hour).UTC(),
	}

	if err := s.Create(ctx, sess); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, ok, err := s.Get(ctx, "sess-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ID != "sess-1" {
		t.Errorf("ID: got %q want sess-1", got.ID)
	}
	if got.ExecutionID != "exec-1" {
		t.Errorf("ExecutionID: got %q want exec-1", got.ExecutionID)
	}
}

func TestSessionStore_GetNotFound(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	got, ok, err := s.Get(ctx, "nope")
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

func TestSessionStore_GetByExecution(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:          "sess-1",
		ExecutionID: "exec-1",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	_ = s.Create(ctx, sess)

	got, ok, err := s.GetByExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("GetByExecution: %v", err)
	}
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ID != "sess-1" {
		t.Errorf("ID: got %q want sess-1", got.ID)
	}
}

func TestSessionStore_GetByExecutionNotFound(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	got, ok, err := s.GetByExecution(ctx, "no-exec")
	if err != nil {
		t.Fatalf("GetByExecution: %v", err)
	}
	if ok {
		t.Fatal("expected found=false")
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSessionStore_Extend(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:        "sess-1",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Minute),
	}
	_ = s.Create(ctx, sess)

	before := time.Now()
	if err := s.Extend(ctx, "sess-1", 2*time.Hour); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	got, _, _ := s.Get(ctx, "sess-1")
	expected := before.Add(2 * time.Hour)
	if got.ExpiresAt.Before(expected) {
		t.Errorf("ExpiresAt not extended: got %v, want >= %v", got.ExpiresAt, expected)
	}
}

func TestSessionStore_ExtendMissingIsNoop(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	if err := s.Extend(ctx, "missing", time.Hour); err != nil {
		t.Errorf("expected nil for missing session, got %v", err)
	}
}

func TestSessionStore_Delete(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:          "sess-1",
		ExecutionID: "exec-1",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	_ = s.Create(ctx, sess)

	if err := s.Delete(ctx, "sess-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, okByID, _ := s.Get(ctx, "sess-1")
	if okByID {
		t.Error("expected not found by ID after delete")
	}

	_, okByExec, _ := s.GetByExecution(ctx, "exec-1")
	if okByExec {
		t.Error("expected not found by execution after delete")
	}
}

func TestSessionStore_DeleteExpired(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	// Already expired (1 hour ago)
	expired := domain.Session{
		ID:          "sess-expired",
		ExecutionID: "exec-expired",
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	// Not yet expired
	active := domain.Session{
		ID:          "sess-active",
		ExecutionID: "exec-active",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	_ = s.Create(ctx, expired)
	_ = s.Create(ctx, active)

	// Grace period of 0 — expired session should be removed
	n, err := s.DeleteExpired(ctx, 0)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deleted, got %d", n)
	}

	_, okExpired, _ := s.Get(ctx, "sess-expired")
	if okExpired {
		t.Error("expired session should have been removed")
	}

	_, okActive, _ := s.Get(ctx, "sess-active")
	if !okActive {
		t.Error("active session should remain")
	}
}

func TestSessionStore_CreateWithEmptyExecutionID(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:        "sess-no-exec",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	_ = s.Create(ctx, sess)

	got, ok, _ := s.Get(ctx, "sess-no-exec")
	if !ok {
		t.Fatal("expected found=true")
	}
	if got.ExecutionID != "" {
		t.Errorf("ExecutionID should be empty, got %q", got.ExecutionID)
	}

	// GetByExecution with empty string should not find it
	_, okExec, _ := s.GetByExecution(ctx, "")
	if okExec {
		t.Error("should not find session with empty execution ID via GetByExecution")
	}
}

func TestSessionStore_DeleteExpiredWithGracePeriod(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	// Expired 30 seconds ago
	recent := domain.Session{
		ID:          "sess-recent",
		ExecutionID: "exec-recent",
		CreatedAt:   time.Now().Add(-time.Minute),
		ExpiresAt:   time.Now().Add(-30 * time.Second),
	}
	// Expired 2 hours ago
	old := domain.Session{
		ID:          "sess-old",
		ExecutionID: "exec-old",
		CreatedAt:   time.Now().Add(-3 * time.Hour),
		ExpiresAt:   time.Now().Add(-2 * time.Hour),
	}

	_ = s.Create(ctx, recent)
	_ = s.Create(ctx, old)

	// Grace period of 1 hour — only the 2-hour-old expired session should be removed
	n, err := s.DeleteExpired(ctx, time.Hour)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 deleted, got %d", n)
	}

	_, okRecent, _ := s.Get(ctx, "sess-recent")
	if !okRecent {
		t.Error("recently expired session should be preserved by grace period")
	}

	_, okOld, _ := s.Get(ctx, "sess-old")
	if okOld {
		t.Error("old expired session should have been removed")
	}
}

func TestSessionStore_DeleteExpiredCleansExecIndex(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	expired := domain.Session{
		ID:          "sess-1",
		ExecutionID: "exec-1",
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	_ = s.Create(ctx, expired)

	_, _ = s.DeleteExpired(ctx, 0)

	_, okExec, _ := s.GetByExecution(ctx, "exec-1")
	if okExec {
		t.Error("execution index should be cleaned up after DeleteExpired")
	}
}

func TestSessionStore_GetReturnsCopy(t *testing.T) {
	s := memstore.NewSessionStore()
	ctx := context.Background()

	sess := domain.Session{
		ID:          "sess-1",
		ExecutionID: "exec-1",
		AgentID:     "agent-1",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	_ = s.Create(ctx, sess)

	got, _, _ := s.Get(ctx, "sess-1")
	got.AgentID = "mutated"

	got2, _, _ := s.Get(ctx, "sess-1")
	if got2.AgentID != "agent-1" {
		t.Errorf("Get should return a copy; mutation leaked: got %q want %q", got2.AgentID, "agent-1")
	}
}
