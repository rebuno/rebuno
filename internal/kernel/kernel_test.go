package kernel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func memDeps(ms *memstore.Store, d kernel.Deps) kernel.Deps {
	if d.Events == nil {
		d.Events = ms
	}
	if d.Steps == nil {
		d.Steps = ms
	}
	if d.Executions == nil {
		d.Executions = ms
	}
	if d.Agents == nil {
		d.Agents = ms
	}
	if d.Approvals == nil {
		d.Approvals = ms
	}
	if d.Queue == nil {
		d.Queue = ms
	}
	if d.Locker == nil {
		d.Locker = ms
	}
	if d.UnitOfWork == nil {
		d.UnitOfWork = ms
	}
	return d
}

func setup(t *testing.T) (*kernel.Kernel, context.Context) {
	t.Helper()
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DispatchBaseDelay: 1 * time.Millisecond}
	k := kernel.New(cfg, kernel.Deps{
		Events:     ms,
		Steps:      ms,
		Executions: ms,
		Agents:     ms,
		Approvals:  ms,
		Queue:      ms,
		Locker:     ms,
		UnitOfWork: ms,
		Policy:     policy.PermissiveEngine{},
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	return k, ctx
}

// leaseOf returns the lease for the execution's newest dispatch, claiming it
// first when the test never ran the dispatcher, since an agent only holds a
// lease for an attempt that was delivered. The dispatch also scopes occurrence
// counting, so a test that wants a fresh replay generation asks for a new
// dispatch rather than resetting anything itself.
func leaseOf(t *testing.T, k *kernel.Kernel, execID uuid.UUID) domain.Lease {
	t.Helper()
	ctx := context.Background()
	d := newestDispatch(t, k, execID)
	if d.Status != domain.DispatchInFlight {
		// A resume dispatch parked by a rate limit is only due later, so claim
		// as of its due time.
		at := time.Now().UTC()
		if d.NextAttemptAt.After(at) {
			at = d.NextAttemptAt
		}
		if _, err := k.Deps().Queue.Claim(ctx, "test", 1000, at); err != nil {
			t.Fatalf("claim dispatches: %v", err)
		}
		d = newestDispatch(t, k, execID)
	}
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("execution %s has no deliverable dispatch (status %s)", execID, d.Status)
	}
	return domain.Lease{DispatchID: d.ID, Attempt: d.Attempt}
}

func newestDispatch(t *testing.T, k *kernel.Kernel, execID uuid.UUID) domain.Dispatch {
	t.Helper()
	ds, err := k.Deps().Queue.ListDispatchesByExecution(context.Background(), execID)
	if err != nil {
		t.Fatalf("list dispatches: %v", err)
	}
	if len(ds) == 0 {
		t.Fatal("execution has no dispatch")
	}
	return ds[len(ds)-1]
}

func TestCreateExecution(t *testing.T) {
	k, ctx := setup(t)
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != domain.ExecutionRunning {
		t.Fatalf("expected running, got %s", exec.Status)
	}
	got, err := k.GetExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "agent-1" {
		t.Fatal("agent id mismatch")
	}
	events, err := k.GetEvents(ctx, exec.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 { // created, started, dispatch.sent
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestSubmitAndReplayToolStep(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	argsHash := mustHash(args)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", argsHash, 0)

	// First submit -> proceed
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}

	result := json.RawMessage(`{"ok":true}`)
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: result, Lease: leaseOf(t, k, exec.ID)}); err != nil {
		t.Fatal(err)
	}

	// Re-dispatch, then replay: the occurrence counter restarts with the new
	// dispatch, so the same call recomputes the same step_id and short-circuits.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	dec2, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Decision != "replay" {
		t.Fatalf("expected replay, got %s", dec2.Decision)
	}
	if string(dec2.Result) != string(result) {
		t.Fatal("replay result mismatch")
	}
}

