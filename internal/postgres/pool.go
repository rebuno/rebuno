package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	MaxConns int32
	MinConns int32
}

func NewPool(ctx context.Context, dsn string, opts ...PoolConfig) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}

	if len(opts) > 0 {
		if opts[0].MaxConns > 0 {
			config.MaxConns = opts[0].MaxConns
		}
		if opts[0].MinConns > 0 {
			config.MinConns = opts[0].MinConns
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
