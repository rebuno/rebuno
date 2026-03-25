package ratelimit

import (
	"sync"
	"time"
)

// Limiter checks whether a request identified by key is within its rate limit.
type Limiter interface {
	// Allow returns true if the request is within the rate limit.
	// key identifies the rate limit bucket (e.g., "agent-id:tool-id").
	// max is the maximum number of requests allowed in the window.
	// window is the sliding time window duration.
	Allow(key string, max int, window time.Duration) (bool, error)
}

// MemoryLimiter is an in-memory sliding window rate limiter.
type MemoryLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	timestamps []time.Time
}

func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{
		buckets: make(map[string]*bucket),
	}
}

func (l *MemoryLimiter) Allow(key string, max int, window time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{}
		l.buckets[key] = b
	}

	// Remove expired timestamps
	valid := b.timestamps[:0]
	for _, ts := range b.timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}
	b.timestamps = valid

	if len(b.timestamps) >= max {
		return false, nil
	}

	b.timestamps = append(b.timestamps, now)
	return true, nil
}