// A dispatch id from a different execution must be rejected rather than silently
// opening a fresh occurrence namespace — that would make every already-recorded
// effect miss its replay and run for real a second time.
func TestSubmitRejectsForeignDispatch(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	other, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	args := json.RawMessage(`{"path":"/tmp"}`)

	_, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, other.ID),
	})
	if !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("expected superseded lease for foreign dispatch, got %v", err)
	}

	_, err = k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, Lease: domain.Lease{DispatchID: uuid.New(), Attempt: 1},
	})
	if !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("expected superseded lease for unknown dispatch, got %v", err)
	}

	_, err = k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args,
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected validation error for missing dispatch, got %v", err)
	}
}

// Occurrence is scoped to the dispatch, so a re-dispatch walks the same step IDs
// and replays, while identical calls *within* one dispatch get distinct steps.
func TestOccurrenceIsScopedToDispatch(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	args := json.RawMessage(`{"path":"/tmp"}`)
	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)}

	first, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CompleteStep(ctx, first.StepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{"n":1}`), Lease: leaseOf(t, k, exec.ID)}); err != nil {
		t.Fatal(err)
	}

	// Same dispatch, identical args: a genuinely new call, not a replay.
	second, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision != "proceed" {
		t.Fatalf("second identical call in a dispatch: want proceed, got %s", second.Decision)
	}
	if second.StepID == first.StepID {
		t.Fatal("identical calls in one dispatch must get distinct step ids")
	}

	// New dispatch: the counter restarts, so the first call replays.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	req.Lease = leaseOf(t, k, exec.ID)
	resumed, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision != "replay" {
		t.Fatalf("resumed dispatch: want replay, got %s", resumed.Decision)
	}
	if resumed.StepID != first.StepID {
		t.Fatal("resumed dispatch must recompute the original step id")
	}
	if string(resumed.Result) != `{"n":1}` {
		t.Fatalf("replayed the wrong result: %s", resumed.Result)
	}
}

func TestPolicyDeny(t *testing.T) {
	k, ctx := setup(t)
	d := k.Deps()
	k2 := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		Events:     d.Events,
		Steps:      d.Steps,
		Executions: d.Executions,
		Agents:     d.Agents,
		Approvals:  d.Approvals,
		Queue:      d.Queue,
		Locker:     d.Locker,
		UnitOfWork: d.UnitOfWork,
		Policy:     policy.DenyAllEngine{},
	})
	exec, _ := k2.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", mustHash(args), 0)
	dec, err := k2.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "denied" {
		t.Fatalf("expected denied, got %s", dec.Decision)
	}
	// A policy-denied step must emit exactly one terminal step event
	// (step.denied), never both step.denied and step.failed.
	if got := findStepType(t, k2, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindTool) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindTool)
	}
	assertSingleTerminalStepEvent(t, k2, ctx, exec.ID, domain.EventStepDenied)
	step, err := k2.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepDenied {
		t.Fatalf("expected step status %q, got %q", domain.StepDenied, step.Status)
	}
}

func TestApprovalFlow(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "approve-read",
			When: policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("expected blocked with approval id, got %+v", dec)
	}
	if got, _ := k.GetExecution(ctx, exec.ID); got.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", got.Status)
	}

	// A re-dispatch that reaches the still-pending step gets blocked again.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	dec2, _ := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if dec2.Decision != "blocked" {
		t.Fatalf("expected still blocked, got %s", dec2.Decision)
	}

	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.GetExecution(ctx, exec.ID); got.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after grant, got %s", got.Status)
	}

	// Now submit returns proceed.
	dec3, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec3.Decision != "proceed" {
		t.Fatalf("expected proceed after grant, got %s", dec3.Decision)
	}
}

// An at_most_once tool that requires approval must still run once approved.
func TestApprovalFlowAtMostOnce(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "approve-write",
			When: policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, Lease: leaseOf(t, k, exec.ID), Idempotency: "at_most_once"}

	dec, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("expected blocked with approval id, got %+v", dec)
	}
	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}

	// The grant enqueues a fresh dispatch; the resumed run reaches the approved
	// step at occurrence 0 again and is told to proceed.
	req.Lease = leaseOf(t, k, exec.ID)
	dec2, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Decision != "proceed" {
		t.Fatalf("expected proceed after grant for at_most_once, got %s (error=%s)", dec2.Decision, dec2.Error)
	}
}

func TestApprovalResumeEnqueuesDispatch(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "approve-read",
			When: policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	dec, _ := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatal("expected blocked")
	}

	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}

	// The resumed execution must immediately have a pending dispatch.
	dispatches, err := ms.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dispatches {
		if d.Status == domain.DispatchPending || d.Status == domain.DispatchInFlight {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a pending dispatch after approval grant")
	}
}

func TestLLMCallFlow(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	req := json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	argsHash, _ := identity.ComputeArgsHash(req)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindLLM, "gpt-4", argsHash, 0)

	// An llm_call goes through the same submit_step write path as a tool call.
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}
	// Re-submitting the same step_id while still executing must be accepted (no divergence).
	dec2, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Decision != "proceed" {
		t.Fatalf("expected proceed on second submit while executing, got %s", dec2.Decision)
	}

	resp := json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`)
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: resp, Lease: leaseOf(t, k, exec.ID)}); err != nil {
		t.Fatal(err)
	}
	// After completion, a re-dispatch replays the cached result.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	dec3, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec3.Decision != "replay" {
		t.Fatalf("expected replay after completion, got %s", dec3.Decision)
	}
	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected succeeded, got %s", step.Status)
	}
}

