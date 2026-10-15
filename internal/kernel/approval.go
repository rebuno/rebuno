package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/payload"
	"github.com/rebuno/rebuno/internal/store"
)

type GrantApprovalRequest struct {
	DecidedBy string `json:"decided_by,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

type DenyApprovalRequest struct {
	DecidedBy string `json:"decided_by,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

func approvalDecider(ctx context.Context, named string) (decidedBy, assertedBy string, err error) {
	caller := auth.Identity(ctx)
	if caller == "" {
		return "", "", domain.ErrUnauthorized
	}
	if named == "" {
		return caller, "", nil
	}
	if !auth.HasScope(ctx, domain.ScopeApprovalsDelegate) {
		return "", "", fmt.Errorf("%w: decided_by requires the %s scope", domain.ErrForbidden, domain.ScopeApprovalsDelegate)
	}
	return named, caller, nil
}

func approverMatcher(ctx context.Context, decidedBy, assertedBy string) func([]string) bool {
	if assertedBy != "" {
		return func(approvers []string) bool { return slices.Contains(approvers, decidedBy) }
	}
	return func(approvers []string) bool { return auth.Matches(ctx, approvers) }
}

func requireAwaitingApproval(step domain.Step) error {
	if step.Status != domain.StepAwaitingApproval {
		return fmt.Errorf("%w: approval step is in status %s", domain.ErrConflict, step.Status)
	}
	return nil
}

func (k *Kernel) GrantApproval(ctx context.Context, id uuid.UUID, req GrantApprovalRequest) error {
	decidedBy, assertedBy, err := approvalDecider(ctx, req.DecidedBy)
	if err != nil {
		return err
	}
	approval, err := k.d.Approvals.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	if _, err := k.authorizedExecution(ctx, k.d.Executions, approval.ExecutionID, auth.RoleApprove); err != nil {
		return err
	}
	if err := k.d.UnitOfWork.RunLocked(ctx, lockKey(approval.ExecutionID), func(ctx context.Context) error {
		approval, _ = k.d.Approvals.GetApproval(ctx, id)
		if approval.Status != domain.ApprovalPending {
			return domain.ErrConflict
		}
		if !approval.AllowsApprover(approverMatcher(ctx, decidedBy, assertedBy)) {
			return fmt.Errorf("%w: %q is not an approver for this approval", domain.ErrForbidden, decidedBy)
		}
		now := time.Now().UTC()
		approval.Status = domain.ApprovalGranted
		approval.DecidedBy = decidedBy
		approval.AssertedBy = assertedBy
		approval.DecidedAt = &now
		approval.Rationale = req.Rationale

		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			step, err := tx.GetStep(ctx, approval.StepID)
			if err != nil {
				return err
			}
			if err := requireAwaitingApproval(step); err != nil {
				return err
			}
			evts := []store.EventRecord{
				{Type: domain.EventApprovalGranted, Payload: payload.Approval(approval.ID, approval.StepID, approval.ExecutionID, domain.ApprovalGranted, decidedBy, assertedBy, req.Rationale)},
				{Type: domain.EventStepAllowed, Payload: payload.Step(approval.StepID, step.Kind, step.Target, "")},
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
			return k.resumeIfIdleTx(ctx, tx, approval.ExecutionID, now)
		})
	}); err != nil {
		return err
	}
	k.d.Observer.RecordApprovalOutcome("granted")
	return nil
}

func (k *Kernel) DenyApproval(ctx context.Context, id uuid.UUID, req DenyApprovalRequest) error {
	decidedBy, assertedBy, err := approvalDecider(ctx, req.DecidedBy)
	if err != nil {
		return err
	}
	approval, err := k.d.Approvals.GetApproval(ctx, id)
	if err != nil {
		return err
	}
	if _, err := k.authorizedExecution(ctx, k.d.Executions, approval.ExecutionID, auth.RoleApprove); err != nil {
		return err
	}
	if err := k.d.UnitOfWork.RunLocked(ctx, lockKey(approval.ExecutionID), func(ctx context.Context) error {
		approval, _ = k.d.Approvals.GetApproval(ctx, id)
		if approval.Status != domain.ApprovalPending {
			return domain.ErrConflict
		}
		if !approval.AllowsApprover(approverMatcher(ctx, decidedBy, assertedBy)) {
			return fmt.Errorf("%w: %q is not an approver for this approval", domain.ErrForbidden, decidedBy)
		}
		now := time.Now().UTC()
		approval.Status = domain.ApprovalDenied
		approval.DecidedBy = decidedBy
		approval.AssertedBy = assertedBy
		approval.DecidedAt = &now
		approval.Rationale = req.Rationale

		reason := req.Rationale
		if reason == "" {
			reason = "approval_denied"
		}
		errPayload, _ := json.Marshal(map[string]string{"reason": reason})
		return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			step, err := tx.GetStep(ctx, approval.StepID)
			if err != nil {
				return err
			}
			if err := requireAwaitingApproval(step); err != nil {
				return err
			}
			evts := []store.EventRecord{
				{Type: domain.EventApprovalDenied, Payload: payload.Approval(approval.ID, approval.StepID, approval.ExecutionID, domain.ApprovalDenied, decidedBy, assertedBy, req.Rationale)},
				{Type: domain.EventStepDenied, Payload: payload.StepDenied(approval.StepID, step.Kind, step.Target, "", errPayload)},
			}
			if _, err := tx.AppendBatch(ctx, approval.ExecutionID, evts); err != nil {
				return err
			}
			if err := tx.UpdateApproval(ctx, approval); err != nil {
				return err
			}
			step.Status = domain.StepDenied
			step.Error = errPayload
			step.CompletedAt = &now
			if err := tx.Upsert(ctx, step); err != nil {
				return err
			}
			// Resume rather than fail: a refusal is something the handler is told,
			// like any other denied step, so it can report or route around it.
			return k.resumeIfIdleTx(ctx, tx, approval.ExecutionID, now)
		})
	}); err != nil {
		return err
	}
	k.d.Observer.RecordApprovalOutcome("denied")
	return nil
}

func (k *Kernel) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	approvals, err := k.d.Approvals.ListPendingApprovals(ctx)
	if err != nil {
		return nil, err
	}
	readable, all := k.cfg.Access.ReadableAgents(ctx)
	if all {
		return approvals, nil
	}
	visible := approvals[:0]
	for _, a := range approvals {
		exec, err := k.d.Executions.GetExecution(ctx, a.ExecutionID)
		if err != nil {
			return nil, err
		}
		if slices.Contains(readable, exec.AgentID) {
			visible = append(visible, a)
		}
	}
	return visible, nil
}

func (k *Kernel) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	approval, err := k.d.Approvals.GetApproval(ctx, id)
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := k.authorizedExecution(ctx, k.d.Executions, approval.ExecutionID, auth.RoleRead); err != nil {
		return domain.Approval{}, err
	}
	return approval, nil
}
