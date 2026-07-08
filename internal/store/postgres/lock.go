package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/store"
)

var _ store.Locker = (*Store)(nil)

// Acquire obtains a Postgres session-level advisory lock on a deterministic
// integer derived from key. The returned release function must be called to
// free the lock and return the connection to the pool.
func (s *Store) Acquire(ctx context.Context, key string) (func(), error) {
	keyInt := hashKey(key)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", keyInt); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
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

// releaseAdvisoryLock frees the session-level advisory lock and returns the
// connection to the pool. It deliberately uses a fresh background context: the
// caller's context is often already cancelled by the time the deferred release
// runs (request timeout, client disconnect), and unlocking with a cancelled
// context would skip the unlock and return a lock-holding connection to the
// pool — leaking the lock and wedging every future operation on that key.
//
// If the unlock fails for any reason, the connection is hijacked out of the pool
// and closed rather than returned: ending the session releases any session-level
// advisory locks server-side, so a connection that might still hold the lock is
// never reused.
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

// TryAcquire attempts a non-blocking Postgres session-level advisory lock. It
// returns a release function when the lock is acquired, or (nil, nil) when it is
// already held by another session.
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
