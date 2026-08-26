package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/usage"
)

type SubmitStepRequest struct {
	Kind        domain.StepKind `json:"kind"`
	Target      string          `json:"target"`
	Args        json.RawMessage `json:"args"`
	Idempotency string          `json:"idempotency,omitempty"`
	DispatchID  uuid.UUID       `json:"-"`
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
	if req.DispatchID == uuid.Nil {
		return domain.StepDecision{}, fmt.Errorf("%w: missing dispatch_id", domain.ErrValidation)
	}

	_ = k.d.Queue.TouchDispatch(ctx, execID, time.Now().UTC())

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

	// Fail closed on a dispatch that isn't this execution's: it would silently
	// start a fresh occurrence namespace, and every replayed effect would execute
	// for real a second time.
	dispatch, err := k.d.Queue.GetDispatch(ctx, req.DispatchID)
	if err != nil {
		if err == domain.ErrNotFound {
			return domain.StepDecision{}, fmt.Errorf("%w: unknown dispatch_id", domain.ErrValidation)
		}
		return domain.StepDecision{}, err
	}
	if dispatch.ExecutionID != execID {
		return domain.StepDecision{}, fmt.Errorf("%w: dispatch_id belongs to another execution", domain.ErrValidation)
	}

	argsHash, err := identity.ComputeArgsHash(req.Args)
	if err != nil {
		return domain.StepDecision{}, fmt.Errorf("%w: invalid args: %v", domain.ErrValidation, err)
	}

	occurrence, err := k.d.Steps.DispatchOccurrence(ctx, req.DispatchID, req.Kind, req.Target, argsHash)
	if err != nil {
		return domain.StepDecision{}, err
	}
	stepID := identity.ComputeStepID(execID, req.Kind, req.Target, argsHash, occurrence)

	dec, recorded, err := k.decideStep(ctx, exec, stepID, req, argsHash, occurrence)
	if err != nil {
		return domain.StepDecision{}, err
	}

	if recorded {
		if err := k.d.Steps.AdvanceDispatchOccurrence(ctx, req.DispatchID, req.Kind, req.Target, argsHash, occurrence); err != nil {
			return domain.StepDecision{}, err
		}
		dec.StepID = stepID
	}
	return dec, nil
}

func (k *Kernel) decideStep(
	ctx context.Context, exec domain.Execution, stepID string,
	req SubmitStepRequest, argsHash string, occurrence int,
) (domain.StepDecision, bool, error) {
	existing, err := k.d.Steps.GetStep(ctx, stepID)
	if err == nil {
		k.d.Observer.RecordReplay(true)
		dec, err := k.handleExistingStep(ctx, existing, req.Idempotency)
		return dec, err == nil, err
	}
	if err != domain.ErrNotFound {
		return domain.StepDecision{}, false, err
	}

	k.d.Observer.RecordStepSubmitted(string(req.Kind))
	k.d.Observer.RecordReplay(false)

	if exec.Status == domain.ExecutionBlocked {
		return domain.StepDecision{Decision: "execution_blocked"}, false, nil
	}

	execID := exec.ID
	if req.Idempotency == "at_most_once" {
		retry, err := k.indeterminateRetry(ctx, execID, req, argsHash)
		if err != nil {
			return domain.StepDecision{}, false, err
		}
		if retry {
			return k.recordStepDecision(ctx, execID, exec.AgentID, stepID, req, argsHash, occurrence,
				domain.PolicyResult{
					Decision: domain.DecisionDeny,
					Reason:   "prior attempt outcome unknown; retry refused",
					RuleID:   "__indeterminate_retry",
				})
		}
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
		return domain.StepDecision{}, false, err
	}
	k.d.Observer.RecordPolicyDecision(polResult.Decision)
	return k.recordStepDecision(ctx, execID, exec.AgentID, stepID, req, argsHash, occurrence, polResult)
}

