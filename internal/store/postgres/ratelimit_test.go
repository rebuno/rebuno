package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/ratelimit"
)

// TestPostgresLimiterEnforcesGlobalLimitUnderConcurrency proves the property the
// per-process MemoryLimiter cannot satisfy: across many concurrent "replicas"
// sharing one bucket, exactly MaxCalls admits are granted.
func TestPostgresLimiterEnforcesGlobalLimitUnderConcurrency(t *testing.T) {
	pool := testPool(t)
	defer pool.Close()
	s := NewStore(pool)
	ctx := context.Background()

	key := ratelimit.Key("rule-x:global")
	if _, err := pool.Exec(ctx, `DELETE FROM rate_buckets WHERE key = $1`, string(key)); err != nil {
		t.Fatalf("reset bucket: %v", err)
	}

	// Window far longer than the test runtime so no tokens refill mid-run.
	cfg := domain.RateLimitConfig{MaxCalls: 100, Window: time.Hour}
	const replicas, perReplica = 20, 50 // 1000 attempts total

	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perReplica; j++ {
				ok, _, err := s.Allow(ctx, key, cfg)
				if err != nil {
					t.Errorf("allow: %v", err)
					return
				}
				if ok {
					atomic.AddInt64(&allowed, 1)
				}
			}
		}()
	}
	wg.Wait()

	if allowed != int64(cfg.MaxCalls) {
		t.Fatalf("expected exactly %d admits across all replicas, got %d", cfg.MaxCalls, allowed)
	}
}