func TestTerminalRejectsFurtherSteps(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	// The agent holds a delivered lease, then the execution is cancelled under it.
	lease := leaseOf(t, k, exec.ID)
	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "execution_terminal" {
		t.Fatalf("expected terminal, got %s", dec.Decision)
	}
}

func TestRateLimitDoubleStep(t *testing.T) {
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "rate-limit-read",
			When: policy.Condition{Target: "read"},
			Then: domain.PolicyResult{
				Decision:  domain.DecisionAllow,
				RateLimit: domain.RateLimitConfig{MaxCalls: 1, Window: time.Hour, PerWhat: "execution"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), memDeps(ms, kernel.Deps{Policy: pe, RateLimiter: ratelimit.NewMemoryLimiter()}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)

	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("first step expected proceed, got %s", dec.Decision)
	}

	dec, err = k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "rate_limited" || dec.Reason != "rate_limit_exceeded" {
		t.Fatalf("second step expected rate_limited, got %+v", dec)
	}

	events, err := k.GetEvents(ctx, exec.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		RuleID string `json:"rule_id"`
		Target string `json:"target"`
		Error  struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	var found bool
	for _, ev := range events {
		if ev.Type != domain.EventStepRateLimited {
			continue
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected a %s event, got %v", domain.EventStepRateLimited, eventTypes(events))
	}
	if payload.RuleID != "rate-limit-read" || payload.Target != "read" || payload.Error.Reason != "rate_limit_exceeded" {
		t.Fatalf("unexpected %s payload: %+v", domain.EventStepRateLimited, payload)
	}

	steps, err := k.ListSteps(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected the refused effect to record no step, got %d", len(steps))
	}
}

func eventTypes(events []domain.Event) []string {
	types := make([]string, len(events))
	for i, ev := range events {
		types[i] = ev.Type
	}
	return types
}

func mustHash(args []byte) string {
	h, _ := identity.ComputeArgsHash(args)
	return h
}

// approvalLLMEngine builds a policy engine that requires approval for any
// llm_call step, so we can verify the step_type recorded in approval events
// reflects the actual step kind rather than a hardcoded tool_call.
func approvalLLMEngine(t *testing.T, timeout time.Duration) *policy.RuleEngine {
	t.Helper()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "approve-llm",
			When: policy.Condition{StepKind: string(domain.StepKindLLM)},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: timeout, Message: "approve llm"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return pe
}

func submitLLMStep(t *testing.T, k *kernel.Kernel, ctx context.Context, exec domain.Execution) (string, uuid.UUID) {
	t.Helper()
	req := json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	argsHash, _ := identity.ComputeArgsHash(req)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindLLM, "gpt-4", argsHash, 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("expected blocked with approval id, got %+v", dec)
	}
	return stepID, *dec.ApprovalID
}

// findStepType scans the execution events for one of the given event types and
// returns the recorded step_type payload field.
func findStepType(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID, wantTypes ...string) string {
	t.Helper()
	events, err := k.GetEvents(ctx, execID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool, len(wantTypes))
	for _, w := range wantTypes {
		want[w] = true
	}
	for _, ev := range events {
		if !want[ev.Type] {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal event %s payload: %v", ev.Type, err)
		}
		raw, ok := payload["step_type"]
		if !ok {
			t.Fatalf("event %s missing step_type: %v", ev.Type, payload)
		}
		s, _ := raw.(string)
		return s
	}
	t.Fatalf("no event of types %v found", wantTypes)
	return ""
}

// assertSingleTerminalStepEvent enforces the single-terminal-event invariant
// for a step: exactly one terminal step event (step.denied, step.failed,
// step.succeeded, or step.cancelled) must appear in the execution's event log,
// and it must match wantType. It fatals on zero, multiple, or mismatched
// terminal events.
func assertSingleTerminalStepEvent(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID, wantType string) {
	t.Helper()
	events, err := k.GetEvents(ctx, execID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	terminal := map[string]bool{
		domain.EventStepDenied:    true,
		domain.EventStepFailed:    true,
		domain.EventStepSucceeded: true,
		domain.EventStepCancelled: true,
	}
	var found []string
	for _, ev := range events {
		if terminal[ev.Type] {
			found = append(found, ev.Type)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one terminal step event (%s), got %d: %v", wantType, len(found), found)
	}
	if found[0] != wantType {
		t.Fatalf("expected terminal step event %q, got %q", wantType, found[0])
	}
}

func TestApprovalGrantRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.GrantApproval(ctx, approvalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepAllowed); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepAllowed step_type = %q, want %q", got, domain.StepKindLLM)
	}
}

func TestApprovalDenyRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	assertSingleTerminalStepEvent(t, k, ctx, exec.ID, domain.EventStepDenied)
}

