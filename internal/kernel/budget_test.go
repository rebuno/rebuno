package kernel_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/ratelimit"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func budgetKernel(t *testing.T, maxTokens int, onExceed string) (*kernel.Kernel, context.Context) {
	t.Helper()
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "llm-budget",
			When: policy.Condition{StepKind: string(domain.StepKindLLM)},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionAllow,
				Budget:         domain.BudgetConfig{MaxTokens: maxTokens, OnExceed: onExceed},
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(
		kernel.Config{ReplicaID: "test", DispatchBaseDelay: time.Millisecond},
		memDeps(ms, kernel.Deps{Policy: pe}),
	)
	ctx := auth.WithAdmin(context.Background())
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	return k, ctx
}

func usageBody(in, out int) string {
	return fmt.Sprintf(`{"usage":{"input_tokens":%d,"output_tokens":%d}}`, in, out)
}

// llmCall submits an llm_call and, if it is allowed to proceed, completes it
// with body as the recorded provider response.
func llmCall(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID, body string) domain.StepDecision {
	t.Helper()
	dec, err := k.SubmitStep(ctx, execID, kernel.SubmitStepRequest{
		Kind:   domain.StepKindLLM,
		Target: "claude-opus-5",
		Args:   json.RawMessage(`{"model":"claude-opus-5"}`),
		Lease:  leaseOf(t, k, execID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		return dec
	}
	result, err := json.Marshal(map[string]any{"status": 200, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CompleteStep(ctx, dec.StepID, kernel.CompleteStepRequest{Result: result, Lease: leaseOf(t, k, execID)}); err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestBudgetDeniesOnceSpendReachesLimit(t *testing.T) {
	k, ctx := budgetKernel(t, 1000, "")
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	if dec := llmCall(t, k, ctx, exec.ID, usageBody(600, 300)); dec.Decision != "proceed" {
		t.Fatalf("first call = %s, want proceed", dec.Decision)
	}
	if dec := llmCall(t, k, ctx, exec.ID, usageBody(600, 300)); dec.Decision != "proceed" {
		t.Fatalf("second call = %s, want proceed at 900 of 1000", dec.Decision)
	}

	dec := llmCall(t, k, ctx, exec.ID, usageBody(10, 10))
	if dec.Decision != "denied" {
		t.Fatalf("third call = %s, want denied", dec.Decision)
	}
	if dec.Reason != "execution_token_budget_exceeded" {
		t.Fatalf("reason = %q, want execution_token_budget_exceeded", dec.Reason)
	}
}

func TestBudgetCanRequireApprovalInstead(t *testing.T) {
	k, ctx := budgetKernel(t, 500, domain.DecisionRequireApproval)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	llmCall(t, k, ctx, exec.ID, usageBody(400, 200))

	dec := llmCall(t, k, ctx, exec.ID, usageBody(10, 10))
	if dec.Decision != "blocked" {
		t.Fatalf("over-budget call = %s, want blocked", dec.Decision)
	}
	if dec.ApprovalID == nil {
		t.Fatal("blocked call recorded no approval")
	}
}

func TestBudgetIsBlindToUnmeasuredResponses(t *testing.T) {
	k, ctx := budgetKernel(t, 10, "")
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	for i := range 3 {
		dec := llmCall(t, k, ctx, exec.ID, `{"output":"hello"}`)
		if dec.Decision != "proceed" {
			t.Fatalf("call %d = %s: a response with no parseable usage never trips the budget", i, dec.Decision)
		}
	}
}

func TestSessionBudgetCountsEarlierExecutionsInTheSession(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		for _, scope := range []string{domain.BudgetScopeExecution, domain.BudgetScopeSession} {
			t.Run(backend+"/"+scope, func(t *testing.T) {
				base, _, ctx := sessionKernels(t, backend)
				pe, err := policy.NewRuleEngine(policy.Config{Rules: []policy.Rule{{
					ID:   "llm-budget",
					When: policy.Condition{StepKind: string(domain.StepKindLLM)},
					Then: domain.PolicyResult{Decision: domain.DecisionAllow, Budget: domain.BudgetConfig{MaxTokens: 1000, Scope: scope}},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				deps := base.Deps()
				deps.Policy = pe
				k := kernel.New(kernel.DefaultConfig(), deps)
				first := createInSession(t, k, ctx, "agent-1", "chat")
				llmCall(t, k, ctx, first.ID, usageBody(600, 400))
				if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{}`), nil); err != nil {
					t.Fatal(err)
				}
				second := createInSession(t, k, ctx, "agent-1", "chat")
				want := map[string]string{domain.BudgetScopeExecution: "proceed", domain.BudgetScopeSession: "denied"}[scope]
				if dec := llmCall(t, k, ctx, second.ID, usageBody(1, 1)); dec.Decision != want {
					t.Fatalf("second execution's call = %s, want %s", dec.Decision, want)
				}
			})
		}
	}
}

func TestSessionRateLimitSharesCallsAcrossTheSession(t *testing.T) {
	pe, err := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "one-read-per-session",
			When: policy.Condition{Target: "read"},
			Then: domain.PolicyResult{
				Decision:  domain.DecisionAllow,
				RateLimit: domain.RateLimitConfig{MaxCalls: 1, Window: time.Hour, PerWhat: domain.PerWhatSession},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), memDeps(memstore.NewStore(), kernel.Deps{Policy: pe, RateLimiter: ratelimit.NewMemoryLimiter()}))
	ctx := auth.WithAdmin(context.Background())
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	read := func(session string) string {
		t.Helper()
		exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{Session: session})
		if err != nil {
			t.Fatal(err)
		}
		dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, exec.ID)})
		if err != nil {
			t.Fatal(err)
		}
		if err := k.CompleteExecution(ctx, exec.ID, leaseOf(t, k, exec.ID), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
		return dec.Decision
	}
	for _, c := range []struct{ session, want string }{{"chat", "proceed"}, {"chat", "rate_limited"}, {"other", "proceed"}} {
		if got := read(c.session); got != c.want {
			t.Fatalf("read in %s = %s, want %s", c.session, got, c.want)
		}
	}
}

func TestDecisionEventsRecordThePolicyHash(t *testing.T) {
	const bundle = "default_action: allow\n"
	pe, err := policy.NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.DefaultConfig(), memDeps(memstore.NewStore(), kernel.Deps{Policy: pe}))
	ctx := auth.WithAdmin(context.Background())
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, exec.ID)}); err != nil {
		t.Fatal(err)
	}
	events, err := k.GetEvents(ctx, exec.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(bundle))
	for _, e := range events {
		if e.Type != domain.EventStepAllowed {
			continue
		}
		var payload struct {
			PolicyHash string `json:"policy_hash"`
		}
		if err := json.Unmarshal(e.Payload, &payload); err != nil || payload.PolicyHash != hex.EncodeToString(sum[:]) {
			t.Fatalf("step.allowed policy_hash = %q, %v", payload.PolicyHash, err)
		}
		return
	}
	t.Fatal("no step.allowed event")
}
