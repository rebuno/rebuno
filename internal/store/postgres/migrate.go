package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/migrations"
)

const migrateLockKey = "rebuno-schema-migrate"

// Migrate reads the embedded migration SQL and executes it against the pool,
// under an advisory lock so concurrent replicas can't race on schema creation.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlBytes, err := fs.ReadFile(migrations.FS, "001_initial.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}

	keyInt := hashKey(migrateLockKey)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", keyInt); err != nil {
		conn.Release()
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer releaseAdvisoryLock(conn, keyInt)

	if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("execute migration: %w", err)
	}
	return nil
}
