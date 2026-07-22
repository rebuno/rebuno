package stream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// startHub wires a Hub over a MemoryBus and runs its receive loop until the
// test ends.
func startHub(t *testing.T) *Hub {
	t.Helper()
	h := NewHub(NewMemoryBus())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.Start(ctx) }()
	// Give Start a beat to register its deliver callback with the bus.
	time.Sleep(10 * time.Millisecond)
	return h
}

func recv(t *testing.T, ch <-chan Delta) Delta {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta")
		return Delta{}
	}
}

func TestHubDeliversToSubscriber(t *testing.T) {
	h := startHub(t)
	exec := uuid.New()

	ch, cancel := h.Subscribe(exec)
	defer cancel()

	want := Delta{StepID: "step-1", Seq: 1, Data: "hello"}
	if err := h.Publish(context.Background(), exec, want); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := recv(t, ch); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestHubFanoutAndIsolation(t *testing.T) {
	h := startHub(t)
	execA, execB := uuid.New(), uuid.New()

	a1, cancelA1 := h.Subscribe(execA)
	defer cancelA1()
	a2, cancelA2 := h.Subscribe(execA)
	defer cancelA2()
	b1, cancelB1 := h.Subscribe(execB)
	defer cancelB1()

	d := Delta{StepID: "s", Seq: 1, Data: "x"}
	_ = h.Publish(context.Background(), execA, d)

	if got := recv(t, a1); got != d {
		t.Fatalf("a1 got %+v", got)
	}
	if got := recv(t, a2); got != d {
		t.Fatalf("a2 got %+v", got)
	}
	select {
	case got := <-b1:
		t.Fatalf("execB subscriber wrongly received %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubCancelStopsDelivery(t *testing.T) {
	h := startHub(t)
	exec := uuid.New()

	ch, cancel := h.Subscribe(exec)
	cancel()

	_ = h.Publish(context.Background(), exec, Delta{StepID: "s", Seq: 1, Data: "x"})
	select {
	case got := <-ch:
		t.Fatalf("received after cancel: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubDropsWhenSubscriberFull(t *testing.T) {
	h := startHub(t)
	exec := uuid.New()

	ch, cancel := h.Subscribe(exec)
	defer cancel()

	// Publish more than the buffer without draining. deliver must never block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subBuffer*4; i++ {
			_ = h.Publish(context.Background(), exec, Delta{StepID: "s", Seq: int64(i), Data: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	_ = ch
}
