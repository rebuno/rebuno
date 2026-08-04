package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/migrations"
)

const migrateLockKey = "rebuno-schema-migrate"

// Migrate reads the embedded migration SQL and executes it against the pool,
// under an advisory lock so concurrent replicas can't race on schema creation.
// Migration files run in lexical order so schema changes apply sequentially.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read migration dir: %w", err)
	}
	var names []string
	for _, f := range files {
		if f.IsDir() || !hasSQLExt(f.Name()) {
			continue
		}
		names = append(names, f.Name())
	}
	sort.Strings(names)

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

	for _, name := range names {
		sqlBytes, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("execute migration %s: %w", name, err)
		}
	}
	return nil
}

func hasSQLExt(name string) bool {
	return len(name) > 4 && name[len(name)-4:] == ".sql"
}
