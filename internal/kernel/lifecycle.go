package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
			{Type: domain.EventStepDenied, Payload: projector.StepErrorPayload(approval.StepID, step.Kind, errPayload)},
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
