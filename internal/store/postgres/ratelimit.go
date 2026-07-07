package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/ratelimit"
)

var _ ratelimit.Limiter = (*Store)(nil)
var _ ratelimit.Reaper = (*Store)(nil)

// Allow performs an atomic token-bucket admission decision shared across all
// replicas. The refill-and-decrement happens in a single statement under the
// row lock that ON CONFLICT takes, so concurrent replicas serialize correctly
// on the key. A denied call consumes no token: the WHERE on DO UPDATE skips the
// decrement, RETURNING yields no row, and we read that as "denied".
func (s *Store) Allow(ctx context.Context, key ratelimit.Key, cfg domain.RateLimitConfig) (bool, time.Duration, error) {
	if cfg.MaxCalls <= 0 || cfg.Window <= 0 {
		return true, 0, nil
	}
	now := time.Now().UTC()
	var tokens float64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO rate_buckets AS rb (key, tokens, max_tokens, window_seconds, updated_at)
		VALUES ($1, $2::float8 - 1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE
		SET tokens = LEAST(
		        rb.max_tokens::float8,
		        rb.tokens + EXTRACT(EPOCH FROM ($4 - rb.updated_at)) * rb.max_tokens / rb.window_seconds
		    ) - 1,
		    max_tokens     = $2,
		    window_seconds = $3,
		    updated_at     = $4
		WHERE LEAST(
		        rb.max_tokens::float8,
		        rb.tokens + EXTRACT(EPOCH FROM ($4 - rb.updated_at)) * rb.max_tokens / rb.window_seconds
		    ) >= 1
		RETURNING tokens
	`, string(key), cfg.MaxCalls, cfg.Window.Seconds(), now).Scan(&tokens)

	if errors.Is(err, pgx.ErrNoRows) {
		// No row returned: the refilled balance was < 1. Denied, no token spent.
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("rate limit allow: %w", err)
	}
	return true, 0, nil
}

// ReapBefore deletes buckets untouched since cutoff. Called from the leader's
// cleanup worker to bound table growth.
func (s *Store) ReapBefore(ctx context.Context, cutoff time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM rate_buckets WHERE updated_at < $1`, cutoff); err != nil {
		return fmt.Errorf("reap rate buckets: %w", err)
	}
	return nil
}
