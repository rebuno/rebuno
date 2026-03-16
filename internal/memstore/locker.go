package memstore

import (
	"context"
	"sync"
)

// Locker is an in-memory implementation of store.Locker.
// Each key gets its own buffered channel (capacity 1) so that Acquire can
// respect context cancellation while still providing mutual exclusion.
type Locker struct {
	mu   sync.Mutex
	keys map[string]chan struct{}
}

func NewLocker() *Locker {
	return &Locker{keys: make(map[string]chan struct{})}
}

// Acquire obtains the lock for key. It blocks until the lock is available or
// ctx is cancelled. The returned release func must be called to unlock.
func (l *Locker) Acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	ch, ok := l.keys[key]
	if !ok {
		ch = make(chan struct{}, 1)
		l.keys[key] = ch
	}
	l.mu.Unlock()

	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
