package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rebuno/rebuno/internal/domain"
)

type SessionStore struct {
	pool *pgxpool.Pool
}

func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool}
}

func (s *SessionStore) Create(ctx context.Context, session domain.Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id, execution_id, agent_id, consumer_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, session.ID, session.ExecutionID, session.AgentID, session.ConsumerID,
		session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *SessionStore) Get(ctx context.Context, sessionID string) (*domain.Session, bool, error) {
	var sess domain.Session
	err := s.pool.QueryRow(ctx, `
		SELECT id, execution_id, agent_id, consumer_id, created_at, expires_at
		FROM sessions
		WHERE id = $1
	`, sessionID).Scan(
		&sess.ID, &sess.ExecutionID, &sess.AgentID,
		&sess.ConsumerID, &sess.CreatedAt, &sess.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query session: %w", err)
	}
	return &sess, true, nil
}

func (s *SessionStore) GetByExecution(ctx context.Context, executionID string) (*domain.Session, bool, error) {
	var sess domain.Session
	err := s.pool.QueryRow(ctx, `
		SELECT id, execution_id, agent_id, consumer_id, created_at, expires_at
		FROM sessions
		WHERE execution_id = $1 AND expires_at > now()
		ORDER BY created_at DESC
		LIMIT 1
	`, executionID).Scan(
		&sess.ID, &sess.ExecutionID, &sess.AgentID,
		&sess.ConsumerID, &sess.CreatedAt, &sess.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query session by execution: %w", err)
	}
	return &sess, true, nil
}

func (s *SessionStore) Extend(ctx context.Context, sessionID string, duration time.Duration) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE sessions SET expires_at = now() + $1::interval WHERE id = $2`,
		fmt.Sprintf("%g seconds", duration.Seconds()), sessionID,
	)
	if err != nil {
		return fmt.Errorf("extend session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found: %w", sessionID, domain.ErrNotFound)
	}
	return nil
}

func (s *SessionStore) Delete(ctx context.Context, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE id = $1`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *SessionStore) DeleteExpired(ctx context.Context, gracePeriod time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE expires_at < now() - $1::interval`,
		fmt.Sprintf("%g seconds", gracePeriod.Seconds()),
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
