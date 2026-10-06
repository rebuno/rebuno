package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/payload"
	"github.com/rebuno/rebuno/internal/store"
)

func (k *Kernel) Suspend(ctx context.Context, execID uuid.UUID, lease domain.Lease) (bool, error) {
	if !lease.Valid() {
		return false, fmt.Errorf("%w: missing dispatch lease", domain.ErrValidation)
	}
	var suspended bool
	err := k.d.UnitOfWork.RunLocked(ctx, lockKey(execID), func(ctx context.Context) error {
		exec, err := authorizedExecution(ctx, k.d.Executions, execID)
		if err != nil {
			return err
		}
		if exec.Status.IsTerminal() {
			return domain.ErrExecutionTerminal
		}
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			if err := renewAuthorizedLease(ctx, tx, execID, lease, time.Now().UTC()); err != nil {
				return err
			}
			waiting, err := awaitingSteps(ctx, tx, execID)
			if err != nil || !waiting {
				return err
			}
			suspended = true
			if _, err := tx.Append(ctx, execID, domain.EventExecutionBlocked, payload.Execution(execID, domain.ExecutionBlocked, nil, domain.ReasonAwaitingSteps)); err != nil {
				return err
			}
			if err := tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionBlocked, nil, ""); err != nil {
				return err
			}
			return releaseDispatchesLocked(ctx, tx, execID)
		})
	})
	return suspended, err
}

func awaitingSteps(ctx context.Context, tx store.TxStore, execID uuid.UUID) (bool, error) {
	steps, err := tx.ListByExecution(ctx, execID)
	if err != nil {
		return false, err
	}
	executing := make(map[string]bool)
	for _, s := range steps {
		switch s.Status {
		case domain.StepAwaitingApproval:
			return true, nil
		case domain.StepExecuting:
			executing[s.StepID] = true
		}
	}
	if len(executing) == 0 {
		return false, nil
	}
	children, err := subagents(ctx, tx, execID)
	if err != nil {
		return false, err
	}
	for _, c := range children {
		if executing[c.SpawnedBy.StepID] {
			return true, nil
		}
	}
	return false, nil
}

func (k *Kernel) resumeIfIdleTx(ctx context.Context, tx store.TxStore, execID uuid.UUID, now time.Time) error {
	waiting, err := awaitingSteps(ctx, tx, execID)
	if err != nil || waiting {
		return err
	}
	if _, err := tx.Append(ctx, execID, domain.EventExecutionResumed, payload.Execution(execID, domain.ExecutionRunning, nil, "")); err != nil {
		return err
	}
	if err := tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionRunning, nil, ""); err != nil {
		return err
	}
	return k.enqueueDispatchTx(ctx, tx, execID, now)
}
