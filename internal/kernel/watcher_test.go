package kernel

import (
	"sync"
	"testing"
)

func TestExecutionWatcher_NotifyUnwatchConcurrent(t *testing.T) {
	// Regression test for issue #48: concurrent map iteration and write
	// in Notify and the unwatch cleanup function.
	w := newExecutionWatcher()
	const execID = "exec-race"
	const goroutines = 50
	const iterations = 200

	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				ch, unwatch := w.Watch(execID)
				w.Notify(execID)
				// Drain notification so the channel can be reused.
				select {
				case <-ch:
				default:
				}
				unwatch()
			}
		}()
	}

	wg.Wait()

	// After all goroutines finish, the watcher map should be empty.
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.watchers) != 0 {
		t.Fatalf("expected empty watchers map, got %d entries", len(w.watchers))
	}
}

func TestExecutionWatcher_WatchNotify(t *testing.T) {
	w := newExecutionWatcher()

	ch, unwatch := w.Watch("exec-1")
	defer unwatch()

	w.Notify("exec-1")

	select {
	case <-ch:
	default:
		t.Fatal("expected notification on channel")
	}
}

func TestExecutionWatcher_UnwatchCleansUp(t *testing.T) {
	w := newExecutionWatcher()

	_, unwatch := w.Watch("exec-1")
	unwatch()

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.watchers["exec-1"]; ok {
		t.Fatal("expected execution entry to be removed after last unwatch")
	}
}

func TestExecutionWatcher_NotifyNoWatchers(t *testing.T) {
	w := newExecutionWatcher()
	// Should not panic.
	w.Notify("nonexistent")
}
