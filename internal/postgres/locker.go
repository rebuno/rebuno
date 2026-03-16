package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Locker struct {
	pool *pgxpool.Pool
}

func NewLocker(pool *pgxpool.Pool) *Locker {
	return &Locker{pool: pool}
}

func (l *Locker) Acquire(ctx context.Context, key string) (func(), error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for advisory lock: %w", err)
	}

	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, key)
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire advisory lock for %q: %w", key, err)
	}

	release := func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, key)
		conn.Release()
	}

	return release, nil
}
