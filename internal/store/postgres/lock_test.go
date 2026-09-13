package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func advisoryLockCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'`).Scan(&n); err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	return n
}

func TestAdvisoryLockReleasedAfterContextCancel(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := NewStore(pool)

	before := advisoryLockCount(t, ctx, pool)

	lockCtx, cancel := context.WithCancel(ctx)
	release, err := s.Acquire(lockCtx, "leak-test-key")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	cancel()  // caller context dies before release, as in a cancelled request
	release() // must still free the lock despite the dead context

	if after := advisoryLockCount(t, ctx, pool); after != before {
		t.Fatalf("advisory lock leaked: count was %d before, %d after release", before, after)
	}
}

func TestTryAcquireReportsHeldLock(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	s := NewStore(pool)

	release, err := s.Acquire(ctx, "contended-key")
	if err != nil {
		t.Fatalf("acquire: %v", err)
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

func smallPoolStore(t *testing.T, cfg *pgxpool.Config) *Store {
	t.Helper()
	cfg = cfg.Copy()
	cfg.MaxConns = 3
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

func TestContendedLockLeavesPoolAvailable(t *testing.T) {
	otherReplica := NewStore(testPool(t))
	s := smallPoolStore(t, otherReplica.pool.Config())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	key := uuid.NewString()
	release, err := otherReplica.Acquire(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

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
			release, err := s.Acquire(ctx, key)
			if err == nil {
				err = s.pool.Ping(ctx)
				release()
			}
			results <- err
		})
	}
	waitForLockWaiters(t, ctx, s, key, waiters)

	probeCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	releaseOther, err := s.Acquire(probeCtx, uuid.NewString())
	if err != nil {
		t.Fatalf("acquire unrelated lock: %v", err)
	}
	defer releaseOther()
	if err := s.pool.Ping(probeCtx); err != nil {
		t.Fatalf("database work under unrelated lock: %v", err)
	}
	releaseOther()
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

func TestAcquireFailureReleasesLocalGate(t *testing.T) {
	s := smallPoolStore(t, testPool(t).Config())
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
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	release, err := s.Acquire(waitCtx, key)
	stop()
	if release != nil {
		release()
		t.Fatal("acquired a lock with an exhausted pool")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire returned %v", err)
	}

	releaseConnections()
	release, err = s.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("acquire after pool recovery: %v", err)
	}
	release()
}
