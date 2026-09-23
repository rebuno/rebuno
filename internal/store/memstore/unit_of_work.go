package memstore

import (
	"context"

	"github.com/rebuno/rebuno/internal/store"
)

type txStore struct {
	*Store
}

var _ store.TxStore = (*txStore)(nil)

// Does NOT roll back: if fn errors, mutations already applied persist. A real
// backend must roll back on error; do not rely on that here.
func (s *Store) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &txStore{Store: s}
	return fn(tx)
}

// Holds only the key lock: each store call inside fn applies on its own, and a
// RunInTx inside fn is atomic with respect to other callers.
func (s *Store) RunLocked(ctx context.Context, key string, fn func(context.Context) error) error {
	release, err := s.acquire(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}