// indeterminateRetry reports whether this effect already resolved indeterminate
// in this execution: an at_most_once step the kernel found mid-flight on a
// re-dispatch and failed rather than re-run.
func (k *Kernel) indeterminateRetry(ctx context.Context, execID uuid.UUID, req SubmitStepRequest, argsHash string) (bool, error) {
	steps, err := k.d.Steps.ListByExecution(ctx, execID)
	if err != nil {
		return false, err
	}
	for _, s := range steps {
		if s.Status == domain.StepFailed && s.Kind == req.Kind && s.Target == req.Target &&
			s.ArgsHash == argsHash && stepErrorReason(s.Error) == domain.ReasonIndeterminate {
			return true, nil
		}
	}
	return false, nil
}

func stepErrorReason(errPayload json.RawMessage) string {
	var recorded struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(errPayload, &recorded); err != nil {
		return ""
	}
	return recorded.Reason
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
		// before execution). Safe to run now, even for at_most_once. The body
		// runs once this decision returns, so the step advances to executing.
		now := time.Now().UTC()
		step.Status = domain.StepExecuting
		step.StartedAt = &now
		evts := []store.EventRecord{
			{Type: domain.EventStepExecuting, Payload: projector.StepPayload(step.StepID, step.Kind, step.Target, "")},
		}
		if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
			return domain.StepDecision{}, err
		}
		return domain.StepDecision{Decision: "proceed"}, nil
	case domain.StepExecuting:
		if idempotency == "at_most_once" {
			errPayload, _ := json.Marshal(map[string]string{
				"reason":  domain.ReasonIndeterminate,
				"message": "outcome unknown: this may already have taken effect. Check the state before continuing.",
			})
			if err := k.failStepInternal(ctx, step, errPayload); err != nil {
				return domain.StepDecision{}, err
			}
			return domain.StepDecision{Decision: "replay", Error: errPayload}, nil
		}
		return domain.StepDecision{Decision: "proceed"}, nil
	case domain.StepDenied:
		// A resumed handler re-proposing a refused effect is told why it was
		// refused, so it can distinguish a policy rule from a human decision.
		reason := stepErrorReason(step.Error)
		if reason == "" {
			reason = domain.ReasonPolicyDenied
		}
		return domain.StepDecision{Decision: "denied", Reason: reason}, nil
	default:
		return domain.StepDecision{Decision: "proceed"}, nil
	}
}

// refuseRateLimited appends the refusal to the event log and returns the
// decision. No step row is written.
func (k *Kernel) refuseRateLimited(
	ctx context.Context, execID uuid.UUID, stepID string,
	req SubmitStepRequest, ruleID, reason string,
) (domain.StepDecision, bool, error) {
	errPayload, _ := json.Marshal(map[string]string{"reason": reason, "rule_id": ruleID})
	if _, err := k.d.Events.Append(ctx, execID, domain.EventStepRateLimited,
		projector.StepDeniedPayload(stepID, req.Kind, req.Target, ruleID, errPayload)); err != nil {
		return domain.StepDecision{}, false, err
	}
	return domain.StepDecision{Decision: "rate_limited", Reason: reason}, false, nil
}

// onRateLimited parks the step when the rule allows a wait and the execution
// has not parked before, and refuses it otherwise.
func (k *Kernel) onRateLimited(
	ctx context.Context, execID uuid.UUID, stepID string,
	req SubmitStepRequest, pol domain.PolicyResult, wait time.Duration,
) (domain.StepDecision, bool, error) {
	cfg := pol.RateLimit
	if cfg.MaxWait <= 0 {
		return k.refuseRateLimited(ctx, execID, stepID, req, pol.RuleID, domain.ReasonRateLimited)
	}
	if wait <= 0 {
		// The Postgres limiter reports no balance on a denial, so fall back to
		// the interval at which the bucket refills one token.
		wait = cfg.Window / time.Duration(cfg.MaxCalls)
	}
	parked, err := k.d.Events.CountByType(ctx, execID, domain.EventStepRateLimited)
	if err != nil {
		k.log.Warn("rate limit park history unavailable, refusing step",
			"rule_id", pol.RuleID, "execution_id", execID.String(), "error", err)
	}
	if err != nil || parked > 0 || wait > cfg.MaxWait {
		return k.refuseRateLimited(ctx, execID, stepID, req, pol.RuleID, domain.ReasonRateLimited)
	}
	// Jitter so executions on one key do not all become claimable on one tick.
	wait = min(wait+rand.N(wait/2+1), cfg.MaxWait)
	return k.parkRateLimited(ctx, execID, stepID, req, pol.RuleID, wait)
}

