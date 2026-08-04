package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store"
)

func (k *Kernel) ExpireApprovals(ctx context.Context, now time.Time) error {
	approvals, err := k.d.Approvals.ListExpiredApprovals(ctx, now)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		if err := k.expireApproval(ctx, approval, now); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) CancelExpiredExecutions(ctx context.Context, now time.Time) error {
	executions, err := k.d.Executions.ListExpiredExecutions(ctx, now)
	if err != nil {
		return err
	}
	for _, exec := range executions {
		if err := k.CancelExecution(ctx, exec.ID); err != nil {
			if errors.Is(err, domain.ErrExecutionTerminal) || errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return err
		}
	}
	return nil
}

// ReclaimStalledExecutions re-dispatches executions whose steps have been in
// the executing state longer than the configured StepStalledTimeout. An agent
// that dies mid-step after acking the dispatch leaves the execution stuck: the
// dispatch is no longer in_flight so ReclaimStalled never touches it, and no
// heartbeat arrives to refresh the step. This worker detects such steps and
// enqueues a fresh dispatch so the agent (on reconnect) replays recorded steps
// and recovers the orphaned one per its idempotency mode.
func (k *Kernel) ReclaimStalledExecutions(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-k.cfg.StepStalledTimeout)
	steps, err := k.d.Steps.ListStalledSteps(ctx, cutoff)
	if err != nil {
		return err
	}
	seen := make(map[uuid.UUID]struct{}, len(steps))
	for _, step := range steps {
		if _, ok := seen[step.ExecutionID]; ok {
			continue
		}
		seen[step.ExecutionID] = struct{}{}
		if err := k.reclaimStalledExecution(ctx, step.ExecutionID, step.StepID); err != nil {
			k.log.Warn("reclaim stalled execution failed", "execution_id", step.ExecutionID, "step_id", step.StepID, "error", err)
		}
	}
	return nil
}

// reclaimStalledExecution re-dispatches a single execution if it is still
// running and still has a step in the executing state. The per-execution lock
// and re-checks guard against races with concurrent step completion, cancel,
// or another replica's re-dispatch.
func (k *Kernel) reclaimStalledExecution(ctx context.Context, execID uuid.UUID, stepID string) error {
	release, err := k.d.Locker.Acquire(ctx, lockKey(execID))
	if err != nil {
		return err
	}
	defer release()

	exec, err := k.d.Executions.GetExecution(ctx, execID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if exec.Status != domain.ExecutionRunning {
		return nil
	}
	// Re-check the step under the lock: it may have completed or been
	// cancelled between the ListStalledSteps scan and now.
	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if step.Status != domain.StepExecuting {
		return nil
	}
	// Skip if a live (pending/in_flight/failed) dispatch already exists: the
	// agent may already have been re-dispatched by another path.
	dispatches, err := k.d.Queue.ListDispatchesByExecution(ctx, execID)
	if err != nil {
		return err
	}
	for _, d := range dispatches {
		if d.Status == domain.DispatchPending || d.Status == domain.DispatchInFlight || d.Status == domain.DispatchFailed {
			return nil
		}
	}
	if err := k.EnqueueReDrive(ctx, execID); err != nil {
		return err
	}
	k.log.Info("re-dispatched stalled execution", "execution_id", execID, "step_id", stepID)
	k.d.Observer.RecordExecutionRedispatched()
	return nil
}

func (k *Kernel) Cleanup(ctx context.Context, retain time.Duration, now time.Time) error {
	if retain <= 0 {
		return nil
	}
	cutoff := now.Add(-retain)
	if r, ok := k.d.RateLimiter.(ratelimit.Reaper); ok {
		if err := r.ReapBefore(ctx, cutoff); err != nil {
			k.log.Warn("rate limit reap failed", "error", err) // best-effort
		}
	}
	return k.d.Executions.DeleteExecutionsCreatedBefore(ctx, cutoff)
}

func (k *Kernel) expireApproval(ctx context.Context, approval domain.Approval, now time.Time) error {
	release, err := k.d.Locker.Acquire(ctx, lockKey(approval.ExecutionID))
	if err != nil {
		return err
	}
	defer release()

	approval, _ = k.d.Approvals.GetApproval(ctx, approval.ID)
	if approval.Status != domain.ApprovalPending || approval.TimeoutAt.After(now) {
		return nil
	}
	approval.Status = domain.ApprovalExpired
	approval.DecidedAt = &now
	approval.Rationale = "timeout"

	errPayload, _ := json.Marshal(map[string]string{"reason": "approval_timeout"})
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		step, err := tx.GetStep(ctx, approval.StepID)
		if err != nil {
			return err
		}
		evts := []store.EventRecord{
			{Type: domain.EventApprovalExpired, Payload: projector.ApprovalPayload(approval.ID, approval.StepID, approval.ExecutionID, domain.ApprovalExpired, "", "timeout")},
			{Type: domain.EventStepDenied, Payload: projector.StepDeniedPayload(approval.StepID, step.Kind, step.Target, "", errPayload)},
			{Type: domain.EventExecutionFailed, Payload: projector.ExecutionPayload(approval.ExecutionID, domain.ExecutionFailed, nil, "approval_timeout")},
		}
		if _, err := tx.AppendBatch(ctx, approval.ExecutionID, evts); err != nil {
			return err
		}
		step.Status = domain.StepDenied
		step.Error = errPayload
		step.CompletedAt = &now
		if err := tx.Upsert(ctx, step); err != nil {
			return err
		}
		if err := tx.UpdateApproval(ctx, approval); err != nil {
			return err
		}
		return tx.UpdateExecutionStatus(ctx, approval.ExecutionID, domain.ExecutionFailed, nil, "approval_timeout")
	}); err != nil {
		return err
	}
	k.d.Observer.RecordApprovalOutcome("expired")
	return nil
}
