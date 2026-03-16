package store

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
)

type SignalStore interface {
	Publish(ctx context.Context, executionID string, signal domain.Signal) error
	GetPending(ctx context.Context, executionID string) ([]domain.Signal, error)
	Clear(ctx context.Context, executionID string) error
}