// TestApprovalDenyResumesExecution verifies that denying an approval resumes the
// execution with the step denied, so the handler is told what happened rather
// than being killed, and that re-proposing the same effect stays denied without
// asking the approver again.
func TestApprovalDenyResumesExecution(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil {
		t.Fatal(err)
	}
	got, _ := k.GetExecution(ctx, exec.ID)
	if got.Status != domain.ExecutionRunning {
		t.Fatalf("expected execution running after deny, got %s %s", got.Status, got.FailureReason)
	}

	// The resumed handler re-proposes the effect and is told a human refused it.
	req := json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "denied" || dec.Reason != "approval_denied" {
		t.Fatalf("expected denied approval_denied on re-propose, got %+v", dec)
	}

	// Re-proposing must not ask the approver a second time.
	pending, err := ms.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending approvals after deny, got %d", len(pending))
	}
	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil && !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
}

// Denying with a rationale makes that rationale the reason a re-proposed step is given.
func TestApprovalDenyRationaleReachesHandler(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	rationale := "use the existing helper in utils.go instead"
	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob", Rationale: rationale}); err != nil {
		t.Fatal(err)
	}

	req := json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "denied" || dec.Reason != rationale {
		t.Fatalf("expected denied with rationale %q, got %+v", rationale, dec)
	}
}

func TestApprovalExpireRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: 1 * time.Millisecond}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, 1*time.Millisecond)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	submitLLMStep(t, k, ctx, exec)

	time.Sleep(10 * time.Millisecond)
	if err := k.ExpireApprovals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	assertSingleTerminalStepEvent(t, k, ctx, exec.ID, domain.EventStepDenied)
}

func TestCancelExecutionRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	submitLLMStep(t, k, ctx, exec)

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	assertSingleTerminalStepEvent(t, k, ctx, exec.ID, domain.EventStepDenied)
}