// parkRateLimited leaves the execution running and writes no step row: the
// queued dispatch is its own resumer, and the retry reuses this step_id.
func (k *Kernel) parkRateLimited(
	ctx context.Context, execID uuid.UUID, stepID string,
	req SubmitStepRequest, ruleID string, wait time.Duration,
) (domain.StepDecision, bool, error) {
	errPayload, _ := json.Marshal(map[string]string{"reason": domain.ReasonRateLimited, "rule_id": ruleID})
	at := time.Now().UTC().Add(wait)
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if _, err := tx.Append(ctx, execID, domain.EventStepRateLimited,
			projector.StepDeniedPayload(stepID, req.Kind, req.Target, ruleID, errPayload)); err != nil {
			return err
		}
		if err := releaseDispatchesLocked(ctx, tx, execID); err != nil {
			return err
		}
		return k.enqueueDispatchTx(ctx, tx, execID, at)
	}); err != nil {
		return domain.StepDecision{}, false, err
	}
	k.log.Info("step parked on rate limit",
		"rule_id", ruleID, "execution_id", execID.String(), "wait", wait)
	return domain.StepDecision{Decision: "blocked", Reason: domain.ReasonRateLimited}, false, nil
}

func (k *Kernel) recordStepDecision(ctx context.Context, execID uuid.UUID, agentID, stepID string, req SubmitStepRequest, argsHash string, occurrence int, pol domain.PolicyResult) (domain.StepDecision, bool, error) {
	if pol.RateLimit.MaxCalls > 0 {
		key := ratelimit.ScopeKey(pol.RuleID, pol.RateLimit.PerWhat, execID.String(), agentID)
		allowed, wait, err := k.d.RateLimiter.Allow(ctx, key, pol.RateLimit)
		if err != nil {
			if pol.RateLimit.OnLimiterError == domain.LimiterErrorDeny {
				k.d.Observer.RecordRateLimit("error_denied")
				return k.refuseRateLimited(ctx, execID, stepID, req, pol.RuleID, domain.ReasonRateLimiterUnavailable)
			}
			// Fail open
			k.log.Warn("rate limiter error, failing open",
				"rule_id", pol.RuleID, "execution_id", execID.String(), "error", err)
			k.d.Observer.RecordRateLimit("error_allowed")
			allowed = true
		}
		if !allowed {
			k.d.Observer.RecordRateLimit("limited")
			return k.onRateLimited(ctx, execID, stepID, req, pol, wait)
		}
	}

	if pol.Budget.MaxTokens > 0 && pol.Decision == domain.DecisionAllow {
		spent, err := k.d.Steps.ExecutionUsage(ctx, execID)
		switch {
		case err != nil:
			k.log.Warn("execution usage unavailable, allowing step",
				"rule_id", pol.RuleID, "execution_id", execID.String(), "error", err)
		case spent >= pol.Budget.MaxTokens:
			pol.Decision = domain.DecisionDeny
			if pol.Budget.OnExceed == domain.DecisionRequireApproval {
				pol.Decision = domain.DecisionRequireApproval
			}
			pol.Reason = domain.ReasonBudgetExceeded
			k.log.Info("execution token budget exceeded",
				"rule_id", pol.RuleID, "execution_id", execID.String(),
				"spent", spent, "max_tokens", pol.Budget.MaxTokens, "decision", pol.Decision)
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
			return domain.StepDecision{}, false, err
		}
		return domain.StepDecision{Decision: "proceed"}, true, nil

	case domain.DecisionDeny:
		step.Status = domain.StepDenied
		step.CompletedAt = &now
		reason := pol.Reason
		if reason == "" {
			reason = domain.ReasonPolicyDenied
		}
		errPayload, _ := json.Marshal(map[string]string{"reason": reason, "rule_id": pol.RuleID})
		step.Error = errPayload
		evts = append(evts,
			store.EventRecord{Type: domain.EventStepDenied, Payload: projector.StepDeniedPayload(stepID, req.Kind, req.Target, pol.RuleID, errPayload)},
		)
		if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
			return domain.StepDecision{}, false, err
		}
		return domain.StepDecision{Decision: "denied", Reason: reason}, true, nil

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
			if err := tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionBlocked, nil, ""); err != nil {
				return err
			}

			return releaseDispatchesLocked(ctx, tx, execID)
		}); err != nil {
			return domain.StepDecision{}, false, err
		}
		return domain.StepDecision{Decision: "blocked", ApprovalID: &approvalID}, true, nil
	}

	return domain.StepDecision{}, false, fmt.Errorf("unknown policy decision: %s", pol.Decision)
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
	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	release, err := k.d.Locker.Acquire(ctx, lockKey(step.ExecutionID))
	if err != nil {
		return domain.StepDecision{}, err
	}
	defer release()

	// Re-fetch under the lock to avoid a TOCTOU with CancelExecution.
	step, err = k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	exec, err := k.d.Executions.GetExecution(ctx, step.ExecutionID)
	if err != nil {
		return domain.StepDecision{}, err
	}

	if exec.Status.IsTerminal() {
		return domain.StepDecision{Decision: "execution_terminal"}, nil
	}
	if step.Status.IsTerminal() {
		return domain.StepDecision{Decision: "replay", Result: step.Result}, nil
	}
	now := time.Now().UTC()
	step.Status = domain.StepSucceeded
	step.Result = req.Result
	step.CompletedAt = &now

	var tokens usage.Tokens
	if step.Kind == domain.StepKindLLM {
		tokens = usage.Parse(req.Result)
		if !tokens.Found() {
			k.d.Observer.RecordUsageMissing()
		}
		step.UsageInput = tokens.Input
		step.UsageOutput = tokens.Output
	}

	evts := []store.EventRecord{
		{Type: domain.EventStepSucceeded, Payload: projector.StepResultPayload(stepID, step.Kind, step.Target, tokens)},
	}
	if err := k.writeStepAndEvents(ctx, step, evts); err != nil {
		return domain.StepDecision{}, err
	}
	return domain.StepDecision{Decision: "recorded"}, nil
}

