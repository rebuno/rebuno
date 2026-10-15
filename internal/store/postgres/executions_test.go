package postgres

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

type executionTestStore interface {
	store.AgentStore
	CreateExecution(context.Context, domain.Execution) error
	GetExecution(context.Context, uuid.UUID) (domain.Execution, error)
	ListExecutions(context.Context, domain.ExecutionFilter) (domain.ExecutionPage, error)
}

func executionBackends(t *testing.T, run func(t *testing.T, s executionTestStore)) {
	t.Run("memory", func(t *testing.T) { run(t, memstore.NewStore()) })
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
		run(t, NewStore(pool))
	})
}

func createTestExecution(t *testing.T, s executionTestStore, agentID string, edit func(*domain.Execution)) domain.Execution {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterAgent(ctx, domain.Agent{ID: agentID, WebhookURL: "http://a", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: agentID, Status: domain.ExecutionPending, Input: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
	if edit != nil {
		edit(&exec)
	}
	if err := s.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	return exec
}

func TestListExecutionsByAgentIDsBackends(t *testing.T) {
	executionBackends(t, func(t *testing.T, s executionTestStore) {
		ctx := context.Background()
		suffix := uuid.NewString()
		a, b, c := "a-"+suffix, "b-"+suffix, "c-"+suffix
		for _, id := range []string{a, b, c} {
			createTestExecution(t, s, id, nil)
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
	})
}

func TestExecutionSubjectRoundTripBackends(t *testing.T) {
	executionBackends(t, func(t *testing.T, s executionTestStore) {
		exec := createTestExecution(t, s, "subject-"+uuid.NewString(), func(e *domain.Execution) {
			e.CreatedBy = "service:slackbot"
			e.OnBehalfOf = "user:alice@corp.com"
			e.SubjectGroups = []string{"support", "oncall"}
		})
		got, err := s.GetExecution(context.Background(), exec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.CreatedBy != exec.CreatedBy || got.OnBehalfOf != exec.OnBehalfOf || !slices.Equal(got.SubjectGroups, exec.SubjectGroups) {
			t.Fatalf("got created_by %q on_behalf_of %q groups %v", got.CreatedBy, got.OnBehalfOf, got.SubjectGroups)
		}
	})
}