// TestCompleteStepAfterExecutionCancelled verifies that completing a step
// whose execution is already terminal does not append a step.succeeded event.
// See https://github.com/rebuno/rebuno/issues/122.
func TestCompleteStepAfterExecutionCancelled(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", mustHash(args), 0)
	lease := leaseOf(t, k, exec.ID)
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: lease}); err != nil {
		t.Fatal(err)
	}

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}

	dec, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{"ok":true}`), Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "execution_terminal" {
		t.Fatalf("expected execution_terminal, got %s", dec.Decision)
	}

	// No step.succeeded event must have been appended.
	events, err := k.GetEvents(ctx, exec.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == domain.EventStepSucceeded {
			t.Fatalf("unexpected step.succeeded event on cancelled execution: %+v", ev)
		}
	}

	// The step must not have transitioned to succeeded.
	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status == domain.StepSucceeded {
		t.Fatalf("step should not have transitioned to succeeded, got %s", step.Status)
	}
}

// TestFailStepAfterExecutionCancelled verifies that failing a step whose
// execution is already terminal does not append a step.failed event.
func TestFailStepAfterExecutionCancelled(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", mustHash(args), 0)
	lease := leaseOf(t, k, exec.ID)
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: lease}); err != nil {
		t.Fatal(err)
	}

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}

	errPayload := json.RawMessage(`{"reason":"boom"}`)
	dec, err := k.FailStep(ctx, stepID, kernel.FailStepRequest{Error: errPayload, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "execution_terminal" {
		t.Fatalf("expected execution_terminal, got %s", dec.Decision)
	}

	events, err := k.GetEvents(ctx, exec.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == domain.EventStepFailed {
			t.Fatalf("unexpected step.failed event on cancelled execution: %+v", ev)
		}
	}

	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status == domain.StepFailed {
		t.Fatalf("step should not have transitioned to failed, got %s", step.Status)
	}
}

// TestCancelExecutionCancelsInFlightSteps verifies that cancelling an execution
// closes out steps left in `executing`: they were handed to the agent and their
// outcome was never reported back, so they are terminal with an unknown result.
func TestCancelExecutionCancelsInFlightSteps(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"cmd":"sleep 60"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "bash", mustHash(args), 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "bash", Args: args, Lease: leaseOf(t, k, exec.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("submit decision = %q, want proceed", dec.Decision)
	}

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}

	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepCancelled {
		t.Fatalf("step status = %q, want %q", step.Status, domain.StepCancelled)
	}
	if step.CompletedAt == nil {
		t.Error("cancelled step has no completed_at")
	}
	assertSingleTerminalStepEvent(t, k, ctx, exec.ID, domain.EventStepCancelled)
}

// TestCancelExecutionCancelsPendingApprovals verifies that cancelling an
// execution expires its pending approvals and does not leave them orphaned.
func TestCancelExecutionCancelsPendingApprovals(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	approval, err := k.GetApproval(ctx, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ApprovalExpired {
		t.Fatalf("expected pending approval to be expired after cancel, got %s", approval.Status)
	}
}

// failingQueue wraps a memstore and fails ListDispatchesByExecution so we can
// verify CancelExecution propagates the dispatches query error instead of
// swallowing it and leaving active dispatches orphaned.
type failingQueue struct {
	*memstore.Store
	dispatchErr error
}

func (f *failingQueue) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	return nil, f.dispatchErr
}

// failingUnitOfWork delegates RunInTx to the underlying store but hands fn a
// TxStore whose dispatch listing fails.
type failingUnitOfWork struct {
	*memstore.Store
	dispatchErr error
}

func (f *failingUnitOfWork) RunInTx(ctx context.Context, fn func(store.TxStore) error) error {
	return f.Store.RunInTx(ctx, func(tx store.TxStore) error {
		return fn(&failingTxStore{TxStore: tx, dispatchErr: f.dispatchErr})
	})
}

type failingTxStore struct {
	store.TxStore
	dispatchErr error
}

func (f *failingTxStore) ListDispatchesByExecution(ctx context.Context, execID uuid.UUID) ([]domain.Dispatch, error) {
	return nil, f.dispatchErr
}

// TestCancelExecutionPropagatesDispatchError verifies that a failure while
// listing dispatches aborts the cancel instead of proceeding with an empty
// list and leaving active dispatches orphaned.
func TestCancelExecutionPropagatesDispatchError(t *testing.T) {
	ms := memstore.NewStore()
	dispatchErr := errors.New("dispatch query failed")
	uow := &failingUnitOfWork{Store: ms, dispatchErr: dispatchErr}
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: &failingQueue{Store: ms, dispatchErr: dispatchErr}, Locker: ms, UnitOfWork: uow,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	err := k.CancelExecution(ctx, exec.ID)
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("expected cancel to propagate dispatch query error, got %v", err)
	}
}

func TestApprovalConfigFromYAMLBundleReachesApproval(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	pe, err := policy.NewRuleEngineFromBundle(`
default_action: deny
rules:
  - id: approve-fs-writes
    when:
      target: fs_write
    then:
      decision: require_approval
      reason: filesystem writes need approval
      approval_config:
        approvers: ["alice", "bob"]
        timeout: 5m
        message: check the target path before granting
