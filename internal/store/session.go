package store

import (
	"context"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type SessionStore interface {
	Create(ctx context.Context, session domain.Session) error
	Get(ctx context.Context, sessionID string) (*domain.Session, bool, error)
	GetByExecution(ctx context.Context, executionID string) (*domain.Session, bool, error)
	Extend(ctx context.Context, sessionID string, duration time.Duration) error
	Delete(ctx context.Context, sessionID string) error
	DeleteExpired(ctx context.Context, gracePeriod time.Duration) (int, error)
}
