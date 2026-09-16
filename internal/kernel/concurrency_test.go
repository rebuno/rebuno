package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

type concurrencyStore interface {
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

func concurrencyKernels(t *testing.T, backend string) (*kernel.Kernel, func() *kernel.Kernel, context.Context) {
	t.Helper()
	ctx := auth.WithAdmin(t.Context())
	memory := memstore.NewStore()
	newStore := func() concurrencyStore { return memory }
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
		schema := pgx.Identifier{"concurrency_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
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
		newStore = func() concurrencyStore { return postgres.NewStore(pool) }
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

func createKeyed(t *testing.T, k *kernel.Kernel, ctx context.Context, agent, key string) domain.Execution {
	t.Helper()
	exec, err := k.CreateExecution(ctx, agent, json.RawMessage(`{}`), kernel.CreateExecutionOptions{ConcurrencyKey: key})
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

func drainKeyed(t *testing.T, k *kernel.Kernel, ctx context.Context) {
	t.Helper()
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrencySerializesAcrossAgents(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := concurrencyKernels(t, backend)
			first := createKeyed(t, k, ctx, "agent-1", "session-a")
			second := createKeyed(t, k, ctx, "agent-2", "session-a")
			third := createKeyed(t, k, ctx, "agent-1", "session-a")
			independent := createKeyed(t, k, ctx, "agent-1", "session-b")
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
				ConcurrencyKey string `json:"concurrency_key"`
			}
			if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload.ConcurrencyKey != "session-a" {
				t.Fatalf("creation key: %+v, %v", payload, err)
			}
			drainKeyed(t, k, ctx)
			requireExecutionStatus(t, k, ctx, first.ID, domain.ExecutionRunning)
			requireExecutionStatus(t, k, ctx, independent.ID, domain.ExecutionRunning)
			for _, exec := range []domain.Execution{second, third} {
				requireExecutionStatus(t, k, ctx, exec.ID, domain.ExecutionPending)
				ds, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
				if err != nil || len(ds) != 0 {
					t.Fatalf("waiter dispatches: %v, %v", ds, err)
				}
			}
			page, err := k.ListExecutions(ctx, domain.ExecutionFilter{ConcurrencyKey: "session-a"})
			if err != nil || len(page.Executions) != 3 {
				t.Fatalf("key filter: %+v, %v", page, err)
			}
			if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionRunning)
			requireExecutionStatus(t, k, ctx, third.ID, domain.ExecutionPending)
			drainKeyed(t, k, ctx)
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

func TestConcurrencyRetainsOwnershipAcrossApproval(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			base, _, ctx := concurrencyKernels(t, backend)
			deps := base.Deps()
			deps.Policy = approvalPolicy()
			k := kernel.New(kernel.DefaultConfig(), deps)
			first := createKeyed(t, k, ctx, "agent-1", "session")
			second := createKeyed(t, k, ctx, "agent-1", "session")
			drainKeyed(t, k, ctx)
			decision, err := k.SubmitStep(ctx, first.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "fs_write", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, first.ID)})
			if err != nil || decision.ApprovalID == nil {
				t.Fatalf("approval: %+v, %v", decision, err)
			}
			drainKeyed(t, k, ctx)
			requireExecutionStatus(t, k, ctx, first.ID, domain.ExecutionBlocked)
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionPending)
			if err := k.GrantApproval(ctx, *decision.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "test"}); err != nil {
				t.Fatal(err)
			}
			drainKeyed(t, k, ctx)
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionPending)
			if err := k.CancelExecution(ctx, first.ID); err != nil {
				t.Fatal(err)
			}
			requireExecutionStatus(t, k, ctx, second.ID, domain.ExecutionRunning)
		})
	}
}

func TestConcurrencyCancellationAndDeadlines(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := concurrencyKernels(t, backend)
			first := createKeyed(t, k, ctx, "agent-1", "session")
			cancelled := createKeyed(t, k, ctx, "agent-1", "session")
			past := time.Now().Add(-time.Hour)
			expired := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: "agent-1", ConcurrencyKey: "session", Status: domain.ExecutionPending, Input: json.RawMessage(`{}`), DeadlineAt: &past}
			if err := k.Deps().Executions.CreateExecution(ctx, expired); err != nil {
				t.Fatal(err)
			}
			next := createKeyed(t, k, ctx, "agent-1", "session")
			drainKeyed(t, k, ctx)
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

func TestConcurrencyAdmissionAcrossReplicas(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, replica, ctx := concurrencyKernels(t, backend)
			const count = 12
			var wg sync.WaitGroup
			for range count {
				wg.Go(func() {
					if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{ConcurrencyKey: "session"}); err != nil {
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
			page, err := k.ListExecutions(ctx, domain.ExecutionFilter{ConcurrencyKey: "session"})
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
func TestConcurrencyAdmissionRollback(t *testing.T) {
	k, _, ctx := concurrencyKernels(t, "postgres")
	deps := k.Deps()
	fault := &admissionFault{UnitOfWork: deps.UnitOfWork}
	fault.fail.Store(true)
	deps.UnitOfWork = fault
	failing := kernel.New(kernel.DefaultConfig(), deps)
	exec := createKeyed(t, failing, ctx, "agent-1", "session")
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

func TestConcurrencyRetentionPreservesNonterminalExecutions(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := concurrencyKernels(t, backend)
			ids := make(map[domain.ExecutionStatus]uuid.UUID)
			for _, status := range []domain.ExecutionStatus{domain.ExecutionPending, domain.ExecutionRunning, domain.ExecutionBlocked, domain.ExecutionCompleted, domain.ExecutionFailed, domain.ExecutionCancelled} {
				exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: "agent-1", Input: json.RawMessage(`{}`), ConcurrencyKey: string(status), Status: domain.ExecutionPending, CreatedAt: time.Now().Add(-48 * time.Hour)}
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
