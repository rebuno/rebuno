package ratelimit

import (
	"testing"
	"time"
)

func TestMemoryLimiter_AllowsWithinLimit(t *testing.T) {
	lim := NewMemoryLimiter()
	for i := 0; i < 5; i++ {
		allowed, err := lim.Allow("agent:shell.exec", 5, time.Minute)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if !allowed {
			t.Errorf("expected allow on call %d of 5", i+1)
		}
	}
}

func TestMemoryLimiter_DeniesOverLimit(t *testing.T) {
	lim := NewMemoryLimiter()
	for i := 0; i < 5; i++ {
		lim.Allow("agent:tool", 5, time.Minute)
	}
	allowed, err := lim.Allow("agent:tool", 5, time.Minute)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if allowed {
		t.Error("expected deny on 6th call with limit of 5")
	}
}

func TestMemoryLimiter_SeparateKeys(t *testing.T) {
	lim := NewMemoryLimiter()
	for i := 0; i < 5; i++ {
		lim.Allow("agent-a:tool", 5, time.Minute)
	}
	// Different key should still be allowed
	allowed, _ := lim.Allow("agent-b:tool", 5, time.Minute)
	if !allowed {
		t.Error("expected allow for different key")
	}
}

func TestMemoryLimiter_WindowExpiry(t *testing.T) {
	lim := NewMemoryLimiter()
	// Use a very short window
	for i := 0; i < 3; i++ {
		lim.Allow("key", 3, 50*time.Millisecond)
	}
	// Should be denied
	allowed, _ := lim.Allow("key", 3, 50*time.Millisecond)
	if allowed {
		t.Error("expected deny at limit")
	}
	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)
	// Should be allowed again
	allowed, _ = lim.Allow("key", 3, 50*time.Millisecond)
	if !allowed {
		t.Error("expected allow after window expiry")
	}
}
