package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func submitSubagentStep(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID, args string) string {
	t.Helper()
	dec, err := k.SubmitStep(ctx, execID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "run_subagent", Args: json.RawMessage(args), Lease: leaseOf(t, k, execID)})
	if err != nil || dec.Decision != "proceed" {
		t.Fatalf("submit subagent step: %+v, %v", dec, err)
	}
	return dec.StepID
}

func startSubagent(t *testing.T, k *kernel.Kernel, ctx context.Context, parentID uuid.UUID, stepID string) domain.Execution {
	t.Helper()
	child, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{SpawnedBy: &domain.SpawnedBy{ExecutionID: parentID, StepID: stepID}})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func suspend(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID) bool {
	t.Helper()
	suspended, err := k.Suspend(ctx, execID, leaseOf(t, k, execID))
	if err != nil {
		t.Fatal(err)
	}
	return suspended
}

func requireStep(t *testing.T, k *kernel.Kernel, ctx context.Context, stepID string, status domain.StepStatus) domain.Step {
	t.Helper()
	step, err := k.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != status {
		t.Fatalf("step %s: want %s, got %s", stepID, status, step.Status)
	}
	return step
}

func TestSuspendedParentResumesWhenSubagentCompletes(t *testing.T) {
	k, ctx := setup(t)
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	staleLease := leaseOf(t, k, parent.ID)
	stepID := submitSubagentStep(t, k, ctx, parent.ID, `{"task":"research"}`)

	child := startSubagent(t, k, ctx, parent.ID, stepID)
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
	if !suspend(t, k, ctx, parent.ID) {
		t.Fatal("parent with a live subagent did not suspend")
	}
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionBlocked)
	if _, err := k.SubmitStep(ctx, parent.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "other", Args: json.RawMessage(`{}`), Lease: staleLease}); err == nil {
		t.Fatal("blocked parent accepted a step on its released lease")
	}
	if again := startSubagent(t, k, ctx, parent.ID, stepID); again.ID != child.ID {
		t.Fatalf("restart created %s, want %s", again.ID, child.ID)
	}

	if err := k.CompleteExecution(ctx, child.ID, leaseOf(t, k, child.ID), json.RawMessage(`{"answer":42}`), nil); err != nil {
		t.Fatal(err)
	}
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
	dec, err := k.SubmitStep(ctx, parent.ID, kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "run_subagent", Args: json.RawMessage(`{"task":"research"}`), Lease: leaseOf(t, k, parent.ID)})
	if err != nil || dec.Decision != "replay" || string(dec.Result) != `{"answer":42}` {
		t.Fatalf("replay: %+v, %v", dec, err)
	}
}

func TestSubagentCancellationFailsParentStep(t *testing.T) {
	k, ctx := setup(t)
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	stepID := submitSubagentStep(t, k, ctx, parent.ID, `{}`)
	child := startSubagent(t, k, ctx, parent.ID, stepID)
	suspend(t, k, ctx, parent.ID)

	if err := k.CancelExecution(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	step := requireStep(t, k, ctx, stepID, domain.StepFailed)
	var got map[string]string
	if err := json.Unmarshal(step.Error, &got); err != nil || got["reason"] != "subagent_cancelled" || got["execution_id"] != child.ID.String() {
		t.Fatalf("step error: %s", step.Error)
	}
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
}

func TestParentResumesAfterLastSubagentSettles(t *testing.T) {
	k, ctx := setup(t)
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	first := submitSubagentStep(t, k, ctx, parent.ID, `{"n":1}`)
	second := submitSubagentStep(t, k, ctx, parent.ID, `{"n":2}`)
	a := startSubagent(t, k, ctx, parent.ID, first)
	b := startSubagent(t, k, ctx, parent.ID, second)
	suspend(t, k, ctx, parent.ID)

	if err := k.CompleteExecution(ctx, a.ID, leaseOf(t, k, a.ID), json.RawMessage(`1`), nil); err != nil {
		t.Fatal(err)
	}
	requireStep(t, k, ctx, first, domain.StepSucceeded)
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionBlocked)

	if err := k.CompleteExecution(ctx, b.ID, leaseOf(t, k, b.ID), json.RawMessage(`2`), nil); err != nil {
		t.Fatal(err)
	}
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
}

