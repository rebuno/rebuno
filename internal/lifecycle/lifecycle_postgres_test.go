package lifecycle_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/lifecycle"
	"github.com/rebuno/rebuno/internal/store/postgres"
)

type blockedCleanupKernel struct {
	fakeKernel
	entered chan struct{}
	unblock chan struct{}
}

func (k *blockedCleanupKernel) Cleanup(ctx context.Context, retain time.Duration, now time.Time) error {
	close(k.entered)
	select {
	case <-k.unblock:
	case <-ctx.Done():
	}
	return nil
}

func TestDeadlineLoopContinuesWhileCleanupHoldsLeaderLock(t *testing.T) {
	if testing.Short() || os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires DATABASE_URL without -short")
	}
	pool, err := pgxpool.New(t.Context(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	k := &blockedCleanupKernel{entered: make(chan struct{}), unblock: make(chan struct{})}
	mgr := lifecycle.NewManagerWithLocker(k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Millisecond, postgres.NewStore(pool), lifecycle.WithDeadlineInterval(10*time.Millisecond))
	mgr.LeaderLockKey = "deadline-cleanup-" + uuid.NewString()
	ctx, cancel := context.WithCancel(t.Context())
	mgr.Start(ctx)
	defer func() {
		cancel()
		close(k.unblock)
		mgr.Stop()
	}()

	select {
	case <-k.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not start")
	}
	initial := atomic.LoadInt32(&k.cancelExpiredExecutions)
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for atomic.LoadInt32(&k.cancelExpiredExecutions) < initial+2 {
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("deadline loop did not continue during cleanup: %d ticks", atomic.LoadInt32(&k.cancelExpiredExecutions))
		}
	}
}
