package kernel

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type executionReader interface {
	GetExecution(context.Context, uuid.UUID) (domain.Execution, error)
}

func authorizedExecution(ctx context.Context, reader executionReader, id uuid.UUID) (domain.Execution, error) {
	exec, err := reader.GetExecution(ctx, id)
	if err != nil {
		return domain.Execution{}, err
	}
	if err := auth.AuthorizeAgent(ctx, exec.AgentID); err != nil {
		return domain.Execution{}, err
	}
	return exec, nil
}

func (k *Kernel) GetExecutionStep(ctx context.Context, execID uuid.UUID, stepID string) (domain.Step, error) {
	if _, err := k.GetExecution(ctx, execID); err != nil {
		return domain.Step{}, err
	}
	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.Step{}, err
	}
	if step.ExecutionID != execID {
		return domain.Step{}, domain.ErrNotFound
	}
	return step, nil
}

func (k *Kernel) CompleteExecutionStep(ctx context.Context, execID uuid.UUID, stepID string, req CompleteStepRequest) (domain.StepDecision, error) {
	if _, err := k.GetExecutionStep(ctx, execID, stepID); err != nil {
		return domain.StepDecision{}, err
	}
	return k.CompleteStep(ctx, stepID, req)
}

func (k *Kernel) FailExecutionStep(ctx context.Context, execID uuid.UUID, stepID string, req FailStepRequest) (domain.StepDecision, error) {
	if _, err := k.GetExecutionStep(ctx, execID, stepID); err != nil {
		return domain.StepDecision{}, err
	}
	return k.FailStep(ctx, stepID, req)
}

func renewAuthorizedLease(ctx context.Context, tx store.TxStore, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	if _, err := authorizedExecution(ctx, tx, execID); err != nil {
		return err
	}
	return tx.RenewLease(ctx, execID, lease, now)
}
