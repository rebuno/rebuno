package store

import (
	"context"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

type JobQueue interface {
	Enqueue(ctx context.Context, job domain.Job) error
	DequeueForTool(ctx context.Context, toolID string) (*domain.Job, error)
	All(ctx context.Context) ([]domain.Job, error)
	Remove(ctx context.Context, jobID uuid.UUID) error
}
