package postgres

import (
	"context"

	"github.com/rebuno/rebuno/internal/store"
)

// Compile-time interface checks for the non-transactional Store.
var _ store.EventStore = (*Store)(nil)
var _ store.StepStore = (*Store)(nil)
var _ store.ExecutionStore = (*Store)(nil)
var _ store.AgentStore = (*Store)(nil)
var _ store.ApprovalStore = (*Store)(nil)
var _ store.JobQueue = (*Store)(nil)
var _ store.Locker = (*Store)(nil)
var _ store.UnitOfWork = (*Store)(nil)

// txStore is the union of stores backed by a single pgx transaction. Callbacks
// passed to RunInTx receive a txStore so that every operation shares the same
// transaction.
type txStore struct {
	querier
}

var _ store.TxStore = (*txStore)(nil)

// RunInTx executes fn inside a single Postgres transaction. The provided TxStore
// is backed by that transaction; on a non-nil return from fn the transaction
// is rolled back, otherwise it is committed.
func (s *Store) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := fn(&txStore{querier: querier{q: tx}}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
