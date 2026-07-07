package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/kernel/migrations"
)

// Migrate reads the embedded migration SQL and executes it against the pool.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlBytes, err := fs.ReadFile(migrations.FS, "001_initial.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("execute migration: %w", err)
	}
	return nil
}
