package memstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/memstore"
)

func TestLocker_AcquireAndRelease(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	release, err := l.Acquire(ctx, "key-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if release == nil {
		t.Fatal("expected non-nil release func")
	}
	release()
}

func TestLocker_MutualExclusion(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	release1, err := l.Acquire(ctx, "key-x")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Second acquire with a short timeout should fail while lock is held.
	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err2 := l.Acquire(tctx, "key-x")
	if err2 == nil {
		t.Fatal("expected error on second Acquire while lock is held")
	}

	release1()

	// After release the lock should be acquirable again.
	release2, err3 := l.Acquire(ctx, "key-x")
	if err3 != nil {
		t.Fatalf("Acquire after release: %v", err3)
	}
	release2()
}

func TestLocker_ContextCancellation(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	// Hold the lock.
	release, _ := l.Acquire(ctx, "key-cancel")

	cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()

	_, err := l.Acquire(cancelCtx, "key-cancel")
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if err != context.DeadlineExceeded && err != context.Canceled {
		t.Errorf("unexpected error type: %v", err)
	}

	release()
}

func TestLocker_DifferentKeysIndependent(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	r1, err1 := l.Acquire(ctx, "key-a")
	if err1 != nil {
		t.Fatalf("Acquire key-a: %v", err1)
	}

	r2, err2 := l.Acquire(ctx, "key-b")
	if err2 != nil {
		t.Fatalf("Acquire key-b while key-a held: %v", err2)
	}

	r1()
	r2()
}

func TestLocker_OrderingAfterRelease(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	release1, _ := l.Acquire(ctx, "ordered-key")

	var mu sync.Mutex
	var order []int

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r, err := l.Acquire(ctx, "ordered-key")
		if err != nil {
			t.Errorf("goroutine Acquire: %v", err)
			return
		}
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
		r()
	}()

	// Give goroutine time to block on Acquire.
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	order = append(order, 1)
	mu.Unlock()
	release1()

	wg.Wait()

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Errorf("unexpected order: %v", order)
	}
}

func TestLocker_ConcurrentGoroutinesExclusion(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	const goroutines = 10
	const increments = 100

	var counter int
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range increments {
				release, err := l.Acquire(ctx, "counter-key")
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				counter++
				release()
			}
		}()
	}

	wg.Wait()

	if counter != goroutines*increments {
		t.Errorf("counter = %d, want %d (data race detected)", counter, goroutines*increments)
	}
}

func TestLocker_ReleaseIsIdempotent(t *testing.T) {
	l := memstore.NewLocker()
	ctx := context.Background()

	release, err := l.Acquire(ctx, "key-1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	release()

	// Second release should not panic or deadlock; acquire should still work
	release2, err := l.Acquire(ctx, "key-1")
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	release2()
}
