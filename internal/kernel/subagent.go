package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/payload"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/usage"
)

func (k *Kernel) checkParentStep(ctx context.Context, child domain.Execution) error {
	parent, err := authorizedExecution(ctx, k.d.Executions, child.SpawnedBy.ExecutionID)
	if err != nil {
		return err
	}
	if parent.Status.IsTerminal() {
		return fmt.Errorf("%w: the parent execution is terminal", domain.ErrConflict)
	}
	if child.Session != "" && child.Session == parent.Session {
		return fmt.Errorf("%w: a subagent cannot join its parent's session", domain.ErrValidation)
	}
	step, err := k.d.Steps.GetStep(ctx, child.SpawnedBy.StepID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && step.ExecutionID != parent.ID) {
		return fmt.Errorf("%w: spawned_by names no step of the parent execution", domain.ErrValidation)
	}
	if err != nil {
		return err
	}
	if step.Status != domain.StepExecuting {
		return fmt.Errorf("%w: the parent step is in status %s", domain.ErrConflict, step.Status)
	}
	return nil
}

func (k *Kernel) settleParentStep(ctx context.Context, childID uuid.UUID) error {
	child, err := k.d.Executions.GetExecution(ctx, childID)
	if err != nil {
		return err
	}
	if child.SpawnedBy == nil || !child.Status.IsTerminal() {
		return nil
	}
	parentID := child.SpawnedBy.ExecutionID
	return k.d.UnitOfWork.RunLocked(ctx, lockKey(parentID), func(ctx context.Context) error {
		parent, err := k.d.Executions.GetExecution(ctx, parentID)
		if err != nil {
			return err
		}
		if parent.Status.IsTerminal() {
			return nil
		}
		step, err := k.d.Steps.GetStep(ctx, child.SpawnedBy.StepID)
		if err != nil {
			return err
		}
		if step.Status != domain.StepExecuting {
			return nil
		}
		now := time.Now().UTC()
		step.CompletedAt = &now
		var outcome store.EventRecord
		if child.Status == domain.ExecutionCompleted {
			step.Status = domain.StepSucceeded
			step.Result = child.Output
			if len(step.Result) == 0 {
				step.Result = json.RawMessage("null")
			}
			outcome = store.EventRecord{Type: domain.EventStepSucceeded, Payload: payload.StepResult(step.StepID, step.Kind, step.Target, usage.Tokens{})}
		} else {
			step.Status = domain.StepFailed
			step.Error, _ = json.Marshal(map[string]string{
				"reason":         "subagent_" + string(child.Status),
				"execution_id":   child.ID.String(),
				"failure_reason": child.FailureReason,
			})
			outcome = store.EventRecord{Type: domain.EventStepFailed, Payload: payload.StepError(step.StepID, step.Kind, step.Target, step.Error)}
		}
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			if _, err := tx.Append(ctx, parentID, outcome.Type, outcome.Payload); err != nil {
				return err
			}
			if err := tx.Upsert(ctx, step); err != nil {
				return err
			}
			if err := settleEffect(ctx, tx, step); err != nil {
				return err
			}
			if parent.Status != domain.ExecutionBlocked {
				return nil
			}
			return k.resumeIfIdleTx(ctx, tx, parentID, now)
		})
	})
}

type executionLister interface {
	ListExecutions(context.Context, domain.ExecutionFilter) (domain.ExecutionPage, error)
}

func subagents(ctx context.Context, executions executionLister, parentID uuid.UUID) ([]domain.Execution, error) {
	var out []domain.Execution
	filter := domain.ExecutionFilter{SpawnedBy: &parentID, Limit: MaxListExecutionsLimit}
	for {
		page, err := executions.ListExecutions(ctx, filter)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Executions...)
		if page.NextCursor == "" {
			return out, nil
		}
		filter.Cursor = page.NextCursor
	}
}

func (k *Kernel) cancelSubagents(ctx context.Context, parentID uuid.UUID) error {
	children, err := subagents(ctx, k.d.Executions, parentID)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range children {
		if c.Status.IsTerminal() {
			continue
		}
		if err := k.cancelExecution(ctx, c.ID, domain.ReasonParentTerminal); err != nil && !errors.Is(err, domain.ErrExecutionTerminal) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Failed follow-ups are retried by the lifecycle sweeps.
func (k *Kernel) afterTerminal(ctx context.Context, exec domain.Execution) {
	k.releaseSession(ctx, exec.Session)
	if exec.SpawnedBy != nil {
		if err := k.settleParentStep(ctx, exec.ID); err != nil {
			k.log.Warn("settle parent step failed", "execution_id", exec.ID.String(), "error", err)
		}
	}
	if err := k.cancelSubagents(ctx, exec.ID); err != nil {
		k.log.Warn("cancel subagents failed", "execution_id", exec.ID.String(), "error", err)
	}
}

func (k *Kernel) SettleSubagents(ctx context.Context) error {
	unsettled, err := k.d.Executions.ListUnsettledSubagents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, child := range unsettled {
		if child.Status.IsTerminal() {
			err = k.settleParentStep(ctx, child.ID)
		} else {
			err = k.cancelExecution(ctx, child.ID, domain.ReasonParentTerminal)
		}
		if err != nil && !errors.Is(err, domain.ErrExecutionTerminal) && !errors.Is(err, domain.ErrNotFound) {
			errs = append(errs, fmt.Errorf("settle subagent %s: %w", child.ID, err))
		}
	}
	return errors.Join(errs...)
}
