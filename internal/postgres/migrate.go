package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS, dir string) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration lock: %w", err)
	}
	defer conn.Release()

	// migrationLockID is the PostgreSQL advisory lock ID used to serialize migrations.
	// Chosen as a distinctive constant to avoid collisions with application-level locks.
	const migrationLockID = 8675309
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    TEXT PRIMARY KEY,
			checksum    TEXT NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		path := name
		if dir != "." {
			path = dir + "/" + name
		}
		data, err := fs.ReadFile(migrations, path)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		checksum := fmt.Sprintf("%x", sha256.Sum256(data))

		var existingChecksum string
		err = conn.QueryRow(ctx,
			`SELECT checksum FROM schema_migrations WHERE filename = $1`,
			name,
		).Scan(&existingChecksum)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if err == nil {
			if existingChecksum != checksum {
				return fmt.Errorf("migration %s has been modified after being applied (expected checksum %s, got %s)",
					name, existingChecksum, checksum)
			}
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for migration %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, string(data)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("exec migration %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)`,
			name, checksum,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}
