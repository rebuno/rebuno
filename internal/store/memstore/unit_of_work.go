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
