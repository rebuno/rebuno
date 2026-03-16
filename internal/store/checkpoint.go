package store

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
)

type CheckpointStore interface {
	Get(ctx context.Context, executionID string) (*domain.Checkpoint, bool, error)
	Save(ctx context.Context, checkpoint domain.Checkpoint) error
	Delete(ctx context.Context, executionID string) error
}
