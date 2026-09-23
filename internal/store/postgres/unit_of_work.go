package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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

type txKey struct{}

// q returns the transaction RunLocked placed in ctx, so a locked section never
// needs a second pool connection.
func (s *Store) q(ctx context.Context) Querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return s.pool
}

func (s *Store) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	begin := s.pool.Begin
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		begin = tx.Begin
	}
	tx, err := begin(ctx)
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

// RunLocked queues same-key callers locally first, so waiters for one
// execution hold no connection. The advisory lock serializes across replicas.
func (s *Store) RunLocked(ctx context.Context, key string, fn func(context.Context) error) error {
	release, err := s.locks.acquire(ctx, key)
	if err != nil {
		return err
	}
	defer release()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// A failed rollback closes the connection, which also frees the lock.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", hashKey(key)); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