func (k *Kernel) FailStep(ctx context.Context, stepID string, req FailStepRequest) (domain.StepDecision, error) {
	step, err := k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	release, err := k.d.Locker.Acquire(ctx, lockKey(step.ExecutionID))
	if err != nil {
		return domain.StepDecision{}, err
	}
	defer release()

	// Re-fetch under the lock to avoid a TOCTOU with CancelExecution.
	step, err = k.d.Steps.GetStep(ctx, stepID)
	if err != nil {
		return domain.StepDecision{}, err
	}
	exec, err := k.d.Executions.GetExecution(ctx, step.ExecutionID)
	if err != nil {
		return domain.StepDecision{}, err
	}

	if exec.Status.IsTerminal() {
		return domain.StepDecision{Decision: "execution_terminal"}, nil
	}
	if step.Status.IsTerminal() {
		return domain.StepDecision{Decision: "replay", Error: step.Error}, nil
	}
	now := time.Now().UTC()
	step.Status = domain.StepFailed
	step.Error = req.Error
	step.CompletedAt = &now
	evts := []store.EventRecord{
		{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(stepID, step.Kind, step.Target, req.Error)},
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
		{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(step.StepID, step.Kind, step.Target, errPayload)},
	}
	return k.writeStepAndEvents(ctx, step, evts)
}

func (k *Kernel) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return k.d.Steps.GetStep(ctx, stepID)
}

func (k *Kernel) ListSteps(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	return k.d.Steps.ListByExecution(ctx, execID)
}
