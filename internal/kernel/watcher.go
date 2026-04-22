package kernel

import "sync"

// executionWatcher is a concurrency-safe registry that allows SSE handlers
// to receive notifications when new events are emitted for an execution.
type executionWatcher struct {
	mu       sync.Mutex
	watchers map[string]map[chan struct{}]struct{}
}

func newExecutionWatcher() *executionWatcher {
	return &executionWatcher{
		watchers: make(map[string]map[chan struct{}]struct{}),
	}
}

// Watch registers a notification channel for the given execution.
// Returns the channel and an unwatch function that must be called to clean up.
func (w *executionWatcher) Watch(executionID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	w.mu.Lock()
	if w.watchers[executionID] == nil {
		w.watchers[executionID] = make(map[chan struct{}]struct{})
	}
	w.watchers[executionID][ch] = struct{}{}
	w.mu.Unlock()

	return ch, func() {
		w.mu.Lock()
		delete(w.watchers[executionID], ch)
		if len(w.watchers[executionID]) == 0 {
			delete(w.watchers, executionID)
		}
		w.mu.Unlock()
	}
}

// Notify sends a non-blocking signal to all watchers of the given execution.
// Buffered channels (size 1) coalesce rapid notifications.
func (w *executionWatcher) Notify(executionID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	chs := w.watchers[executionID]

	for ch := range chs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
