package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/identity"
	"github.com/rebuno/kernel/internal/projector"
	"github.com/rebuno/kernel/internal/ratelimit"
	"github.com/rebuno/kernel/internal/store"
)

type SubmitStepRequest struct {
	Kind        domain.StepKind `json:"kind"`
	Target      string          `json:"target"`
	Args        json.RawMessage `json:"args"`
	Idempotency string          `json:"idempotency,omitempty"`
	// StepID is the agent-supplied deterministic step identity. The kernel
	// recomputes the ID to validate consistency; if the step already exists,
	// it is replayed directly without re-counting.
	StepID string `json:"-"`
}

type CompleteStepRequest struct {
	Result json.RawMessage `json:"result"`
}

type FailStepRequest struct {
	Error json.RawMessage `json:"error"`
}

func (k *Kernel) SubmitStep(ctx context.Context, execID uuid.UUID, req SubmitStepRequest) (domain.StepDecision, error) {
	if req.Idempotency == "" {
		req.Idempotency = "safe_to_retry"
	}
	if req.StepID == "" {
		return domain.StepDecision{}, fmt.Errorf("%w: missing step_id", domain.ErrValidation)
	}

	release, err := k.d.Locker.Acquire(ctx, lockKey(execID))
	if err != nil {
		return domain.StepDecision{}, err
	}
	defer release()

	exec, err := k.d.Executions.GetExecution(ctx, execID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	if exec.Status.IsTerminal() {
		return domain.StepDecision{Decision: "execution_terminal"}, nil
	}

	argsHash, err := identity.ComputeArgsHash(req.Args)
	if err != nil {
		return domain.StepDecision{}, fmt.Errorf("%w: invalid args: %v", domain.ErrValidation, err)
	}

	existing, err := k.d.Steps.GetStep(ctx, req.StepID)
	if err == nil {
		k.d.Observer.RecordReplay(true)
		return k.handleExistingStep(ctx, existing, req.Idempotency)
	}
	if err != domain.ErrNotFound {
		return domain.StepDecision{}, err
	}

	k.d.Observer.RecordStepSubmitted(string(req.Kind))
	k.d.Observer.RecordReplay(false)

	if exec.Status == domain.ExecutionBlocked {
		return domain.StepDecision{Decision: "execution_blocked"}, nil
	}

	// Kernel-authoritative occurrence: count matching prior steps under the lock.
	occurrence, err := k.d.Steps.CountOccurrence(ctx, execID, req.Kind, req.Target, argsHash)
	if err != nil {
		return domain.StepDecision{}, err
	}
	computedID := identity.ComputeStepID(execID, req.Kind, req.Target, argsHash, occurrence)
	if computedID != req.StepID {
		return domain.StepDecision{}, fmt.Errorf("%w: sdk=%s kernel=%s", domain.ErrStepIDMismatch, req.StepID, computedID)
	}

	input := domain.PolicyInput{
		AgentID:  exec.AgentID,
		Target:   req.Target,
		Args:     req.Args,
		StepKind: req.Kind,
	}
	start := time.Now()
	polResult, err := k.d.Policy.Evaluate(ctx, input)
	k.d.Observer.RecordPolicyLatency(time.Since(start))
	if err != nil {
		return domain.StepDecision{}, err
	}
	return k.recordStepDecision(ctx, execID, exec.AgentID, req.StepID, req, argsHash, occurrence, polResult)
}

func (k *Kernel) handleExistingStep(ctx context.Context, step domain.Step, idempotency string) (domain.StepDecision, error) {
	switch step.Status {
	case domain.StepSucceeded:
		return domain.StepDecision{Decision: "replay", Result: step.Result}, nil
	case domain.StepFailed:
		return domain.StepDecision{Decision: "replay", Error: step.Error}, nil
	case domain.StepAwaitingApproval:
		approvals, err := k.d.Approvals.ListPendingApprovals(ctx)
		if err != nil {
			return domain.StepDecision{}, err
		}
		for _, a := range approvals {
			if a.StepID == step.StepID {
				return domain.StepDecision{Decision: "blocked", ApprovalID: &a.ID}, nil
			}
		}
		return domain.StepDecision{Decision: "blocked"}, nil
	case domain.StepAllowed, domain.StepProposed:
		// Recorded but the body never ran (approved-and-resumed, or a crash
		// before execution). Safe to run now, even for at_most_once.
		return domain.StepDecision{Decision: "proceed"}, nil
	case domain.StepExecuting:
		if idempotency == "at_most_once" {
			errPayload, _ := json.Marshal(map[string]string{"reason": "indeterminate"})
			if err := k.failStepInternal(ctx, step, errPayload); err != nil {
				return domain.StepDecision{}, err
			}
			return domain.StepDecision{Decision: "replay", Error: errPayload}, nil
		}
		return domain.StepDecision{Decision: "proceed"}, nil
	case domain.StepDenied:
		return domain.StepDecision{Decision: "denied", Reason: "policy_denied"}, nil
	default:
		return domain.StepDecision{Decision: "proceed"}, nil
	}
}

func (k *Kernel) recordStepDecision(ctx context.Context, execID uuid.UUID, agentID, stepID string, req SubmitStepRequest, argsHash string, occurrence int, pol domain.PolicyResult) (domain.StepDecision, error) {
	if pol.RateLimit.MaxCalls > 0 {
		key := ratelimit.ScopeKey(pol.RuleID, pol.RateLimit.PerWhat, execID.String(), agentID)
		allowed, _, err := k.d.RateLimiter.Allow(ctx, key, pol.RateLimit)
		if err != nil {
			if pol.RateLimit.OnLimiterError == domain.LimiterErrorDeny {
				k.d.Observer.RecordRateLimit("error_denied")
				return domain.StepDecision{Decision: "rate_limited", Reason: "rate_limiter_unavailable"}, nil
			}
			// Fail open
			k.log.Warn("rate limiter error, failing open",
				"rule_id", pol.RuleID, "execution_id", execID.String(), "error", err)
			k.d.Observer.RecordRateLimit("error_allowed")
			allowed = true
		}
		if !allowed {
			k.d.Observer.RecordRateLimit("limited")
			return domain.StepDecision{Decision: "rate_limited", Reason: "rate_limit_exceeded"}, nil
		}
	}

	now := time.Now().UTC()
	step := domain.Step{
		StepID:      stepID,
		ExecutionID: execID,
		Kind:        req.Kind,
		Target:      req.Target,
		ArgsHash:    argsHash,
		Occurrence:  occurrence,
		Status:      domain.StepProposed,
		Idempotency: req.Idempotency,
		Args:        req.Args,
	}

	evts := []store.EventRecord{
		{Type: domain.EventStepProposed, Payload: projector.StepPayload(stepID, req.Kind, req.Target, "")},
	}

	switch pol.Decision {
	case domain.DecisionAllow:
		step.Status = domain.StepExecuting
		step.StartedAt = &now
		evts = append(evts,
			store.EventRecord{Type: domain.EventStepAllowed, Payload: projector.StepPayload(stepID, req.Kind, req.Target, pol.RuleID)},
			store.EventRecord{Type: domain.EventStepExecuting, Payload: projector.StepPayload(stepID, req.Kind, req.Target, "")},
		)
		if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
			return domain.StepDecision{}, err
		}
		return domain.StepDecision{Decision: "proceed"}, nil

	case domain.DecisionDeny:
		step.Status = domain.StepFailed
		step.CompletedAt = &now
		errPayload, _ := json.Marshal(map[string]string{"reason": "policy_denied", "rule_id": pol.RuleID})
		step.Error = errPayload
		evts = append(evts,
			store.EventRecord{Type: domain.EventStepDenied, Payload: projector.StepPayload(stepID, req.Kind, req.Target, pol.RuleID)},
			store.EventRecord{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(stepID, req.Kind, errPayload)},
		)
		if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
			return domain.StepDecision{}, err
		}
		return domain.StepDecision{Decision: "denied", Reason: pol.Reason}, nil

	case domain.DecisionRequireApproval:
		approvalID := uuid.Must(uuid.NewV7())
		approversJSON, _ := json.Marshal(pol.ApprovalConfig.Approvers)
		timeout := pol.ApprovalConfig.Timeout
		if timeout == 0 {
			timeout = k.cfg.DefaultApprovalTimeout
		}
		timeoutAt := now.Add(timeout)
		approval := domain.Approval{
			ID:          approvalID,
			StepID:      stepID,
			ExecutionID: execID,
			Status:      domain.ApprovalPending,
			Approvers:   approversJSON,
			Message:     pol.ApprovalConfig.Message,
			TimeoutAt:   timeoutAt,
			CreatedAt:   now,
		}
		step.Status = domain.StepAwaitingApproval
		evts = append(evts,
			store.EventRecord{Type: domain.EventStepAwaitingApproval, Payload: projector.StepPayload(stepID, req.Kind, req.Target, pol.RuleID)},
			store.EventRecord{Type: domain.EventApprovalRequested, Payload: projector.ApprovalPayload(approvalID, stepID, execID, domain.ApprovalPending, "", "")},
		)
		blockPayload := projector.ExecutionPayload(execID, domain.ExecutionBlocked, nil, "")
		evts = append(evts, store.EventRecord{Type: domain.EventExecutionBlocked, Payload: blockPayload})

		if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
			if _, err := tx.AppendBatch(ctx, execID, evts); err != nil {
				return err
			}
			if err := tx.Upsert(ctx, step); err != nil {
				return err
			}
			if err := tx.CreateApproval(ctx, approval); err != nil {
				return err
			}
			return tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionBlocked, nil, "")
		}); err != nil {
			return domain.StepDecision{}, err
		}
		return domain.StepDecision{Decision: "blocked", ApprovalID: &approvalID}, nil
	}

	return domain.StepDecision{}, fmt.Errorf("unknown policy decision: %s", pol.Decision)
}

