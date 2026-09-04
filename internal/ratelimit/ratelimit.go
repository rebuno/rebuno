package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type Key string

type Limiter interface {
	Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error)
}

// Implemented by limiters that accumulate per-key state. The lifecycle cleanup
// worker calls it on the leader.
type Reaper interface {
	ReapBefore(ctx context.Context, cutoff time.Time) error
}

func ScopeKey(ruleID, perWhat, execID, agentID string) Key {
	switch perWhat {
	case domain.PerWhatAgent:
		return Key(ruleID + ":agent:" + agentID)
	case domain.PerWhatGlobal:
		return Key(ruleID + ":global")
	default:
		return Key(ruleID + ":exec:" + execID)
	}
}

type MemoryLimiter struct {
	mu      sync.RWMutex
	buckets map[Key]*bucket
}

type bucket struct {
	tokens     float64
	lastUpdate time.Time
}

func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{buckets: make(map[Key]*bucket)}
}

func (l *MemoryLimiter) Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error) {
	if cfg.MaxCalls <= 0 || cfg.Window <= 0 {
		return true, 0, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(cfg.MaxCalls), lastUpdate: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastUpdate)
		refill := float64(cfg.MaxCalls) * float64(elapsed) / float64(cfg.Window)
		b.tokens = min(float64(cfg.MaxCalls), b.tokens+refill)
		b.lastUpdate = now
	}

	if b.tokens >= 1.0 {
		b.tokens--
		return true, 0, nil
	}

	wait := time.Duration((1.0 - b.tokens) * float64(cfg.Window) / float64(cfg.MaxCalls))
	return false, wait, nil
}

type NoOpLimiter struct{}

func NoOp() *NoOpLimiter {
	return &NoOpLimiter{}
}

func (NoOpLimiter) Allow(ctx context.Context, key Key, cfg domain.RateLimitConfig) (bool, time.Duration, error) {
	return true, 0, nil
}

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
