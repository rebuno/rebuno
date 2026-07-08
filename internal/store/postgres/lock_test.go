package postgres

import (
	"context"
	"testing"

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

// TestAdvisoryLockReleasedAfterContextCancel verifies that when the
// context used to acquire the lock is already cancelled by the time release runs
// (request timeout / client disconnect), the lock must still be freed and the
// connection must not return to the pool holding it.
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

// TestTryAcquireReportsHeldLock confirms TryAcquire honors its non-blocking
// contract: it returns (nil, nil) when the key is already held by Acquire.
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