func (k *Kernel) writeStepAndEvents(ctx context.Context, step domain.Step, evts []store.EventRecord) error {
	return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if _, err := tx.AppendBatch(ctx, step.ExecutionID, evts); err != nil {
			return err
		}
		return tx.Upsert(ctx, step)
	})
}

func (k *Kernel) CompleteStep(ctx context.Context, stepID string, req CompleteStepRequest) (domain.StepDecision, error) {
	release, err := k.d.Locker.Acquire(ctx, lockKeyFromStepID(stepID))
	if err != nil {
		return domain.StepDecision{}, err
	}
	defer release()

	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	if step.Status.IsTerminal() {
		return domain.StepDecision{Decision: "replay", Result: step.Result}, nil
	}
	now := time.Now().UTC()
	step.Status = domain.StepSucceeded
	step.Result = req.Result
	step.CompletedAt = &now
	evts := []store.EventRecord{
		{Type: domain.EventStepSucceeded, Payload: projector.StepResultPayload(stepID, step.Kind)},
	}
	if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
		return domain.StepDecision{}, err
	}
	return domain.StepDecision{Decision: "recorded"}, nil
}

func (k *Kernel) FailStep(ctx context.Context, stepID string, req FailStepRequest) (domain.StepDecision, error) {
	release, err := k.d.Locker.Acquire(ctx, lockKeyFromStepID(stepID))
	if err != nil {
		return domain.StepDecision{}, err
	}
	defer release()

	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	if step.Status.IsTerminal() {
		return domain.StepDecision{Decision: "replay", Error: step.Error}, nil
	}
	now := time.Now().UTC()
	step.Status = domain.StepFailed
	step.Error = req.Error
	step.CompletedAt = &now
	evts := []store.EventRecord{
		{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(stepID, step.Kind, req.Error)},
	}
	if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
		return domain.StepDecision{}, err
	}
	return domain.StepDecision{Decision: "recorded"}, nil
}

func (k *Kernel) failStepInternal(ctx context.Context, step domain.Step, errPayload []byte) error {
	now := time.Now().UTC()
	step.Status = domain.StepFailed
	step.Error = errPayload
	step.CompletedAt = &now
	evts := []store.EventRecord{
		{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(step.StepID, step.Kind, errPayload)},
	}
	return k.writeStepAndEvents(ctx, step, evts)
}

func (k *Kernel) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return k.d.Steps.GetStep(ctx, stepID)
}

func (k *Kernel) ListSteps(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	return k.d.Steps.ListByExecution(ctx, execID)
}

func lockKeyFromStepID(stepID string) string {
	return "step:" + stepID
}
