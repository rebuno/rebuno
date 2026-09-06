package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/rebuno/rebuno/internal/lifecycle"
)

type fakeKernel struct {
	dispatches              int32
	expireApprovals         int32
	cancelExpiredExecutions int32
	cleanups                int32
	cancelErr               error
}

func (f *fakeKernel) RunDispatcher(ctx context.Context) error {
	atomic.AddInt32(&f.dispatches, 1)
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeKernel) ExpireApprovals(ctx context.Context, now time.Time) error {
	atomic.AddInt32(&f.expireApprovals, 1)
	return nil
}

func (f *fakeKernel) CancelExpiredExecutions(ctx context.Context, now time.Time) error {
	atomic.AddInt32(&f.cancelExpiredExecutions, 1)
	return f.cancelErr
}

func (f *fakeKernel) Cleanup(ctx context.Context, retain time.Duration, now time.Time) error {
	atomic.AddInt32(&f.cleanups, 1)
	return nil
}

func TestDeadlineLoopRunsIndependentlyOfCleanup(t *testing.T) {
	k := &fakeKernel{}
	mgr := lifecycle.NewManagerWithLocker(
		k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute, // singleton/cleanup interval
		nil,            // no leader lock: singletons run unconditionally
		lifecycle.WithDeadlineInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	cancel()
	mgr.Stop()

	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got < 5 {
		t.Fatalf("expected deadline loop to fire multiple times, got %d", got)
	}
	if got := atomic.LoadInt32(&k.cleanups); got != 0 {
		t.Fatalf("expected cleanup to not fire within 10m interval, got %d", got)
	}
	if got := atomic.LoadInt32(&k.expireApprovals); got != 0 {
		t.Fatalf("expected expire-approvals to not fire within 10m interval, got %d", got)
	}
}

func TestDeadlineLoopDisabledByDefault(t *testing.T) {
	k := &fakeKernel{}
	mgr := lifecycle.NewManagerWithLocker(
		k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Millisecond, // singleton interval
		nil,
		// no WithDeadlineInterval: dedicated loop disabled
	)
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	mgr.Stop()

	// Cleanup fires only from runSingletons, so an equal count is what pins these
	// calls to the singleton tick instead of a dedicated loop left enabled.
	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got == 0 {
		t.Fatalf("expected singleton tick to drive CancelExpiredExecutions, got %d", got)
	}
	if got := atomic.LoadInt32(&k.cleanups); got == 0 {
		t.Fatalf("expected singleton tick to also drive Cleanup, got %d", got)
	}
	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got != atomic.LoadInt32(&k.cleanups) {
		t.Fatalf("cancelExpiredExecutions (%d) should equal cleanups (%d) when driven by runSingletons",
			got, atomic.LoadInt32(&k.cleanups))
	}
}

func TestDeadlineTickPropagatesError(t *testing.T) {
	k := &fakeKernel{cancelErr: errors.New("boom")}
	mgr := lifecycle.NewManagerWithLocker(
		k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
		nil,
		lifecycle.WithDeadlineInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	mgr.Stop()

	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got == 0 {
		t.Fatal("expected deadline loop to fire despite errors")
	}
}

// heldLocker reports the lock as always held by another replica, so
// withLeaderLock should skip the tick.
type heldLocker struct{}

func (heldLocker) Acquire(ctx context.Context, key string) (func(), error) {
	return nil, nil
}
func (heldLocker) TryAcquire(ctx context.Context, key string) (func(), error) {
	return nil, nil
}

func TestDeadlineLoopGatedByLeaderLock(t *testing.T) {
	k := &fakeKernel{}
	mgr := lifecycle.NewManagerWithLocker(
		k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute,
		heldLocker{},
		lifecycle.WithDeadlineInterval(10*time.Millisecond),
	)
	mgr.LeaderLockKey = "leader"
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	mgr.Stop()

	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got != 0 {
		t.Fatalf("expected deadline loop to be skipped while leader lock held, got %d", got)
	}
}
