package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func releaseAdvisoryLock(conn *pgxpool.Conn, keyInt int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", keyInt); err != nil {
		raw := conn.Hijack()
		_ = raw.Close(ctx)
		return
	}
	conn.Release()
}

func (s *Store) TryAcquire(ctx context.Context, key string) (func(), error) {
	keyInt := hashKey(key)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", keyInt).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("try advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, nil
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		releaseAdvisoryLock(conn, keyInt)
	}, nil
}
