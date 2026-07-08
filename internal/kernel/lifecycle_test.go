package kernel_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func TestApprovalExpiry(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", DefaultApprovalTimeout: 1 * time.Millisecond}
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:       "approve-read",
			Priority: 1,
			When:     policy.Condition{Target: "write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: 1 * time.Millisecond},
			},
		}},
	})
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms, Policy: pe,
	})
	ctx := context.Background()
	k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	args := json.RawMessage(`{"path":"/tmp"}`)
	argsHash, _ := identity.ComputeArgsHash(args)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "write", argsHash, 0)
	dec, _ := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "write", Args: args, StepID: stepID})
	if dec.Decision != "blocked" {
		t.Fatalf("expected blocked, got %s", dec.Decision)
	}
	// Wait for timeout and run expiry.
	time.Sleep(10 * time.Millisecond)
	if err := k.ExpireApprovals(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	exec, _ = k.GetExecution(ctx, exec.ID)
	if exec.Status != domain.ExecutionFailed || exec.FailureReason != "approval_timeout" {
		t.Fatalf("expected failed approval_timeout, got %s %s", exec.Status, exec.FailureReason)
	}
}

func TestCancelExpiredExecutions(t *testing.T) {
	ms := memstore.NewStore()
	cfg := kernel.Config{ReplicaID: "test", ExecutionDeadlineTimeout: 1 * time.Millisecond}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: policy.PermissiveEngine{},
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if exec.DeadlineAt == nil {
		t.Fatal("expected execution deadline")
	}
	time.Sleep(10 * time.Millisecond)
	if err := k.CancelExpiredExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, err := k.GetExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ExecutionCancelled {
		t.Fatalf("expected cancelled, got %s", got.Status)
	}
}
