package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store"
)

func advisoryLockCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&n); err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	return n
}

func TestRunLockedReleasesLockAfterContextCancel(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := NewStore(pool)

	before := advisoryLockCount(t, ctx, pool)

	lockCtx, cancel := context.WithCancel(ctx)
	err := s.RunLocked(lockCtx, "leak-test-key", func(context.Context) error {
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("expected commit with a canceled context to fail")
	}

	if after := advisoryLockCount(t, ctx, pool); after != before {
		t.Fatalf("advisory lock leaked: count was %d before, %d after", before, after)
	}
}

func TestTryAcquireReportsHeldLock(t *testing.T) {
	ctx := context.Background()
	s := NewStore(testPool(t))

	release, err := s.TryAcquire(ctx, "contended-key")
	if err != nil || release == nil {
		t.Fatalf("try acquire: release=%v err=%v", release != nil, err)
	}
	defer release()

	got, err := s.TryAcquire(ctx, "contended-key")
	if err != nil {
		t.Fatalf("try acquire: %v", err)
	}
	if got != nil {
		got()
		t.Fatal("expected TryAcquire to report the lock as held (nil release)")
	}
}

func smallPoolStore(t *testing.T, cfg *pgxpool.Config, maxConns int32) *Store {
	t.Helper()
	cfg = cfg.Copy()
	cfg.MaxConns = maxConns
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool)
}

func waitForLockWaiters(t *testing.T, ctx context.Context, s *Store, key string, count int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		s.locks.mu.Lock()
		entry := s.locks.entries[key]
		ready := entry != nil && entry.refs == count
		s.locks.mu.Unlock()
		if ready && s.pool.Stat().AcquiredConns() > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %d lock requests: %v", count, ctx.Err())
		case <-ticker.C:
		}
	}
}

// holdLock holds key through another store until the returned release is called.
func holdLock(t *testing.T, ctx context.Context, s *Store, key string) func() {
	t.Helper()
	held := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.RunLocked(ctx, key, func(context.Context) error {
			close(held)
			<-done
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-finished:
		t.Fatalf("hold lock: %v", err)
	}
	return sync.OnceFunc(func() {
		close(done)
		if err := <-finished; err != nil {
			t.Errorf("release held lock: %v", err)
		}
	})
}

func TestContendedLockLeavesPoolAvailable(t *testing.T) {
	otherReplica := NewStore(testPool(t))
	s := smallPoolStore(t, otherReplica.pool.Config(), 3)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	key := uuid.NewString()
	release := holdLock(t, ctx, otherReplica, key)

	waiters := int(s.pool.Config().MaxConns) + 1
	results := make(chan error, waiters)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		release()
		wg.Wait()
	}()
	for range waiters {
		wg.Go(func() {
			results <- s.RunLocked(ctx, key, func(ctx context.Context) error {
				_, err := s.GetExecution(ctx, uuid.New())
				if errors.Is(err, domain.ErrNotFound) {
					return nil
				}
				return err
			})
		})
	}
	waitForLockWaiters(t, ctx, s, key, waiters)

	probeCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if err := s.RunLocked(probeCtx, uuid.NewString(), func(context.Context) error {
		return s.pool.Ping(probeCtx)
	}); err != nil {
		t.Fatalf("database work under unrelated lock: %v", err)
	}
	select {
	case err := <-results:
		t.Fatalf("waiter returned while another replica held the lock: %v", err)
	default:
	}

	release()
	for range waiters {
		if err := <-results; err != nil {
			t.Fatalf("waiter failed after lock release: %v", err)
		}
	}
}

func TestRunLockedFailureReleasesLocalGate(t *testing.T) {
	s := smallPoolStore(t, testPool(t).Config(), 3)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var held []*pgxpool.Conn
	releaseConnections := sync.OnceFunc(func() {
		for _, conn := range held {
			conn.Release()
		}
	})
	defer releaseConnections()
	for range s.pool.Config().MaxConns {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}

	key := uuid.NewString()
	noop := func(context.Context) error { return nil }
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	err := s.RunLocked(waitCtx, key, noop)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run locked with an exhausted pool returned %v", err)
	}

	releaseConnections()
	if err := s.RunLocked(ctx, key, noop); err != nil {
		t.Fatalf("run locked after pool recovery: %v", err)
	}
}

func TestRunLockedStoreCallsShareItsConnection(t *testing.T) {
	s := smallPoolStore(t, testPool(t).Config(), 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := s.RunLocked(ctx, uuid.NewString(), func(ctx context.Context) error {
		if _, err := s.GetExecution(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		cfg := domain.RateLimitConfig{MaxCalls: 1, Window: time.Minute}
		if _, _, err := s.Allow(ctx, ratelimit.Key(uuid.NewString()), cfg); err != nil {
			return err
		}
		return s.RunInTx(ctx, func(tx store.TxStore) error {
			_, err := tx.GetExecution(ctx, uuid.New())
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		})
	})
	if err != nil {
		t.Fatalf("locked section on a one-connection pool: %v", err)
	}
}

func TestRunInTxInsideRunLockedRollsBackAlone(t *testing.T) {
	ctx := context.Background()
	s := NewStore(testPool(t))
	execID := seedExecution(t, ctx, s)
	exec, err := s.GetExecution(ctx, execID)
	if err != nil {
		t.Fatal(err)
	}

	err = s.RunLocked(ctx, uuid.NewString(), func(ctx context.Context) error {
		err := s.RunInTx(ctx, func(tx store.TxStore) error {
			if _, err := tx.Append(ctx, execID, "discarded", nil); err != nil {
				return err
			}
			return tx.CreateExecution(ctx, exec)
		})
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("nested duplicate create returned %v", err)
		}
		_, err = s.Append(ctx, execID, "kept", nil)
		return err
	})
	if err != nil {
		t.Fatalf("locked section after nested rollback: %v", err)
	}

	events, err := s.GetEvents(ctx, execID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "kept" {
		t.Fatalf("events after nested rollback: %+v", events)
	}
}
