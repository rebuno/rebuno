package store

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
)

type RunnerStore interface {
	Register(ctx context.Context, runner domain.Runner) error
	Get(ctx context.Context, runnerID string) (*domain.Runner, bool, error)
	List(ctx context.Context) ([]domain.Runner, error)
	UpdateHeartbeat(ctx context.Context, runnerID string) error
	Delete(ctx context.Context, runnerID string) error
}