`)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/etc/passwd"}`)
	before := time.Now().UTC()
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: args, Lease: leaseOf(t, k, exec.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("expected blocked with approval id, got %+v", dec)
	}

	approval, err := k.GetApproval(ctx, *dec.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	var approvers []string
	if err := json.Unmarshal(approval.Approvers, &approvers); err != nil {
		t.Fatalf("approvers not stored as a list: %v", err)
	}
	if len(approvers) != 2 || approvers[0] != "alice" || approvers[1] != "bob" {
		t.Errorf("approvers: got %v, want [alice bob]", approvers)
	}
	if approval.Message != "check the target path before granting" {
		t.Errorf("message: got %q", approval.Message)
	}
	// The rule asked for 5m; the kernel default is 1h, so a dropped timeout is visible.
	if gap := approval.TimeoutAt.Sub(before); gap > 6*time.Minute {
		t.Errorf("timeout_at is %v out — the rule's 5m was dropped in favour of the default", gap)
	}
}

// blockedApproval submits a step that the given approval_config gates, and
// returns the kernel and the pending approval's id.
func blockedApproval(t *testing.T, approvalConfig string) (*kernel.Kernel, uuid.UUID) {
	t.Helper()
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngineFromBundle(`
default_action: deny
rules:
  - id: approve-fs-writes
    when:
      target: fs_write
    then:
      decision: require_approval
` + approvalConfig)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}, memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	args := json.RawMessage(`{"path":"/etc/passwd"}`)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: args, Lease: leaseOf(t, k, exec.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.ApprovalID == nil {
		t.Fatalf("expected an approval, got %+v", dec)
	}
	return k, *dec.ApprovalID
}

func TestApproversGateWhoMayDecide(t *testing.T) {
	const listed = `
      approval_config:
        approvers: ["alice", "bob"]
`
	ctx := context.Background()

	t.Run("a non-approver cannot grant", func(t *testing.T) {
		k, id := blockedApproval(t, listed)
		err := k.GrantApproval(ctx, id, kernel.GrantApprovalRequest{DecidedBy: "carol"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("grant by carol: got %v, want ErrForbidden", err)
		}
		// The rejected decision must leave no trace on the approval.
		a, _ := k.GetApproval(ctx, id)
		if a.Status != domain.ApprovalPending {
			t.Errorf("status: got %q, want pending", a.Status)
		}
		if a.DecidedBy != "" || a.DecidedAt != nil {
			t.Errorf("rejected grant still recorded decided_by=%q decided_at=%v", a.DecidedBy, a.DecidedAt)
		}
	})

	// Denying is a decision too: a non-approver must not be able to kill a step.
	t.Run("a non-approver cannot deny", func(t *testing.T) {
		k, id := blockedApproval(t, listed)
		err := k.DenyApproval(ctx, id, kernel.DenyApprovalRequest{DecidedBy: "carol"})
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("deny by carol: got %v, want ErrForbidden", err)
		}
		if a, _ := k.GetApproval(ctx, id); a.Status != domain.ApprovalPending {
			t.Errorf("status: got %q, want pending", a.Status)
		}
	})

	t.Run("a listed approver can grant", func(t *testing.T) {
		k, id := blockedApproval(t, listed)
		if err := k.GrantApproval(ctx, id, kernel.GrantApprovalRequest{DecidedBy: "bob"}); err != nil {
			t.Fatalf("grant by bob: %v", err)
		}
		a, _ := k.GetApproval(ctx, id)
		if a.Status != domain.ApprovalGranted || a.DecidedBy != "bob" {
			t.Fatalf("got status %q decided_by %q, want granted/bob", a.Status, a.DecidedBy)
		}
	})

	// The common case: no approvers listed means the rule routes to nobody in
	// particular, so anyone may decide. Enforcing here would break every
	// existing bundle that omits the field.
	t.Run("no approvers means anyone may grant", func(t *testing.T) {
		k, id := blockedApproval(t, "")
		if err := k.GrantApproval(ctx, id, kernel.GrantApprovalRequest{DecidedBy: "carol"}); err != nil {
			t.Fatalf("grant by carol on an unrestricted approval: %v", err)
		}
	})
}

