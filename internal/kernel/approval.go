package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/store"
)

type GrantApprovalRequest struct {
	DecidedBy string `json:"decided_by"`
	Rationale string `json:"rationale,omitempty"`
}

type DenyApprovalRequest struct {
	DecidedBy string `json:"decided_by"`
	Rationale string `json:"rationale,omitempty"`
}

func (k *Kernel) GrantApproval(ctx context.Context, id uuid.UUID, req GrantApprovalRequest) error {
	approval, err := k.d.Approvals.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	release, err := k.d.Locker.Acquire(ctx, lockKey(approval.ExecutionID))
	if err != nil {
		return err
	}
	defer release()

	approval, _ = k.d.Approvals.GetApproval(ctx, id)
	if approval.Status != domain.ApprovalPending {
		return domain.ErrConflict
	}
	if !approval.AllowsApprover(req.DecidedBy) {
		return fmt.Errorf("%w: %q is not an approver for this approval", domain.ErrForbidden, req.DecidedBy)
	}
	now := time.Now().UTC()
	approval.Status = domain.ApprovalGranted
	approval.DecidedBy = req.DecidedBy
	approval.DecidedAt = &now
	approval.Rationale = req.Rationale

	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		step, err := tx.GetStep(ctx, approval.StepID)
		if err != nil {
			return err
		}
		evts := []store.EventRecord{
			{Type: domain.EventApprovalGranted, Payload: projector.ApprovalPayload(approval.ID, approval.StepID, approval.ExecutionID, domain.ApprovalGranted, req.DecidedBy, req.Rationale)},
			{Type: domain.EventStepAllowed, Payload: projector.StepPayload(approval.StepID, step.Kind, "", "")},
			{Type: domain.EventExecutionResumed, Payload: projector.ExecutionPayload(approval.ExecutionID, domain.ExecutionRunning, nil, "")},
		}
		if _, err := tx.AppendBatch(ctx, approval.ExecutionID, evts); err != nil {
			return err
		}
		// Update the projected step so replay sees it as allowed/ready.
		step.Status = domain.StepAllowed
		if err := tx.Upsert(ctx, step); err != nil {
			return err
		}
		if err := tx.UpdateApproval(ctx, approval); err != nil {
			return err
		}
		if err := tx.UpdateExecutionStatus(ctx, approval.ExecutionID, domain.ExecutionRunning, nil, ""); err != nil {
			return err
		}
		// Resume the execution by enqueueing a dispatch atomically.
		return k.enqueueDispatchTx(ctx, tx, approval.ExecutionID)
	}); err != nil {
		return err
	}
	k.d.Observer.RecordApprovalOutcome("granted")
	return nil
}

func (k *Kernel) DenyApproval(ctx context.Context, id uuid.UUID, req DenyApprovalRequest) error {
	approval, err := k.d.Approvals.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	release, err := k.d.Locker.Acquire(ctx, lockKey(approval.ExecutionID))
	if err != nil {
		return err
	}
	defer release()

	approval, _ = k.d.Approvals.GetApproval(ctx, id)
	if approval.Status != domain.ApprovalPending {
		return domain.ErrConflict
	}
	if !approval.AllowsApprover(req.DecidedBy) {
		return fmt.Errorf("%w: %q is not an approver for this approval", domain.ErrForbidden, req.DecidedBy)
	}
	now := time.Now().UTC()
	approval.Status = domain.ApprovalDenied
	approval.DecidedBy = req.DecidedBy
	approval.DecidedAt = &now
	approval.Rationale = req.Rationale

	errPayload, _ := json.Marshal(map[string]string{"reason": "approval_denied"})
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		step, err := tx.GetStep(ctx, approval.StepID)
		if err != nil {
			return err
		}
		evts := []store.EventRecord{
			{Type: domain.EventApprovalDenied, Payload: projector.ApprovalPayload(approval.ID, approval.StepID, approval.ExecutionID, domain.ApprovalDenied, req.DecidedBy, req.Rationale)},
			{Type: domain.EventStepDenied, Payload: projector.StepPayload(approval.StepID, step.Kind, "", "")},
			{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(approval.StepID, step.Kind, errPayload)},
			{Type: domain.EventExecutionFailed, Payload: projector.ExecutionPayload(approval.ExecutionID, domain.ExecutionFailed, nil, "approval_denied")},
		}
		if _, err := tx.AppendBatch(ctx, approval.ExecutionID, evts); err != nil {
			return err
		}
		if err := tx.UpdateApproval(ctx, approval); err != nil {
			return err
		}
		step.Status = domain.StepFailed
		step.Error = errPayload
		step.CompletedAt = &now
		if err := tx.Upsert(ctx, step); err != nil {
			return err
		}
		return tx.UpdateExecutionStatus(ctx, approval.ExecutionID, domain.ExecutionFailed, nil, "approval_denied")
	}); err != nil {
		return err
	}
	k.d.Observer.RecordApprovalOutcome("denied")
	return nil
}

func (k *Kernel) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return k.d.Approvals.ListPendingApprovals(ctx)
}

func (k *Kernel) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	return k.d.Approvals.GetApproval(ctx, id)
}
