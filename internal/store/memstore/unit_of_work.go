package memstore

import (
	"context"

	"github.com/rebuno/kernel/internal/store"
)

type txStore struct {
	*Store
}

var _ store.TxStore = (*txStore)(nil)

// RunInTx runs fn under the store's global write lock, so writes inside fn are
// atomic. It does NOT roll back: if fn errors, mutations already applied
// persist. A real backend must roll back on error; do not rely on that here.
func (s *Store) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx := &txStore{Store: s}
	return fn(tx)
}
