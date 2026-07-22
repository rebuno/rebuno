package lifecycle

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/store"
)

type Kernel interface {
	RunDispatches(ctx context.Context, batch int) error
	ExpireApprovals(ctx context.Context, now time.Time) error
	CancelExpiredExecutions(ctx context.Context, now time.Time) error
	Cleanup(ctx context.Context, retain time.Duration, now time.Time) error
}

type Manager struct {
	kernel           Kernel
	logger           *slog.Logger
	stop             chan struct{}
	wg               sync.WaitGroup
	interval         time.Duration
	deadlineInterval time.Duration
	batch            int
	LeaderLockKey    string
	Retention        time.Duration
	leaderLocker     store.Locker
	observer         *observe.Observer
}

type ManagerOption func(*Manager)

// WithObserver injects a custom observer. If the observer is nil, the manager
// falls back to observe.Default().
func WithObserver(o *observe.Observer) ManagerOption {
	return func(m *Manager) {
		m.observer = o
	}
}

// WithDeadlineInterval sets the cadence of the deadline enforcement loop,
// which cancels executions past their deadline_at independently of the
// cleanup/singleton interval. A value <= 0 disables the dedicated loop, in
// which case deadline enforcement falls back to the singleton tick.
func WithDeadlineInterval(d time.Duration) ManagerOption {
	return func(m *Manager) {
		m.deadlineInterval = d
	}
}

// NewManager returns a lifecycle manager with a default 2 second singleton
// worker interval and no leader election (singletons run on every replica).
func NewManager(k Kernel, logger *slog.Logger, opts ...ManagerOption) *Manager {
	return NewManagerWithLocker(k, logger, 2*time.Second, nil, opts...)
}

// NewManagerWithLocker returns a lifecycle manager that gates singleton workers
// behind a non-blocking leader lock held via the provided store.Locker. The
// interval controls how often singleton workers run; dispatch drain always runs
// on a fixed 2 second cadence on every replica.
func NewManagerWithLocker(k Kernel, logger *slog.Logger, interval time.Duration, locker store.Locker, opts ...ManagerOption) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		kernel:        k,
		logger:        logger,
		stop:          make(chan struct{}),
		interval:      interval,
		batch:         10,
		LeaderLockKey: "rebuno_scheduler_leader",
		leaderLocker:  locker,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.observer == nil {
		m.observer = observe.Default()
	}
	return m
}

func (m *Manager) Start(ctx context.Context) {
	m.wg.Add(1)
	go m.loop(ctx, "dispatch", 2*time.Second, m.dispatchTick)
	if m.interval > 0 {
		m.wg.Add(1)
		go m.loop(ctx, "singletons", m.interval, m.singletonsTick)
	}
	if m.deadlineInterval > 0 {
		m.wg.Add(1)
		go m.loop(ctx, "deadline", m.deadlineInterval, m.deadlineTick)
	}
}

func (m *Manager) Stop() {
	close(m.stop)
	m.wg.Wait()
}

func (m *Manager) dispatchTick(ctx context.Context) error {
	return m.kernel.RunDispatches(ctx, m.batch)
}

// deadlineTick enforces execution deadlines on its own cadence so that an
// execution past its deadline_at is cancelled promptly rather than waiting up
// to the (potentially much longer) cleanup/singleton interval. It is gated by
// the same leader lock as the singleton workers so only the leader cancels
// expired executions.
func (m *Manager) deadlineTick(ctx context.Context) error {
	return m.withLeaderLock(ctx, func(ctx context.Context) error {
		return m.kernel.CancelExpiredExecutions(ctx, time.Now().UTC())
	})
}

func (m *Manager) singletonsTick(ctx context.Context) error {
	return m.withLeaderLock(ctx, m.runSingletons)
}

// withLeaderLock runs fn when this replica holds the leader lock. With no
// locker configured, fn runs unconditionally on every replica. When the lock
// is held by another replica, the tick is skipped.
func (m *Manager) withLeaderLock(ctx context.Context, fn func(context.Context) error) error {
	if m.leaderLocker == nil || m.LeaderLockKey == "" {
		return fn(ctx)
	}
	release, err := m.leaderLocker.TryAcquire(ctx, m.LeaderLockKey)
	if err != nil {
		return err
	}
	if release == nil {
		// Another replica holds the leader lock; skip this tick.
		return nil
	}
	defer release()
	return fn(ctx)
}

func (m *Manager) runSingletons(ctx context.Context) error {
	now := time.Now().UTC()
	if err := m.kernel.ExpireApprovals(ctx, now); err != nil {
		return err
	}
	if err := m.kernel.CancelExpiredExecutions(ctx, now); err != nil {
		return err
	}
	return m.kernel.Cleanup(ctx, m.Retention, now)
}

func (m *Manager) loop(ctx context.Context, name string, interval time.Duration, fn func(context.Context) error) {
	defer m.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-ticker.C:
			if err := fn(ctx); err != nil {
				m.observer.RecordWorkerError(name)
				m.logger.Error("lifecycle worker error", "worker", name, "error", err)
			}
		}
	}
}
