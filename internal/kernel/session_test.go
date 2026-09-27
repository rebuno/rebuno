package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
	"github.com/rebuno/rebuno/internal/store/postgres"
)

type sessionStore interface {
	store.APIKeyStore
	store.EventStore
	store.StepStore
	store.ExecutionStore
	store.AgentStore
	store.ApprovalStore
	store.JobQueue
	store.Locker
	store.UnitOfWork
}

func sessionKernels(t *testing.T, backend string) (*kernel.Kernel, func() *kernel.Kernel, context.Context) {
	t.Helper()
	ctx := auth.WithAdmin(t.Context())
	memory := memstore.NewStore()
	newStore := func() sessionStore { return memory }
	if backend == "postgres" {
		if testing.Short() || os.Getenv("DATABASE_URL") == "" {
			t.Skip("requires DATABASE_URL without -short")
		}
		cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		admin, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		schema := pgx.Identifier{"session_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			defer admin.Close()
			if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		cfg.MaxConns = 16
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		newStore = func() sessionStore { return postgres.NewStore(pool) }
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(server.Close)
	replica := func() *kernel.Kernel {
		s := newStore()
		cfg := kernel.DefaultConfig()
		cfg.ReplicaID = uuid.NewString()
		return kernel.New(cfg, kernel.Deps{APIKeys: s, Events: s, Steps: s, Executions: s, Agents: s, Approvals: s, Queue: s, Locker: s, UnitOfWork: s})
	}
	k := replica()
	for _, agent := range []string{"agent-1", "agent-2"} {
		if err := k.RegisterAgent(ctx, domain.Agent{ID: agent, WebhookURL: server.URL, Secret: "secret"}); err != nil {
			t.Fatal(err)
		}
	}
	return k, replica, ctx
}

func createInSession(t *testing.T, k *kernel.Kernel, ctx context.Context, agent, session string) domain.Execution {
	t.Helper()
	exec, err := k.CreateExecution(ctx, agent, json.RawMessage(`{}`), kernel.CreateExecutionOptions{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func requireExecutionStatus(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, status domain.ExecutionStatus) {
	t.Helper()
	exec, err := k.GetExecution(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != status {
		t.Fatalf("execution %s: want %s, got %s", id, status, exec.Status)
	}
}

func drainSessions(t *testing.T, k *kernel.Kernel, ctx context.Context) {
	t.Helper()
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSerializesAcrossAgents(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			first := createInSession(t, k, ctx, "agent-1", "session-a")
			second := createInSession(t, k, ctx, "agent-2", "session-a")
			third := createInSession(t, k, ctx, "agent-1", "session-a")
			independent := createInSession(t, k, ctx, "agent-1", "session-b")
			if first.Status != domain.ExecutionRunning || second.Status != domain.ExecutionPending {
				t.Fatalf("create returned %s and %s", first.Status, second.Status)
			}
			events, err := k.GetEvents(ctx, first.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 || events[0].Type != domain.EventExecutionCreated {
				t.Fatalf("creation events: %+v", events)
			}
			var payload struct {
				Session string `json:"session"`
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload.Session != "session-a" {
				t.Fatalf("creation session: %+v, %v", payload, err)
			}
			drainSessions(t, k, ctx)
			requireExecutionStatus(t, k, ctx, first.ID, domain.ExecutionRunning)
			requireExecutionStatus(t, k, ctx, independent.ID, domain.ExecutionRunning)
			for _, exec := range []domain.Execution{second, third} {
				requireExecutionStatus(t, k, ctx, exec.ID, domain.ExecutionPending)
				ds, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
				if err != nil || len(ds) != 0 {
					t.Fatalf("waiter dispatches: %v, %v", ds, err)
				}
			}
			page, err := k.ListExecutions(ctx, domain.ExecutionFilter{Session: "session-a"})
			if err != nil || len(page.Executions) != 3 {
				t.Fatalf("session filter: %+v, %v", page, err)
			}
			if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{}`), nil); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionRunning)
			requireExecutionStatus(t, k, ctx, third.ID, domain.ExecutionPending)
			drainSessions(t, k, ctx)
			if err := k.FailExecution(ctx, second.ID, leaseOf(t, k, second.ID), "failed"); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, third.ID, domain.ExecutionRunning)
			events, err = k.GetEvents(ctx, third.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, event := range events {
				if event.Type == domain.EventExecutionStarted {
					starts++
				}
			}
			if starts != 1 {
				t.Fatalf("started %d times", starts)
			}
		})
	}
}

func TestSessionRetainsOwnershipAcrossApproval(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			base, _, ctx := sessionKernels(t, backend)
			deps := base.Deps()
			deps.Policy = approvalPolicy()
			k := kernel.New(kernel.DefaultConfig(), deps)
			first := createInSession(t, k, ctx, "agent-1", "session")
			second := createInSession(t, k, ctx, "agent-1", "session")
			drainSessions(t, k, ctx)
			decision, err := k.SubmitStep(ctx, first.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "fs_write", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, first.ID)})
			if err != nil || decision.ApprovalID == nil {
				t.Fatalf("approval: %+v, %v", decision, err)
			}
			drainSessions(t, k, ctx)
			requireExecutionStatus(t, k, ctx, first.ID, domain.ExecutionBlocked)
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionPending)
			if err := k.GrantApproval(ctx, *decision.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "test"}); err != nil {
				t.Fatal(err)
			}
			drainSessions(t, k, ctx)
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionPending)
			if err := k.CancelExecution(ctx, first.ID); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionRunning)
		})
	}
}

func TestSessionCancellationAndDeadlines(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			first := createInSession(t, k, ctx, "agent-1", "session")
			cancelled := createInSession(t, k, ctx, "agent-1", "session")
			past := time.Now().Add(-time.Hour)
			expired := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: "agent-1", Session: "session", Status: domain.ExecutionPending, Input: json.RawMessage(`{}`), DeadlineAt: &past}
			if err := k.Deps().Executions.CreateExecution(ctx, expired); err != nil {
				t.Fatal(err)
			}
			next := createInSession(t, k, ctx, "agent-1", "session")
			drainSessions(t, k, ctx)
			if err := k.CancelExecution(ctx, cancelled.ID); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, first.ID, domain.ExecutionRunning)
			if err := k.CancelExecution(ctx, first.ID); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, next.ID, domain.ExecutionRunning)
			if err := k.CancelExpiredExecutions(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			got, err := k.GetExecution(ctx, expired.ID)
			if err != nil || got.Status != domain.ExecutionCancelled || got.FailureReason != domain.ReasonDeadlineExceeded {
				t.Fatalf("expired: %+v, %v", got, err)
			}
			ds, err := k.Deps().Queue.ListDispatchesByExecution(ctx, expired.ID)
			if err != nil || len(ds) != 0 {
				t.Fatalf("expired dispatches: %+v, %v", ds, err)
			}
		})
	}
}

func TestSessionAdmissionAcrossReplicas(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, replica, ctx := sessionKernels(t, backend)
			const count = 12
			var wg sync.WaitGroup
			for range count {
				wg.Go(func() {
					if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{Session: "session"}); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			replicas := []*kernel.Kernel{k, replica(), replica()}
			for _, r := range replicas {
				wg.Go(func() {
					if err := r.DrainDispatches(ctx); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			page, err := k.ListExecutions(ctx, domain.ExecutionFilter{Session: "session"})
			if err != nil || len(page.Executions) != count {
				t.Fatalf("executions: %+v, %v", page, err)
			}
			var running domain.Execution
			active := 0
			for _, exec := range page.Executions {
				if exec.Status == domain.ExecutionRunning {
					running = exec
					active++
				}
			}
			if active != 1 {
				t.Fatalf("running=%d", active)
			}
			ds, err := k.Deps().Queue.ListDispatchesByExecution(ctx, running.ID)
			if err != nil || len(ds) != 1 || ds[0].Attempt != 1 {
				t.Fatalf("dispatches: %+v, %v", ds, err)
			}
		})
	}
}

type admissionFault struct {
	store.UnitOfWork
	fail atomic.Bool
}
type admissionFaultTx struct{ store.TxStore }

func (f *admissionFault) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	return f.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if f.fail.Load() {
			return fn(admissionFaultTx{tx})
		}
		return fn(tx)
	})
}
func (admissionFaultTx) Enqueue(context.Context, domain.Dispatch) error {
	return fmt.Errorf("dispatch insert failed")
}

// Postgres only: memstore transactions do not roll back.
func TestSessionAdmissionRollback(t *testing.T) {
	k, _, ctx := sessionKernels(t, "postgres")
	deps := k.Deps()
	fault := &admissionFault{UnitOfWork: deps.UnitOfWork}
	fault.fail.Store(true)
	deps.UnitOfWork = fault
	failing := kernel.New(kernel.DefaultConfig(), deps)
	exec := createInSession(t, failing, ctx, "agent-1", "session")
	requireExecutionStatus(t, k, ctx, exec.ID, domain.ExecutionPending)
	events, err := k.GetEvents(ctx, exec.ID, 0, 100)
	if err != nil || len(events) != 1 || events[0].Type != domain.EventExecutionCreated {
		t.Fatalf("rollback events: %+v, %v", events, err)
	}
	ds, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil || len(ds) != 0 {
		t.Fatalf("rollback dispatches: %+v, %v", ds, err)
	}
	fault.fail.Store(false)
	if err := k.AdmitQueued(ctx); err != nil {
		t.Fatal(err)
	}
	requireExecutionStatus(t, k, ctx, exec.ID, domain.ExecutionRunning)
}

func TestSessionRetentionPreservesNonterminalExecutions(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			ids := make(map[domain.ExecutionStatus]uuid.UUID)
			for _, status := range []domain.ExecutionStatus{domain.ExecutionPending, domain.ExecutionRunning, domain.ExecutionBlocked, domain.ExecutionCompleted, domain.ExecutionFailed, domain.ExecutionCancelled} {
				exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: "agent-1", Input: json.RawMessage(`{}`), Session: string(status), Status: domain.ExecutionPending, CreatedAt: time.Now().Add(-48 * time.Hour)}
				if err := k.Deps().Executions.CreateExecution(ctx, exec); err != nil {
					t.Fatal(err)
				}
				if status != domain.ExecutionPending {
					if err := k.Deps().Executions.UpdateExecutionStatus(ctx, exec.ID, status, nil, ""); err != nil {
						t.Fatal(err)
					}
				}
				ids[status] = exec.ID
			}
			if err := k.Cleanup(ctx, 24*time.Hour, time.Now()); err != nil {
				t.Fatal(err)
			}
			for status, id := range ids {
				_, err := k.GetExecution(ctx, id)
				if status.IsTerminal() {
					if !errors.Is(err, domain.ErrNotFound) {
						t.Fatalf("retained %s: %v", status, err)
					}
				} else if err != nil {
					t.Fatalf("deleted %s: %v", status, err)
				}
			}
		})
	}
}

func TestSessionContinuesAgentHead(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			first := createInSession(t, k, ctx, "agent-1", "chat")
			if prev, err := k.PreviousState(ctx, first.ID); err != nil || prev != nil {
				t.Fatalf("first previous: %s, %v", prev, err)
			}
			if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{"answer":1}`), json.RawMessage(`{"turns":1}`)); err != nil {
				t.Fatal(err)
			}
			failed := createInSession(t, k, ctx, "agent-1", "chat")
			if failed.ParentExecutionID == nil || *failed.ParentExecutionID != first.ID {
				t.Fatalf("second parent: %v", failed.ParentExecutionID)
			}
			if err := k.FailExecution(ctx, failed.ID, leaseOf(t, k, failed.ID), "failed"); err != nil {
				t.Fatal(err)
			}
			third := createInSession(t, k, ctx, "agent-1", "chat")
			if third.ParentExecutionID == nil || *third.ParentExecutionID != first.ID {
				t.Fatalf("a failed execution became the head: %v", third.ParentExecutionID)
			}
			requirePreviousState(t, k, ctx, third.ID, `{"turns":1}`)
			events, err := k.GetEvents(ctx, third.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var started struct {
				ParentExecutionID string `json:"parent_execution_id"`
			}
			for _, event := range events {
				if event.Type == domain.EventExecutionStarted {
					if err := json.Unmarshal(event.Payload, &started); err != nil {
						t.Fatal(err)
					}
				}
			}
			if started.ParentExecutionID != first.ID.String() {
				t.Fatalf("started payload parent: %q", started.ParentExecutionID)
			}
			if err := k.CompleteExecution(ctx, third.ID, leaseOf(t, k, third.ID), json.RawMessage(`{"answer":3}`), nil); err != nil {
				t.Fatal(err)
			}
			other := createInSession(t, k, ctx, "agent-2", "chat")
			if other.ParentExecutionID != nil {
				t.Fatalf("another agent's execution became the parent: %v", other.ParentExecutionID)
			}
			if err := k.CompleteExecution(ctx, other.ID, leaseOf(t, k, other.ID), json.RawMessage(`{}`), nil); err != nil {
				t.Fatal(err)
			}
			fourth := createInSession(t, k, ctx, "agent-1", "chat")
			requirePreviousState(t, k, ctx, fourth.ID, `{"answer":3}`)
		})
	}
}

func requirePreviousState(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, want string) {
	t.Helper()
	raw, err := k.PreviousState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got, expected any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("previous state %s: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("previous state: want %s, got %s", want, raw)
	}
}

func TestSessionRetentionKeepsAncestors(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			old := time.Now().Add(-48 * time.Hour)
			create := func(session string, createdAt time.Time) uuid.UUID {
				exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: "agent-1", Input: json.RawMessage(`{}`), Session: session, Status: domain.ExecutionPending, CreatedAt: createdAt}
				if err := k.Deps().Executions.CreateExecution(ctx, exec); err != nil {
					t.Fatal(err)
				}
				if err := k.Deps().Executions.UpdateExecutionStatus(ctx, exec.ID, domain.ExecutionCompleted, nil, ""); err != nil {
					t.Fatal(err)
				}
				return exec.ID
			}
			ancestor := create("active", old)
			recent := create("active", time.Now())
			if err := k.Deps().Executions.SetExecutionParent(ctx, recent, ancestor); err != nil {
				t.Fatal(err)
			}
			expired := create("expired", old)
			if err := k.Cleanup(ctx, 24*time.Hour, time.Now()); err != nil {
				t.Fatal(err)
			}
			for _, id := range []uuid.UUID{ancestor, recent} {
				if _, err := k.GetExecution(ctx, id); err != nil {
					t.Fatalf("session with a recent execution lost %s: %v", id, err)
				}
			}
			if _, err := k.GetExecution(ctx, expired); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expired session kept: %v", err)
			}
		})
	}
}
