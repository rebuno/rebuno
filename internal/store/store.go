package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

type EventStore interface {
	Append(ctx context.Context, execID uuid.UUID, eventType string, payload any) (domain.Event, error)
	// AppendBatch writes multiple events atomically with sequential event_seqs.
	AppendBatch(ctx context.Context, execID uuid.UUID, events []EventRecord) ([]domain.Event, error)
	GetEvents(ctx context.Context, execID uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error)
	GetLatestSequence(ctx context.Context, execID uuid.UUID) (int64, error)
}

type StepStore interface {
	Upsert(ctx context.Context, step domain.Step) error
	GetStep(ctx context.Context, stepID string) (domain.Step, error)
	CountOccurrence(ctx context.Context, execID uuid.UUID, kind domain.StepKind, target, argsHash string) (int, error)
	ListByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Step, error)
}

type ExecutionStore interface {
	CreateExecution(ctx context.Context, exec domain.Execution) error
	GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error)
	ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error)
	UpdateExecutionStatus(ctx context.Context, id uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) error
	ListExpiredExecutions(ctx context.Context, now time.Time) ([]domain.Execution, error)
	DeleteExecutionsCreatedBefore(ctx context.Context, before time.Time) error
}

type AgentStore interface {
	RegisterAgent(ctx context.Context, agent domain.Agent) error
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
	ListAgents(ctx context.Context) ([]domain.Agent, error)
	DeleteAgent(ctx context.Context, id string) error
}

type ApprovalStore interface {
	CreateApproval(ctx context.Context, approval domain.Approval) error
	GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error)
	UpdateApproval(ctx context.Context, approval domain.Approval) error
	ListPendingApprovals(ctx context.Context) ([]domain.Approval, error)
	ListExpiredApprovals(ctx context.Context, now time.Time) ([]domain.Approval, error)
}

type JobQueue interface {
	Enqueue(ctx context.Context, d domain.Dispatch) error
	Claim(ctx context.Context, replica string, batch int, now time.Time) ([]domain.Dispatch, error)
	Ack(ctx context.Context, id uuid.UUID, status domain.DispatchStatus, nextAttemptAt *time.Time) error
	ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error)
	// ReclaimStalled resets in_flight dispatches whose lease has expired back to
	// pending so another replica can claim them.
	ReclaimStalled(ctx context.Context, now time.Time, leaseTimeout time.Duration, batch int) ([]domain.Dispatch, error)
}

type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
	// TryAcquire attempts to acquire the lock without blocking. It returns a
	// release function when the lock is acquired, or (nil, nil) when the lock
	// is held by another caller.
	TryAcquire(ctx context.Context, key string) (release func(), err error)
}

// UnitOfWork runs a function inside a single transaction. The TxStore passed
// to fn is backed by the same transaction so that all operations commit
// atomically.
type UnitOfWork interface {
	RunInTx(ctx context.Context, fn func(TxStore) error) error
}

// TxStore is the union of stores that can participate in a unit of work.
type TxStore interface {
	EventStore
	StepStore
	ExecutionStore
	ApprovalStore
	JobQueue
}

type EventRecord struct {
	Type    string
	Payload any
}
