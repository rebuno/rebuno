package store

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
)

type EventStore interface {
	Append(ctx context.Context, event domain.Event) error
	AppendBatch(ctx context.Context, events []domain.Event) error
	GetByExecution(ctx context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error)
	GetLatestSequence(ctx context.Context, executionID string) (int64, error)
	ListActiveExecutionIDs(ctx context.Context) ([]string, error)
	ListExecutions(ctx context.Context, filter domain.ExecutionFilter, cursor string, limit int) ([]domain.ExecutionSummary, string, error)
	GetExecution(ctx context.Context, executionID string) (*domain.ExecutionSummary, error)
	CreateExecution(ctx context.Context, id, agentID string, labels map[string]string) error
	UpdateExecutionStatus(ctx context.Context, executionID string, status domain.ExecutionStatus) error
	DeleteExecution(ctx context.Context, executionID string) error
	ListTerminalExecutions(ctx context.Context, olderThanSeconds int64, limit int) ([]string, error)
}
