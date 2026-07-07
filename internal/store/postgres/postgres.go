package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/kernel/internal/domain"
)

// Querier is the subset of *pgxpool.Pool and pgx.Tx used by the store.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is a durable Postgres-backed implementation of the store interfaces.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// querier bundles helpers that accept either a connection-pool or a transaction.
type querier struct {
	q Querier
}

func marshalPayload(payload any) (string, error) {
	switch v := payload.(type) {
	case nil:
		return "{}", nil
	case json.RawMessage:
		if v == nil {
			return "{}", nil
		}
		return string(v), nil
	case []byte:
		if v == nil {
			return "{}", nil
		}
		return string(v), nil
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("marshal payload: %w", err)
		}
		return string(b), nil
	}
}

func rawArg(r json.RawMessage) any {
	if r == nil {
		return nil
	}
	return string(r)
}

func rawFromPtr(s *string) json.RawMessage {
	if s == nil {
		return nil
	}
	return json.RawMessage(*s)
}

func timeArg(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return *t
}

func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func hashKey(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}
