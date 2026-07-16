package kernel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/dispatcher"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

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

func TestCreateExecution(t *testing.T) {
	k, ctx := setup(t)
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{"msg":"hi"}`), "v1")
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	argsHash := mustHash(args)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", argsHash, 0)

	// First submit -> proceed
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}

	result := json.RawMessage(`{"ok":true}`)
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: result}); err != nil {
		t.Fatal(err)
	}

	// Replay returns result. The step_id/occurrence must remain stable.
	dec2, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: stepID})
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

func TestStepIDDivergence(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	args := json.RawMessage(`{"path":"/tmp"}`)
	_, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: "wrong-id"})
	if err == nil {
		t.Fatal("expected divergence error")
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
	exec, _ := k2.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", mustHash(args), 0)
	dec, err := k2.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "denied" {
		t.Fatalf("expected denied, got %s", dec.Decision)
	}
}

func TestApprovalFlow(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:       "approve-read",
			Priority: 1,
			When:     policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, kernel.Deps{
		Events:     ms,
		Steps:      ms,
		Executions: ms,
		Agents:     ms,
		Approvals:  ms,
		Queue:      ms,
		Locker:     ms,
		UnitOfWork: ms,
		Policy:     pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "write", mustHash(args), 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("expected blocked with approval id, got %+v", dec)
	}
	if got, _ := k.GetExecution(ctx, exec.ID); got.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", got.Status)
	}

	// Replay while blocked returns blocked.
	dec2, _ := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID})
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
	dec3, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID})
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
			ID:       "approve-write",
			Priority: 1,
			When:     policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms,
		Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms, Policy: pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "write", mustHash(args), 0)
	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID, Idempotency: "at_most_once"}

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
			ID:       "approve-read",
			Priority: 1,
			When:     policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "write", mustHash(args), 0)
	dec, _ := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID})
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	req := json.RawMessage(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	argsHash, _ := identity.ComputeArgsHash(req)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindLLM, "gpt-4", argsHash, 0)

	// An llm_call goes through the same submit_step write path as a tool call.
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}
	// Re-submitting the same step_id while still executing must be accepted (no divergence).
	dec2, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec2.Decision != "proceed" {
		t.Fatalf("expected proceed on second submit while executing, got %s", dec2.Decision)
	}

	resp := json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`)
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: resp}); err != nil {
		t.Fatal(err)
	}
	// After completion, replay returns the cached result.
	dec3, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, StepID: stepID})
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", mustHash(json.RawMessage(`{}`)), 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), StepID: stepID})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "execution_terminal" {
		t.Fatalf("expected terminal, got %s", dec.Decision)
	}
}

func TestDispatcherDeliveryAndRetry(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	var lastBody []byte
	var lastSig string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		lastBody, _ = io.ReadAll(r.Body)
		lastSig = r.Header.Get("Rebuno-Signature")
		// Verify HMAC over raw body.
		expected := dispatcher.SignPayload("secret", lastBody)
		if lastSig != "sha256="+expected {
			t.Errorf("signature mismatch: got %s want sha256=%s", lastSig, expected)
		}
		if called < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 3, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// Wait for queue-level exponential backoff.
	time.Sleep(5 * time.Millisecond)
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if called < 2 {
		t.Fatalf("expected retries, called %d", called)
	}
	if lastSig == "" {
		t.Fatal("missing signature")
	}
	// Confirm the transmitted body does not contain a signature field.
	if bytes.Contains(lastBody, []byte(`"signature"`)) {
		t.Fatal("signature must not be part of the request body")
	}
	exec, _ = k.GetExecution(ctx, exec.ID)
	if exec.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after ack, got %s", exec.Status)
	}
}

// TestDispatchRejectionExhaustsAndFails guards the regression where an agent
// 4xx acked the dispatch 'failed' with a NULL next_attempt_at, stranding it:
// never retried, never exhausted, execution never failed. A persistent 4xx must
// retry with backoff up to max attempts, then fail the execution.
func TestDispatchRejectionExhaustsAndFails(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusBadRequest) // persistent 4xx
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 2, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Attempt 1 (fails, schedules retry), then attempt 2 (hits max, exhausts).
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if called < 2 {
		t.Fatalf("expected 4xx to be retried, called %d", called)
	}
	exec, _ = k.GetExecution(ctx, exec.ID)
	if exec.Status != domain.ExecutionFailed {
		t.Fatalf("expected execution failed after exhaustion, got %s", exec.Status)
	}
	if exec.FailureReason != "dispatch_exhausted" {
		t.Fatalf("expected dispatch_exhausted reason, got %q", exec.FailureReason)
	}
}

func TestRateLimitDoubleStep(t *testing.T) {
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:       "rate-limit-read",
			Priority: 1,
			When:     policy.Condition{Target: "read"},
			Then: domain.PolicyResult{
				Decision:  domain.DecisionAllow,
				RateLimit: domain.RateLimitConfig{MaxCalls: 1, Window: time.Hour, PerWhat: "execution"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		Events:      ms,
		Steps:       ms,
		Executions:  ms,
		Agents:      ms,
		Approvals:   ms,
		Queue:       ms,
		Locker:      ms,
		UnitOfWork:  ms,
		Policy:      pe,
		RateLimiter: ratelimit.NewMemoryLimiter(),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/tmp"}`)
	hash := mustHash(args)
	step0 := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", hash, 0)
	step1 := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", hash, 1)

	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: step0})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("first step expected proceed, got %s", dec.Decision)
	}

	dec, err = k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, StepID: step1})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "rate_limited" || dec.Reason != "rate_limit_exceeded" {
		t.Fatalf("second step expected rate_limited, got %+v", dec)
	}
}

