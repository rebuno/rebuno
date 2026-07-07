package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/rebuno/kernel/internal/domain"
)

// Key identifies the scope being rate-limited, e.g. "ruleID:executionID".
type Key string

type Limiter interface {
	Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error)
}

// Reaper is optionally implemented by limiters that accumulate per-key state and
// need periodic eviction. The lifecycle cleanup worker calls it on the leader.
type Reaper interface {
	ReapBefore(ctx context.Context, cutoff time.Time) error
}

// ScopeKey builds the limiter key for a rule honoring its per_what scope, so a
// rule limits the intended subject rather than always per-execution.
func ScopeKey(ruleID, perWhat, execID, agentID string) Key {
	switch perWhat {
	case "agent":
		return Key(ruleID + ":agent:" + agentID)
	case "global":
		return Key(ruleID + ":global")
	default: // "execution" or unset
		return Key(ruleID + ":exec:" + execID)
	}
}

// MemoryLimiter is a simple, process-local token-bucket rate limiter.
type MemoryLimiter struct {
	mu      sync.RWMutex
	buckets map[Key]*bucket
}

type bucket struct {
	tokens     float64
	lastUpdate time.Time
	max        int
	window     time.Duration
}

func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{buckets: make(map[Key]*bucket)}
}

// Allow reports whether one more call is allowed for key under the given config.
// If denied, it returns the estimated wait time until one token is available.
func (l *MemoryLimiter) Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error) {
	if cfg.MaxCalls <= 0 || cfg.Window <= 0 {
		return true, 0, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(cfg.MaxCalls), lastUpdate: now, max: cfg.MaxCalls, window: cfg.Window}
		l.buckets[key] = b
	} else {
		// Ensure the bucket uses the latest limit configuration.
		b.max = cfg.MaxCalls
		b.window = cfg.Window
		capacity := float64(b.max)
		elapsed := now.Sub(b.lastUpdate)
		refill := float64(b.max) * float64(elapsed) / float64(b.window)
		b.tokens = min(capacity, b.tokens+refill)
		b.lastUpdate = now
	}

	if b.tokens >= 1.0 {
		b.tokens--
		return true, 0, nil
	}

	wait := time.Duration((1.0 - b.tokens) * float64(b.window) / float64(b.max))
	return false, wait, nil
}

// NoOpLimiter always allows requests with no waiting.
type NoOpLimiter struct{}

func NoOp() *NoOpLimiter {
	return &NoOpLimiter{}
}

func (NoOpLimiter) Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error) {
	return true, 0, nil
}

// ReapBefore drops buckets untouched since cutoff. Without this the map grows
// unbounded — one entry per (rule, scope) ever seen.
func (l *MemoryLimiter) ReapBefore(ctx context.Context, cutoff time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if b.lastUpdate.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
	return nil
}
