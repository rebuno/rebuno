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
	// reached via the singleton tick (runSingletons).
	if got := atomic.LoadInt32(&k.cancelExpiredExecutions); got == 0 {
		t.Fatalf("expected singleton tick to drive CancelExpiredExecutions, got %d", got)
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
