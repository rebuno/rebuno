package postgres

import (
	"context"
	"sync"
)

// keyedGate keeps same-key waiters out of the database pool.
type keyedGate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

type gateEntry struct {
	token chan struct{}
	refs  int // Holder plus waiters.
}

func (g *keyedGate) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*gateEntry)
	}
	entry := g.entries[key]
	if entry == nil {
		entry = &gateEntry{token: make(chan struct{}, 1)}
		g.entries[key] = entry
	}
	entry.refs++
	g.mu.Unlock()

	dropRef := func() {
		g.mu.Lock()
		defer g.mu.Unlock()

		entry.refs--
		if entry.refs == 0 {
			delete(g.entries, key)
		}
	}

	select {
	case entry.token <- struct{}{}:
		return sync.OnceFunc(func() {
			<-entry.token
			dropRef()
		}), nil
	case <-ctx.Done():
		dropRef()
		return nil, ctx.Err()
	}
}
