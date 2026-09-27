package kernel_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func TestTestPolicyReplaysRecordedSteps(t *testing.T) {
	k, ctx := setup(t)
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind:   domain.StepKindTool,
		Target: "read",
		Args:   json.RawMessage(`{"path":"/tmp"}`),
		Lease:  leaseOf(t, k, exec.ID),
	}); err != nil {
		t.Fatal(err)
	}

	report, err := k.TestPolicy(ctx, "agent-1", kernel.PolicyTestRequest{
		Bundle:      "default_action: deny\n",
		ExecutionID: &exec.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(report.Results))
	}
	res := report.Results[0]
	if res.Pass {
		t.Fatal("expected the recorded allow to conflict with a deny-all bundle")
	}
	if res.Failure != "deny, want allow" {
		t.Fatalf("failure = %q", res.Failure)
	}
	if res.Target != "read" || res.Args["path"] != "/tmp" {
		t.Fatalf("replayed case lost the step's input: %+v", res.Case)
	}
	if res.WasRule != "permissive" {
		t.Fatalf("was_rule = %q, want permissive", res.WasRule)
	}
}

func TestTestPolicyRejectsBadRequests(t *testing.T) {
	k, ctx := setup(t)
	if _, err := k.TestPolicy(ctx, "agent-1", kernel.PolicyTestRequest{Bundle: "rules:\n  - when: {}\n"}); err == nil {
		t.Fatal("expected a rule without an id to be rejected")
	}
	// An unknown agent id would otherwise be fed to every agent_id rule as-is.
	_, err := k.TestPolicy(ctx, "nope", kernel.PolicyTestRequest{Bundle: "default_action: allow\n"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown agent = %v, want not found", err)
	}
}

func TestTestPolicyReplaysSelectedExecutions(t *testing.T) {
	k, ctx := setup(t)
	var ids []uuid.UUID
	for _, session := range []string{"chat", "chat", "other"} {
		exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{Session: session})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
			Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{}`), Lease: leaseOf(t, k, exec.ID),
		}); err != nil {
			t.Fatal(err)
		}
		if err := k.CompleteExecution(ctx, exec.ID, leaseOf(t, k, exec.ID), json.RawMessage(`{}`), nil); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, exec.ID)
	}

	replayed := func(sel kernel.ExecutionSelector) []string {
		t.Helper()
		report, err := k.TestPolicy(ctx, "agent-1", kernel.PolicyTestRequest{Bundle: "default_action: deny\n", Executions: &sel})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, res := range report.Results {
			if res.Pass {
				t.Fatalf("recorded allow matched a deny-all bundle: %+v", res)
			}
			got = append(got, res.ExecutionID)
		}
		return got
	}
	if got := replayed(kernel.ExecutionSelector{Session: "chat"}); len(got) != 2 || got[0] != ids[1].String() || got[1] != ids[0].String() {
		t.Fatalf("session selection: %v", got)
	}
	if got := replayed(kernel.ExecutionSelector{Limit: 1}); len(got) != 1 || got[0] != ids[2].String() {
		t.Fatalf("limit selection: %v", got)
	}
	future := time.Now().Add(time.Hour)
	if got := replayed(kernel.ExecutionSelector{Since: &future}); len(got) != 0 {
		t.Fatalf("since selection: %v", got)
	}
}
