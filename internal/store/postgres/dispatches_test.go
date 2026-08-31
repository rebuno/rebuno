package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

// fenceStore is the slice of a backend the dispatch lease fence touches. Both
// implementations are exercised, since the fence has to mean the same thing in
// each; memstore lives here rather than in its own package for the same reason
// TestMemStoreInterfaces does, so one DATABASE_URL gate covers both.
type fenceStore interface {
	store.JobQueue
	store.StepStore
	store.AgentStore
	store.ExecutionStore
}

// fenceBackends returns every store implementation under test. Postgres is
// skipped unless DATABASE_URL is set, the same gate as TestNewStore.
func fenceBackends(t *testing.T) map[string]fenceStore {
	t.Helper()
	backends := map[string]fenceStore{"memstore": memstore.NewStore()}
	if testing.Short() {
		return backends
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return backends
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	backends["postgres"] = NewStore(pool)
	return backends
}

// deliveredDispatch registers an agent, creates an execution, queues one
// dispatch and claims it, returning the lease of that first delivery.
func deliveredDispatch(t *testing.T, s fenceStore) (uuid.UUID, domain.Lease) {
	t.Helper()
	ctx := context.Background()
	agentID := "fence-" + uuid.NewString()
	if err := s.RegisterAgent(ctx, domain.Agent{ID: agentID, WebhookURL: "http://localhost", Secret: "s"}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	now := time.Now().UTC()
	execID := uuid.Must(uuid.NewV7())
	if err := s.CreateExecution(ctx, domain.Execution{
		ID: execID, AgentID: agentID, Status: domain.ExecutionRunning,
		Input: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := s.Enqueue(ctx, domain.Dispatch{
		ID: uuid.Must(uuid.NewV7()), ExecutionID: execID, Status: domain.DispatchPending,
		MaxAttempts: 5, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed := claimFor(t, s, execID, now)
	return execID, domain.Lease{DispatchID: claimed.ID, Attempt: claimed.Attempt}
}

// claimFor claims due work and returns this execution's dispatch, which is the
// only one a fresh fixture has.
func claimFor(t *testing.T, s fenceStore, execID uuid.UUID, now time.Time) domain.Dispatch {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Claim(ctx, "replica", 100, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	ds, err := s.ListDispatchesByExecution(ctx, execID)
	if err != nil || len(ds) == 0 {
		t.Fatalf("list dispatches: %v", err)
	}
	d := ds[len(ds)-1]
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("dispatch must be in flight, got %s", d.Status)
	}
	return d
}

func TestLeaseFenceRefusesSupersededAttempt(t *testing.T) {
	ctx := context.Background()
	for name, s := range fenceBackends(t) {
		t.Run(name, func(t *testing.T) {
			execID, stalled := deliveredDispatch(t, s)
			now := time.Now().UTC()

			if err := s.RenewLease(ctx, execID, stalled, now); err != nil {
				t.Fatalf("live lease must renew: %v", err)
			}
			if err := s.CheckLease(ctx, execID, stalled); err != nil {
				t.Fatalf("live lease must check out: %v", err)
			}

			// A lease naming another execution is not this dispatch's.
			if err := s.RenewLease(ctx, uuid.Must(uuid.NewV7()), stalled, now); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("foreign execution must be refused, got %v", err)
			}

			later := now.Add(time.Hour)
			if _, err := s.ReclaimStalled(ctx, later, time.Minute, 10); err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			live := claimFor(t, s, execID, later)
			if live.Attempt != stalled.Attempt+1 {
				t.Fatalf("re-claim must advance the attempt, got %d", live.Attempt)
			}
			liveLease := domain.Lease{DispatchID: live.ID, Attempt: live.Attempt}

			if err := s.RenewLease(ctx, execID, stalled, later); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("superseded renew must be refused, got %v", err)
			}
			if err := s.CheckLease(ctx, execID, stalled); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("superseded check must be refused, got %v", err)
			}
			if err := s.RenewLease(ctx, execID, liveLease, later); err != nil {
				t.Fatalf("live lease must renew after re-claim: %v", err)
			}

			// A late ack from the stalled attempt must leave the live row alone.
			next := later.Add(time.Hour)
			if err := s.Ack(ctx, stalled.DispatchID, stalled.Attempt, domain.DispatchFailed, &next); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("superseded ack must be refused, got %v", err)
			}
			d, err := s.GetDispatch(ctx, live.ID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != domain.DispatchInFlight || d.Attempt != live.Attempt {
				t.Fatalf("live dispatch must be untouched, got %s attempt %d", d.Status, d.Attempt)
			}
		})
	}
}

func TestOccurrenceAdvanceIsFenced(t *testing.T) {
	ctx := context.Background()
	for name, s := range fenceBackends(t) {
		t.Run(name, func(t *testing.T) {
			execID, stalled := deliveredDispatch(t, s)

			if err := s.AdvanceDispatchOccurrence(ctx, execID, stalled, domain.StepKindTool, "read", "hash", 0); err != nil {
				t.Fatalf("live advance: %v", err)
			}
			got, err := s.DispatchOccurrence(ctx, stalled.DispatchID, domain.StepKindTool, "read", "hash")
			if err != nil || got != 1 {
				t.Fatalf("expected next occurrence 1, got %d (%v)", got, err)
			}

			later := time.Now().UTC().Add(time.Hour)
			if _, err := s.ReclaimStalled(ctx, later, time.Minute, 10); err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			live := claimFor(t, s, execID, later)

			// The re-claim cleared the counter for the new attempt; the stalled
			// attempt must not be able to move it back up under the live one.
			if err := s.AdvanceDispatchOccurrence(ctx, execID, stalled, domain.StepKindTool, "read", "hash", 4); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("superseded advance must be refused, got %v", err)
			}
			got, err = s.DispatchOccurrence(ctx, live.ID, domain.StepKindTool, "read", "hash")
			if err != nil || got != 0 {
				t.Fatalf("live attempt must still start from occurrence 0, got %d (%v)", got, err)
			}
		})
	}
}

// A released lease still records the outcome of work it already started: the
// dispatch is no longer in flight, but nothing has superseded it.
func TestCheckLeaseSurvivesRelease(t *testing.T) {
	ctx := context.Background()
	for name, s := range fenceBackends(t) {
		t.Run(name, func(t *testing.T) {
			execID, lease := deliveredDispatch(t, s)
			if err := s.Retire(ctx, lease.DispatchID); err != nil {
				t.Fatalf("retire: %v", err)
			}
			if err := s.CheckLease(ctx, execID, lease); err != nil {
				t.Fatalf("released lease must still check out: %v", err)
			}
			if err := s.RenewLease(ctx, execID, lease, time.Now().UTC()); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("released lease must not renew, got %v", err)
			}
		})
	}
}

// A reclaim must not move the attempt out from under a write that already
// checked the lease. Postgres only: memstore serialises every transaction on one
// write lock, so it holds there trivially.
func TestCheckedLeaseBlocksAConcurrentClaim(t *testing.T) {
	ctx := context.Background()
	s, ok := fenceBackends(t)["postgres"].(*Store)
	if !ok {
		t.Skip("DATABASE_URL not set")
	}
	execID, lease := deliveredDispatch(t, s)
	later := time.Now().UTC().Add(time.Hour)

	err := s.RunInTx(ctx, func(tx store.TxStore) error {
		if err := tx.CheckLease(ctx, execID, lease); err != nil {
			return err
		}
		// The reaper runs on another connection while this transaction is open.
		reclaimed, err := s.ReclaimStalled(ctx, later, time.Minute, 10)
		if err != nil {
			return err
		}
		for _, d := range reclaimed {
			if d.ID == lease.DispatchID {
				t.Error("a reclaim crossed the checked transaction")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Settling a delivery and retiring a dispatch are different operations. A late
// failure from a delivery the kernel has already retired — an approval took the
// work back, or the execution went terminal — must not requeue it.
func TestLateAckCannotRequeueARetiredDispatch(t *testing.T) {
	ctx := context.Background()
	for name, s := range fenceBackends(t) {
		t.Run(name, func(t *testing.T) {
			_, lease := deliveredDispatch(t, s)
			if err := s.Retire(ctx, lease.DispatchID); err != nil {
				t.Fatalf("retire: %v", err)
			}

			next := time.Now().UTC().Add(time.Hour)
			if err := s.Ack(ctx, lease.DispatchID, lease.Attempt, domain.DispatchFailed, &next); !errors.Is(err, domain.ErrLeaseSuperseded) {
				t.Fatalf("a retired dispatch must not be settled, got %v", err)
			}
			d, err := s.GetDispatch(ctx, lease.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != domain.DispatchExhausted {
				t.Fatalf("dispatch must stay retired, got %s", d.Status)
			}
		})
	}
}