func TestSuspendAfterSubagentSettledContinues(t *testing.T) {
	k, ctx := setup(t)
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	stepID := submitSubagentStep(t, k, ctx, parent.ID, `{}`)
	child := startSubagent(t, k, ctx, parent.ID, stepID)
	if err := k.CompleteExecution(ctx, child.ID, leaseOf(t, k, child.ID), json.RawMessage(`"done"`), nil); err != nil {
		t.Fatal(err)
	}
	requireStep(t, k, ctx, stepID, domain.StepSucceeded)

	if suspend(t, k, ctx, parent.ID) {
		t.Fatal("suspended with no step waiting on a subagent")
	}
	requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
}

func approvalParent(t *testing.T) (*kernel.Kernel, context.Context, domain.Execution, string, domain.Execution, uuid.UUID) {
	t.Helper()
	engine, err := policy.NewRuleEngine(policy.Config{
		DefaultAction: domain.DecisionAllow,
		Rules: []policy.Rule{{
			ID:   "approve-llm",
			When: policy.Condition{StepKind: string(domain.StepKindLLM)},
			Then: domain.PolicyResult{Decision: domain.DecisionRequireApproval, ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Config{ReplicaID: "test"}, memDeps(memstore.NewStore(), kernel.Deps{Policy: engine}))
	ctx := auth.WithAdmin(context.Background())
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	stepID := submitSubagentStep(t, k, ctx, parent.ID, `{}`)
	child := startSubagent(t, k, ctx, parent.ID, stepID)
	_, approvalID := submitLLMStep(t, k, ctx, parent)
	return k, ctx, parent, stepID, child, approvalID
}

func TestBlockedParentResumesAfterApprovalAndSubagent(t *testing.T) {
	t.Run("subagent settles first", func(t *testing.T) {
		k, ctx, parent, stepID, child, approvalID := approvalParent(t)
		if err := k.CompleteExecution(ctx, child.ID, leaseOf(t, k, child.ID), json.RawMessage(`"done"`), nil); err != nil {
			t.Fatal(err)
		}
		requireStep(t, k, ctx, stepID, domain.StepSucceeded)
		requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionBlocked)
		if err := k.GrantApproval(ctx, approvalID, kernel.GrantApprovalRequest{DecidedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
	})
	t.Run("approval resolves first", func(t *testing.T) {
		k, ctx, parent, _, child, approvalID := approvalParent(t)
		if err := k.GrantApproval(ctx, approvalID, kernel.GrantApprovalRequest{DecidedBy: "ops"}); err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionBlocked)
		if err := k.CompleteExecution(ctx, child.ID, leaseOf(t, k, child.ID), json.RawMessage(`"done"`), nil); err != nil {
			t.Fatal(err)
		}
		requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
	})
}

func TestSubagentRequiresExecutingParentStep(t *testing.T) {
	k, ctx := setup(t)
	parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	stepID := submitSubagentStep(t, k, ctx, parent.ID, `{}`)
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{}`), Lease: leaseOf(t, k, parent.ID)}); err != nil {
		t.Fatal(err)
	}
	_, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{SpawnedBy: &domain.SpawnedBy{ExecutionID: parent.ID, StepID: stepID}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestCancellingParentCancelsSubagents(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
			child := startSubagent(t, k, ctx, parent.ID, submitSubagentStep(t, k, ctx, parent.ID, `{}`))
			grandchild := startSubagent(t, k, ctx, child.ID, submitSubagentStep(t, k, ctx, child.ID, `{}`))

			if err := k.CancelExecution(ctx, parent.ID); err != nil {
				t.Fatal(err)
			}
			for _, id := range []uuid.UUID{child.ID, grandchild.ID} {
				exec, err := k.GetExecution(ctx, id)
				if err != nil || exec.Status != domain.ExecutionCancelled || exec.FailureReason != domain.ReasonParentTerminal {
					t.Fatalf("subagent %s: %+v, %v", id, exec, err)
				}
			}
		})
	}
}

func TestSettleSubagentsRecoversMissedTerminal(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			parent, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
			stepID := submitSubagentStep(t, k, ctx, parent.ID, `{}`)
			child := startSubagent(t, k, ctx, parent.ID, stepID)
			suspend(t, k, ctx, parent.ID)
			if err := k.Deps().Executions.UpdateExecutionStatus(ctx, child.ID, domain.ExecutionCompleted, []byte(`"done"`), ""); err != nil {
				t.Fatal(err)
			}

			if err := k.SettleSubagents(ctx); err != nil {
				t.Fatal(err)
			}
			step := requireStep(t, k, ctx, stepID, domain.StepSucceeded)
			if string(step.Result) != `"done"` {
				t.Fatalf("result: %s", step.Result)
			}
			requireExecutionStatus(t, k, ctx, parent.ID, domain.ExecutionRunning)
		})
	}
}
