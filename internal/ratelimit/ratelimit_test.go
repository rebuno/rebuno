package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestMemoryLimiter_AllowsUpToMaxCalls(t *testing.T) {
	ctx := context.Background()
	lim := NewMemoryLimiter()
	cfg := domain.RateLimitConfig{MaxCalls: 3, Window: time.Hour}
	key := Key("rule:exec")

	for i := 0; i < cfg.MaxCalls; i++ {
		allowed, wait, err := lim.Allow(ctx, key, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("call %d expected allowed, got denied", i+1)
		}
		if wait != 0 {
			t.Fatalf("call %d expected no wait, got %v", i+1, wait)
		}
	}
}

func TestMemoryLimiter_DeniesAfterMaxCalls(t *testing.T) {
	ctx := context.Background()
	lim := NewMemoryLimiter()
	cfg := domain.RateLimitConfig{MaxCalls: 2, Window: time.Hour}
	key := Key("rule:exec")

	for i := 0; i < cfg.MaxCalls; i++ {
		if allowed, _, _ := lim.Allow(ctx, key, cfg); !allowed {
			t.Fatalf("call %d expected allowed, got denied", i+1)
		}
	}

	allowed, wait, err := lim.Allow(ctx, key, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("expected denied after exhausting tokens")
	}
	if wait <= 0 {
		t.Fatalf("expected positive wait, got %v", wait)
	}
	if wait > cfg.Window {
		t.Fatalf("wait %v exceeds configured window %v", wait, cfg.Window)
	}
}

func TestMemoryLimiter_RefillsAfterWindow(t *testing.T) {
	ctx := context.Background()
	lim := NewMemoryLimiter()
	cfg := domain.RateLimitConfig{MaxCalls: 1, Window: 50 * time.Millisecond}
	key := Key("rule:exec")

	if allowed, _, _ := lim.Allow(ctx, key, cfg); !allowed {
		t.Fatal("first call expected allowed")
	}

	allowed, _, _ := lim.Allow(ctx, key, cfg)
	if allowed {
		t.Fatal("second call expected denied")
	}

	time.Sleep(cfg.Window + 10*time.Millisecond)

	allowed, wait, err := lim.Allow(ctx, key, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed after window passed")
	}
	if wait != 0 {
		t.Fatalf("expected no wait, got %v", wait)
	}
}

func TestMemoryLimiter_DifferentKeysAreIndependent(t *testing.T) {
	ctx := context.Background()
	lim := NewMemoryLimiter()
	cfg := domain.RateLimitConfig{MaxCalls: 1, Window: time.Hour}

	if allowed, _, _ := lim.Allow(ctx, Key("a"), cfg); !allowed {
		t.Fatal("key a expected allowed")
	}
	if allowed, _, _ := lim.Allow(ctx, Key("b"), cfg); !allowed {
		t.Fatal("key b expected allowed")
	}

	// Both keys have now exhausted their single token.
	if allowed, _, _ := lim.Allow(ctx, Key("a"), cfg); allowed {
		t.Fatal("key a expected denied")
	}
	if allowed, _, _ := lim.Allow(ctx, Key("b"), cfg); allowed {
		t.Fatal("key b expected denied")
	}
}

func TestNoOpLimiter_AlwaysAllows(t *testing.T) {
	ctx := context.Background()
	lim := NoOp()
	cfg := domain.RateLimitConfig{MaxCalls: 1, Window: time.Hour}

	for i := 0; i < 10; i++ {
		allowed, wait, err := lim.Allow(ctx, Key("any"), cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("call %d expected allowed, got denied", i+1)
		}
		if wait != 0 {
			t.Fatalf("call %d expected no wait, got %v", i+1, wait)
		}
	}
}
