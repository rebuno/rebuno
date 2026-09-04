package postgres

import (
	"context"

	"github.com/rebuno/rebuno/internal/store"
)

var _ store.EventStore = (*Store)(nil)
var _ store.StepStore = (*Store)(nil)
var _ store.ExecutionStore = (*Store)(nil)
var _ store.AgentStore = (*Store)(nil)
var _ store.ApprovalStore = (*Store)(nil)
var _ store.JobQueue = (*Store)(nil)
var _ store.Locker = (*Store)(nil)
var _ store.UnitOfWork = (*Store)(nil)

type txStore struct {
	querier
}

var _ store.TxStore = (*txStore)(nil)

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
