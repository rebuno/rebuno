package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

// Compile-time interface satisfaction checks.
var (
	_ store.EventStore     = (*Store)(nil)
	_ store.StepStore      = (*Store)(nil)
	_ store.ExecutionStore = (*Store)(nil)
	_ store.AgentStore     = (*Store)(nil)
	_ store.ApprovalStore  = (*Store)(nil)
	_ store.JobQueue       = (*Store)(nil)
	_ store.Locker         = (*Store)(nil)
	_ store.UnitOfWork     = (*Store)(nil)

	_ store.TxStore = (*txStore)(nil)
)

func TestNewStore(t *testing.T) {
	if testing.Short() {
		t.Skip("database integration test")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	s := NewStore(pool)

	agent := domain.Agent{
		ID:         "test-agent",
		WebhookURL: "http://localhost:5000/webhook",
		Secret:     "test-secret",
	}
	if err := s.RegisterAgent(ctx, agent); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	// Create an execution and check events commit atomically.
	execID := uuid.Must(uuid.NewV7())
	exec := domain.Execution{
		ID:        execID,
		AgentID:   agent.ID,
		Input:     json.RawMessage(`{"hello":"world"}`),
		Status:    domain.ExecutionPending,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	err = s.RunInTx(ctx, func(tx store.TxStore) error {
		if err := tx.CreateExecution(ctx, exec); err != nil {
			return err
		}
		_, err := tx.AppendBatch(ctx, execID, []store.EventRecord{
			{Type: domain.EventExecutionCreated, Payload: map[string]string{"status": "pending"}},
			{Type: domain.EventExecutionStarted, Payload: map[string]string{"status": "running"}},
		})
		return err
	})
	if err != nil {
		t.Fatalf("run in tx: %v", err)
	}

	got, err := s.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if got.ID != execID {
		t.Errorf("execution id mismatch: got %v", got.ID)
	}

	events, err := s.GetEvents(ctx, execID, 0, 10)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}

	// Lock/unlock smoke test.
	release, err := s.Acquire(ctx, "test-lock")
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	release()
}

func TestMemStoreInterfaces(t *testing.T) {
	// Sanity check that this test file itself compiles in the package and
	// that the in-memory store remains the fallback comparison.
	_ = memstore.NewStore()
}
