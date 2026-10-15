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

type listStore interface {
	store.AgentStore
	CreateExecution(context.Context, domain.Execution) error
	ListExecutions(context.Context, domain.ExecutionFilter) (domain.ExecutionPage, error)
}

func TestListExecutionsByAgentIDsBackends(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testListExecutionsByAgentIDs(t, memstore.NewStore()) })
	t.Run("postgres", func(t *testing.T) {
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
			t.Fatal(err)
		}
		defer pool.Close()
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		testListExecutionsByAgentIDs(t, NewStore(pool))
	})
}

func testListExecutionsByAgentIDs(t *testing.T, s listStore) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	a, b, c := "a-"+suffix, "b-"+suffix, "c-"+suffix
	for _, id := range []string{a, b, c} {
		if err := s.RegisterAgent(ctx, domain.Agent{ID: id, WebhookURL: "http://a", Secret: "s"}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: id, Status: domain.ExecutionPending, Input: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExecution(ctx, exec); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListExecutions(ctx, domain.ExecutionFilter{AgentIDs: []string{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Executions {
		if e.AgentID != a && e.AgentID != b {
			t.Fatalf("listed an execution of %s", e.AgentID)
		}
	}
	if len(page.Executions) != 2 {
		t.Fatalf("got %d executions, want 2", len(page.Executions))
	}

	page, err = s.ListExecutions(ctx, domain.ExecutionFilter{AgentIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Executions) != 0 {
		t.Fatalf("an empty agent list matched %d executions", len(page.Executions))
	}
}
