package lifecycle_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"log/slog"

	"github.com/rebuno/rebuno/internal/lifecycle"
)

// fakeKernel records calls to each Kernel method so tests can assert cadence.
type fakeKernel struct {
	mu                      sync.Mutex
	dispatches              int32
	expireApprovals         int32
	cancelExpiredExecutions int32
	cleanups                int32
	cancelErr               error
}

func (f *fakeKernel) RunDispatches(ctx context.Context, batch int) error {
	atomic.AddInt32(&f.dispatches, 1)
	return nil
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

// TestDeadlineLoopRunsIndependentlyOfCleanup verifies that the dedicated
// deadline enforcement loop fires far more often than the singleton/cleanup
// interval, so executions past their deadline are cancelled promptly even when
// cleanup is configured to a long interval (the bug in issue #123).
func TestDeadlineLoopRunsIndependentlyOfCleanup(t *testing.T) {
	k := &fakeKernel{}
	// Cleanup interval is deliberately long; deadline interval is short.
	mgr := lifecycle.NewManagerWithLocker(
		k, slog.New(slog.NewTextHandler(io.Discard, nil)),
		10*time.Minute, // singleton/cleanup interval
		nil,            // no leader lock: singletons run unconditionally
		lifecycle.WithDeadlineInterval(10*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	mgr.Start(ctx)

	// Within 200ms the deadline loop should have fired many times, while the
	// singleton loop (10 min) should not have fired at all.
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

// TestDeadlineLoopDisabledByDefault verifies that a zero deadline interval
// disables the dedicated loop (deadline enforcement falls back to the
// singleton tick), preserving backward-compatible behavior.
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

	// With the dedicated loop disabled, CancelExpiredExecutions is only
	// reached via the singleton tick (runSingletons). Tie the assertion to
	// that path: cleanups also only fire from runSingletons, so a nonzero
	// cleanup count confirms the deadline calls came from the singleton tick
	// rather than an accidentally-enabled dedicated loop.
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

// TestDeadlineTickPropagatesError verifies that errors from
// CancelExpiredExecutions surface through the loop without panicking.
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

// TestDeadlineLoopGatedByLeaderLock verifies that when another replica holds
// the leader lock, the dedicated deadline loop skips its tick (the shared
// withLeaderLock helper gates both the deadline and singleton loops).
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