// TestDispatchTimeoutBoundsHungAgent verifies a non-responsive agent webhook is
// bounded by DispatchTimeout rather than blocking a delivery slot indefinitely.
func TestDispatchTimeoutBoundsHungAgent(t *testing.T) {
	ms := memstore.NewStore()
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open until the test releases it
		w.WriteHeader(http.StatusOK)
	}))
	defer hung.Close()
	defer close(release)

	cfg := kernel.Config{
		ReplicaID:           "test",
		DispatchMaxAttempts: 1,
		DispatchBaseDelay:   1 * time.Millisecond,
		DispatchTimeout:     50 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: hung.URL, Secret: "secret"})
	k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	start := time.Now()
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("delivery was not bounded by DispatchTimeout, took %v", elapsed)
	}
}

// TestDispatchConcurrency verifies a batch of slow deliveries runs concurrently:
// wall-clock time stays far below the serial sum of per-delivery latencies.
func TestDispatchConcurrency(t *testing.T) {
	ms := memstore.NewStore()
	const perCall = 80 * time.Millisecond
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(perCall)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	const n = 6
	cfg := kernel.Config{
		ReplicaID:           "test",
		DispatchMaxAttempts: 3,
		DispatchBaseDelay:   1 * time.Millisecond,
		DispatchTimeout:     2 * time.Second,
		DispatchConcurrency: n,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: slow.URL, Secret: "secret"})
	for i := 0; i < n; i++ {
		k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	}

	start := time.Now()
	if err := k.RunDispatches(ctx, n); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	// Serial delivery would take >= n*perCall (480ms). Concurrent delivery should
	// finish in roughly one perCall window; allow generous slack for CI jitter.
	if elapsed >= n*perCall/2 {
		t.Fatalf("deliveries did not run concurrently: took %v for %d jobs of %v each", elapsed, n, perCall)
	}
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
			ID:       "approve-llm",
			Priority: 1,
			When:     policy.Condition{StepKind: string(domain.StepKindLLM)},
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
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindLLM, Target: "gpt-4", Args: req, StepID: stepID})
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

func TestApprovalGrantRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
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
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepFailed); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepFailed step_type = %q, want %q", got, domain.StepKindLLM)
	}
}

// TestApprovalDenyFailsExecution verifies that denying an approval fails the
// execution directly (consistent with expiry) rather than resuming it.
func TestApprovalDenyFailsExecution(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	_, approvalID := submitLLMStep(t, k, ctx, exec)

	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil {
		t.Fatal(err)
	}
	got, _ := k.GetExecution(ctx, exec.ID)
	if got.Status != domain.ExecutionFailed {
		t.Fatalf("expected execution failed after deny, got %s", got.Status)
	}
	if got.FailureReason != "approval_denied" {
		t.Fatalf("expected failure reason approval_denied, got %q", got.FailureReason)
	}
	// Deny must not enqueue a new dispatch: count dispatches before and after.
	before, err := ms.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := len(before)
	if err := k.DenyApproval(ctx, approvalID, kernel.DenyApprovalRequest{DecidedBy: "bob"}); err != nil && !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	after, err := ms.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != beforeCount {
		t.Fatalf("deny must not enqueue a dispatch, had %d before and %d after", beforeCount, len(after))
	}
}

func TestApprovalExpireRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: 1 * time.Millisecond}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, 1*time.Millisecond),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	submitLLMStep(t, k, ctx, exec)

	time.Sleep(10 * time.Millisecond)
	if err := k.ExpireApprovals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepFailed); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepFailed step_type = %q, want %q", got, domain.StepKindLLM)
	}
}

func TestCancelExecutionRecordsActualStepKind(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	submitLLMStep(t, k, ctx, exec)

	if err := k.CancelExecution(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepDenied); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepDenied step_type = %q, want %q", got, domain.StepKindLLM)
	}
	if got := findStepType(t, k, ctx, exec.ID, domain.EventStepFailed); got != string(domain.StepKindLLM) {
		t.Fatalf("EventStepFailed step_type = %q, want %q", got, domain.StepKindLLM)
	}
}

// TestCancelExecutionCancelsPendingApprovals verifies that cancelling an
// execution expires its pending approvals and does not leave them orphaned.
func TestCancelExecutionCancelsPendingApprovals(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalLLMEngine(t, time.Hour),
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
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
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

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
    priority: 10
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
	k := kernel.New(cfg, kernel.Deps{
		Events:     ms,
		Steps:      ms,
		Executions: ms,
		Agents:     ms,
		Approvals:  ms,
		Queue:      ms,
		Locker:     ms,
		UnitOfWork: ms,
		Policy:     pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/etc/passwd"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "fs_write", mustHash(args), 0)
	before := time.Now().UTC()
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: args, StepID: stepID,
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
    priority: 10
    when:
      target: fs_write
    then:
      decision: require_approval
` + approvalConfig)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: time.Hour}, kernel.Deps{
		Events:     ms,
		Steps:      ms,
		Executions: ms,
		Agents:     ms,
		Approvals:  ms,
		Queue:      ms,
		Locker:     ms,
		UnitOfWork: ms,
		Policy:     pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	args := json.RawMessage(`{"path":"/etc/passwd"}`)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "fs_write", mustHash(args), 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: args, StepID: stepID,
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