// An at_most_once effect that resolved indeterminate must not run again in the
// same execution, while a different effect on the same target still proceeds.
func TestIndeterminateRetryIsDenied(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	submit := func(did domain.Lease, args string) domain.StepDecision {
		t.Helper()
		dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
			Kind:        domain.StepKindTool,
			Target:      "send_email",
			Args:        json.RawMessage(args),
			Idempotency: "at_most_once",
			Lease:       did,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dec
	}

	const emailA = `{"to":"a@example.com"}`
	if dec := submit(leaseOf(t, k, exec.ID), emailA); dec.Decision != "proceed" {
		t.Fatalf("first call must proceed, got %s", dec.Decision)
	}

	// No result is recorded. The next dispatch replays the same effect and
	// finds it mid-flight, which an at_most_once step resolves as indeterminate.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	did := leaseOf(t, k, exec.ID)
	dec := submit(did, emailA)
	if dec.Decision != "replay" || !bytes.Contains(dec.Error, []byte("indeterminate")) {
		t.Fatalf("replayed step must resolve indeterminate, got %s %s", dec.Decision, dec.Error)
	}
	if !bytes.Contains(dec.Error, []byte("outcome unknown")) {
		t.Fatalf("indeterminate error must carry an explanatory message, got %s", dec.Error)
	}

	// The agent calls the same effect again: a fresh occurrence, so a new step,
	// which the kernel refuses rather than running the side effect twice.
	dec = submit(did, emailA)
	if dec.Decision != "denied" {
		t.Fatalf("retry of an indeterminate effect must be denied, got %s", dec.Decision)
	}
	refusal := dec.Reason
	if refusal == "" {
		t.Fatal("denial must carry a reason")
	}

	if dec := submit(did, `{"to":"b@example.com"}`); dec.Decision != "proceed" {
		t.Fatalf("a different effect on the same target must proceed, got %s", dec.Decision)
	}

	// A later dispatch replays both calls and is told why the second was
	// refused, not just that some policy denied it.
	if err := k.EnqueueReDrive(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	did = leaseOf(t, k, exec.ID)
	submit(did, emailA)
	if dec := submit(did, emailA); dec.Reason != refusal {
		t.Fatalf("replayed denial reason = %q, want %q", dec.Reason, refusal)
	}
}

func TestDenyReasonMatchesOnReplay(t *testing.T) {
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "deny-read",
			When: policy.Condition{Target: "read"},
			Then: domain.PolicyResult{Decision: domain.DecisionDeny},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`)}
	req.Lease = leaseOf(t, k, exec.ID)
	first, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != "denied" || first.Reason != domain.ReasonPolicyDenied {
		t.Fatalf("first submission: got %+v, want denied/%s", first, domain.ReasonPolicyDenied)
	}

	replay, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Reason != first.Reason {
		t.Fatalf("replay reason %q != first reason %q", replay.Reason, first.Reason)
	}
}

func rateLimitedKernel(t *testing.T, cfg domain.RateLimitConfig) (*kernel.Kernel, context.Context, domain.Execution) {
	t.Helper()
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "rate-limit-read",
			When: policy.Condition{Target: "read"},
			Then: domain.PolicyResult{Decision: domain.DecisionAllow, RateLimit: cfg},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), memDeps(ms, kernel.Deps{Policy: pe, RateLimiter: ratelimit.NewMemoryLimiter()}))
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return k, ctx, exec
}

func submitRead(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID) domain.StepDecision {
	t.Helper()
	dec, err := k.SubmitStep(ctx, execID, kernel.SubmitStepRequest{
		Kind:   domain.StepKindTool,
		Target: "read",
		Args:   json.RawMessage(`{"path":"/tmp"}`),
		Lease:  leaseOf(t, k, execID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestRateLimitParksForRedispatch(t *testing.T) {
	k, ctx, exec := rateLimitedKernel(t, domain.RateLimitConfig{
		MaxCalls: 1, Window: time.Minute, PerWhat: "execution", MaxWait: 5 * time.Minute,
	})

	if dec := submitRead(t, k, ctx, exec.ID); dec.Decision != "proceed" {
		t.Fatalf("first step expected proceed, got %+v", dec)
	}
	dec := submitRead(t, k, ctx, exec.ID)
	if dec.Decision != "blocked" || dec.Reason != domain.ReasonRateLimited {
		t.Fatalf("second step expected blocked/%s, got %+v", domain.ReasonRateLimited, dec)
	}

	// The queued dispatch is its own resumer, so it must still accept steps.
	got, err := k.GetExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ExecutionRunning {
		t.Fatalf("expected execution to stay running, got %s", got.Status)
	}

	dispatches, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pending []domain.Dispatch
	for _, d := range dispatches {
		if d.Status == domain.DispatchPending {
			pending = append(pending, d)
		}
	}
	if len(pending) != 1 {
		t.Fatalf("expected exactly one pending dispatch, got %d of %d", len(pending), len(dispatches))
	}
	if !pending[0].NextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("expected the resume dispatch to be deferred, next_attempt_at=%s", pending[0].NextAttemptAt)
	}

	steps, err := k.ListSteps(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("a parked step writes no step row, got %d steps", len(steps))
	}
}

func TestRateLimitParksOnlyOnce(t *testing.T) {
	k, ctx, exec := rateLimitedKernel(t, domain.RateLimitConfig{
		MaxCalls: 1, Window: time.Minute, PerWhat: "execution", MaxWait: 5 * time.Minute,
	})

	first := submitRead(t, k, ctx, exec.ID)
	if dec := submitRead(t, k, ctx, exec.ID); dec.Decision != "blocked" {
		t.Fatalf("expected the first limited step to park, got %+v", dec)
	}

	// What the agent does when the resume dispatch lands: replay the steps it
	// already recorded, then re-submit the one that was limited.
	replayed := submitRead(t, k, ctx, exec.ID)
	if replayed.StepID != first.StepID {
		t.Fatalf("resume should replay step %s, got %s", first.StepID, replayed.StepID)
	}
	dec := submitRead(t, k, ctx, exec.ID)
	if dec.Decision != "rate_limited" || dec.Reason != domain.ReasonRateLimited {
		t.Fatalf("expected the second limited step to be refused, got %+v", dec)
	}

	dispatches, err := k.Deps().Queue.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, d := range dispatches {
		if d.Status != domain.DispatchExhausted {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("a refusal queues no dispatch, got %d live", live)
	}
}

func TestRateLimitRefusesWaitOverMaxWait(t *testing.T) {
	k, ctx, exec := rateLimitedKernel(t, domain.RateLimitConfig{
		MaxCalls: 1, Window: time.Hour, PerWhat: "execution", MaxWait: time.Minute,
	})

	submitRead(t, k, ctx, exec.ID)
	dec := submitRead(t, k, ctx, exec.ID)
	if dec.Decision != "rate_limited" || dec.Reason != domain.ReasonRateLimited {
		t.Fatalf("a 1h wait under a 1m max_wait should refuse, got %+v", dec)
	}
}

// TestCompleteStepOnBlockedStepConflicts verifies that a step parked on a human
// approval cannot be resolved by the agent that proposed it. Its originating
// lease still checks out, so the status guard is the only thing standing
// between a blocked effect and a recorded outcome.
func TestCompleteStepOnBlockedStepConflicts(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalLLMEngine(t, time.Hour)}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	lease := leaseOf(t, k, exec.ID)
	stepID, _ := submitLLMStep(t, k, ctx, exec)

	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{"ok":true}`), Lease: lease}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected conflict completing a blocked step, got %v", err)
	}
	if _, err := k.FailStep(ctx, stepID, kernel.FailStepRequest{Error: json.RawMessage(`{"reason":"boom"}`), Lease: lease}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected conflict failing a blocked step, got %v", err)
	}

	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepAwaitingApproval {
		t.Fatalf("blocked step was mutated, status %s", step.Status)
	}
}
