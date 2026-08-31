package kernel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
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
	ctx := context.Background()
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	for i := range 3 {
		dec := llmCall(t, k, ctx, exec.ID, `{"output":"hello"}`)
		if dec.Decision != "proceed" {
			t.Fatalf("call %d = %s: a response with no parseable usage never trips the budget", i, dec.Decision)
		}
	}
}
