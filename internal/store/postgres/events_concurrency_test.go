package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

// testPool connects to the integration database or skips the test. Mirrors the
// gating used by TestNewStore so these run only when DATABASE_URL is set.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedExecution(t *testing.T, ctx context.Context, s *Store) uuid.UUID {
	t.Helper()
	agentID := "evt-test-agent-" + uuid.NewString()
	if err := s.RegisterAgent(ctx, domain.Agent{ID: agentID, WebhookURL: "http://localhost/wh", Secret: "s"}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	execID := uuid.Must(uuid.NewV7())
	if err := s.CreateExecution(ctx, domain.Execution{
		ID: execID, AgentID: agentID, Input: []byte(`{}`), Status: domain.ExecutionRunning,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return execID
}

// TestAppendBatchAssignsSequence verifies a plain append succeeds and hands
// back contiguous sequence numbers starting at 1.
func TestAppendBatchAssignsSequence(t *testing.T) {
	ctx := context.Background()
	s := NewStore(testPool(t))
	execID := seedExecution(t, ctx, s)

	evs, err := s.AppendBatch(ctx, execID, []store.EventRecord{
		{Type: "a", Payload: map[string]string{"n": "1"}},
		{Type: "b", Payload: map[string]string{"n": "2"}},
	})
	if err != nil {
		t.Fatalf("append batch: %v", err)
	}
	if len(evs) != 2 || evs[0].EventSeq != 1 || evs[1].EventSeq != 2 {
		t.Fatalf("unexpected sequences: %+v", evs)
	}
}

// TestConcurrentAppendsStayContiguous verifies many appenders
// hitting the same execution via the pool (no kernel advisory lock, like the
// dispatch-delivery path) must still produce a gap-free, duplicate-free 1..N
// sequence because Store.AppendBatch row-locks the execution for the whole append.
func TestConcurrentAppendsStayContiguous(t *testing.T) {
	ctx := context.Background()
	s := NewStore(testPool(t))
	execID := seedExecution(t, ctx, s)

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Append(ctx, execID, "concurrent", map[string]int{"i": i}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append failed: %v", err)
	}

	events, err := s.GetEvents(ctx, execID, 0, 0)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events) != n {
		t.Fatalf("expected %d events, got %d", n, len(events))
	}
	for idx, ev := range events {
		want := int64(idx + 1)
		if ev.EventSeq != want {
			t.Fatalf("non-contiguous sequence at index %d: got %d want %d", idx, ev.EventSeq, want)
		}
	}
}
