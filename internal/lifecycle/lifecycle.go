package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/store"
)

type Kernel interface {
	RunDispatcher(ctx context.Context) error
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
	cancel           context.CancelFunc
	LeaderLockKey    string
	Retention        time.Duration
	leaderLocker     store.Locker
	observer         *observe.Observer
}

type ManagerOption func(*Manager)

func WithObserver(o *observe.Observer) ManagerOption {
	return func(m *Manager) {
		m.observer = o
	}
}

// A value <= 0 disables the dedicated loop; deadline enforcement then falls back
// to the singleton tick.
func WithDeadlineInterval(d time.Duration) ManagerOption {
	return func(m *Manager) {
		m.deadlineInterval = d
	}
}

func NewManager(k Kernel, logger *slog.Logger, opts ...ManagerOption) *Manager {
	return NewManagerWithLocker(k, logger, 2*time.Second, nil, opts...)
}

// The locker gates only the singleton workers; the dispatch coordinator runs on
// every replica.
func NewManagerWithLocker(k Kernel, logger *slog.Logger, interval time.Duration, locker store.Locker, opts ...ManagerOption) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		kernel:        k,
		logger:        logger,
		stop:          make(chan struct{}),
		interval:      interval,
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
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go m.runDispatch(ctx)
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
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *Manager) runDispatch(ctx context.Context) {
	defer m.wg.Done()
	err := m.kernel.RunDispatcher(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		m.observer.RecordWorkerError("dispatch")
		m.logger.Error("dispatch coordinator stopped", "error", err)
	}
}

// Its own cadence, so a passed deadline is not left waiting on the much longer
// singleton interval.
func (m *Manager) deadlineTick(ctx context.Context) error {
	return m.withLeaderLock(ctx, func(ctx context.Context) error {
		return m.kernel.CancelExpiredExecutions(ctx, time.Now().UTC())
	})
}

func (m *Manager) singletonsTick(ctx context.Context) error {
	return m.withLeaderLock(ctx, m.runSingletons)
}

// With no locker configured, fn runs unconditionally on every replica.
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
