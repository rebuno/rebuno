package postgres

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"testing/synctest"
)

func TestKeyedGateSerializesWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate keyedGate
		release, err := gate.acquire(t.Context(), "execution")
		if err != nil {
			t.Fatal(err)
		}
		defer release()

		const waiters = 4
		completed := 0
		for range waiters {
			go func() {
				release, err := gate.acquire(t.Context(), "execution")
				if err != nil {
					t.Error(err)
					return
				}
				defer release()
				n := completed
				runtime.Gosched()
				completed = n + 1
			}()
		}
		synctest.Wait()
		if completed != 0 {
			t.Fatal("waiter entered while the gate was held")
		}

		releaseOther, err := gate.acquire(t.Context(), "other-execution")
		if err != nil {
			t.Fatal(err)
		}
		releaseOther()

		release()
		release()
		synctest.Wait()
		if completed != waiters {
			t.Fatalf("completed %d operations, want %d", completed, waiters)
		}
	})
}

func TestKeyedGateCanceledWaiterPreservesExclusion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate keyedGate
		release, err := gate.acquire(t.Context(), "execution")
		if err != nil {
			t.Fatal(err)
		}
		defer release()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var canceledErr error
		go func() {
			release, err := gate.acquire(ctx, "execution")
			canceledErr = err
			if release != nil {
				release()
				t.Error("canceled waiter acquired the gate")
			}
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		if !errors.Is(canceledErr, context.Canceled) {
			t.Fatalf("canceled waiter returned %v", canceledErr)
		}

		acquired := false
		go func() {
			release, err := gate.acquire(t.Context(), "execution")
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			acquired = true
		}()
		synctest.Wait()
		if acquired {
			t.Fatal("canceling a waiter released the holder's gate")
		}

		release()
		synctest.Wait()
		if !acquired {
			t.Fatal("remaining waiter did not acquire the gate")
		}
		if release, err := gate.acquire(ctx, "execution"); release != nil {
			release()
			t.Fatal("acquired a gate with an already canceled context")
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire returned %v", err)
		}
		if len(gate.entries) != 0 {
			t.Fatal("idle gate retained after cancellation and release")
		}
	})
}
