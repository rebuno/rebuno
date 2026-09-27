package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func runStep(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, target, idempotency, result string) domain.StepDecision {
	t.Helper()
	dec, err := k.SubmitStep(ctx, id, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: target, Args: json.RawMessage(`{}`),
		Idempotency: idempotency, Lease: leaseOf(t, k, id),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision == "proceed" {
		if _, err := k.CompleteExecutionStep(ctx, id, dec.StepID, kernel.CompleteStepRequest{Result: json.RawMessage(result), Lease: leaseOf(t, k, id)}); err != nil {
			t.Fatal(err)
		}
	}
	return dec
}

func latestSeq(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID) int64 {
	t.Helper()
	seq, err := k.Deps().Events.GetLatestSequence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestForkReplaysStepsRecordedBeforeTheForkPoint(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			k, _, ctx := sessionKernels(t, backend)
			source := createInSession(t, k, ctx, "agent-1", "main")
			runStep(t, k, ctx, source.ID, "search", "safe_to_retry", `{"hits":1}`)
			at := latestSeq(t, k, ctx, source.ID)
			runStep(t, k, ctx, source.ID, "summarize", "safe_to_retry", `"old"`)

			fork, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{Session: "rewind", AtSeq: at})
			if err != nil {
				t.Fatal(err)
			}
			if fork.ForkedFrom == nil || *fork.ForkedFrom != source.ID || fork.ForkSeq != at || fork.ParentExecutionID != nil {
				t.Fatalf("fork lineage: %+v", fork)
			}
			if dec := runStep(t, k, ctx, fork.ID, "search", "safe_to_retry", `{}`); dec.Decision != "replay" {
				t.Fatalf("step before the fork point: %+v", dec)
			}
			if dec := runStep(t, k, ctx, fork.ID, "summarize", "safe_to_retry", `"new"`); dec.Decision != "proceed" {
				t.Fatalf("step after the fork point: %+v", dec)
			}

			events, err := k.GetEvents(ctx, fork.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			for _, e := range events {
				types = append(types, e.Type)
				if e.Type == domain.EventExecutionStarted {
					break
				}
			}
			want := []string{domain.EventExecutionCreated, domain.EventExecutionForked, domain.EventStepProposed,
				domain.EventStepAllowed, domain.EventStepExecuting, domain.EventStepSucceeded, domain.EventExecutionStarted}
			if len(types) != len(want) {
				t.Fatalf("fork log: %v", types)
			}
			for i := range want {
				if types[i] != want[i] {
					t.Fatalf("fork log: %v", types)
				}
			}
		})
	}
}

func TestForkRequiresApprovalToRepeatAnAtMostOnceEffect(t *testing.T) {
	k, _, ctx := sessionKernels(t, "memory")
	source := createInSession(t, k, ctx, "agent-1", "main")
	at := latestSeq(t, k, ctx, source.ID)
	runStep(t, k, ctx, source.ID, "open_pr", "at_most_once", `"#42"`)
	runStep(t, k, ctx, source.ID, "search", "safe_to_retry", `{}`)

	fork, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{AtSeq: at})
	if err != nil {
		t.Fatal(err)
	}
	if fork.Status != domain.ExecutionRunning || fork.Session != "" {
		t.Fatalf("fork without a session: %+v", fork)
	}
	dec := runStep(t, k, ctx, fork.ID, "open_pr", "at_most_once", `"#43"`)
	if dec.Decision != "blocked" || dec.ApprovalID == nil {
		t.Fatalf("repeated effect: %+v", dec)
	}
	approval, err := k.GetApproval(ctx, *dec.ApprovalID)
	if err != nil || approval.StepID != dec.StepID {
		t.Fatalf("approval: %+v, %v", approval, err)
	}
	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if dec := runStep(t, k, ctx, fork.ID, "search", "safe_to_retry", `{}`); dec.Decision != "proceed" {
		t.Fatalf("safe_to_retry effect: %+v", dec)
	}
}

func TestCreateContinuesANamedParent(t *testing.T) {
	k, _, ctx := sessionKernels(t, "memory")
	source := createInSession(t, k, ctx, "agent-1", "main")
	create := func(session string, parent uuid.UUID) (domain.Execution, error) {
		return k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), kernel.CreateExecutionOptions{Session: session, ParentExecutionID: &parent})
	}
	if _, err := create("alt", source.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("parent still running: %v", err)
	}
	if err := k.CompleteExecution(ctx, source.ID, leaseOf(t, k, source.ID), json.RawMessage(`{}`), json.RawMessage(`{"turns":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := create("main", source.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("parent named into an existing session: %v", err)
	}
	oneOff, err := create("", source.ID)
	if err != nil || oneOff.Status != domain.ExecutionRunning || oneOff.ParentExecutionID == nil || *oneOff.ParentExecutionID != source.ID {
		t.Fatalf("continuation without a session: %+v, %v", oneOff, err)
	}
	branch, err := create("alt", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if branch.Status != domain.ExecutionRunning || branch.ParentExecutionID == nil || *branch.ParentExecutionID != source.ID {
		t.Fatalf("branch: %+v", branch)
	}
	requirePreviousState(t, k, ctx, branch.ID, `{"turns":1}`)
	if err := k.CompleteExecution(ctx, branch.ID, leaseOf(t, k, branch.ID), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	next := createInSession(t, k, ctx, "agent-1", "alt")
	if next.ParentExecutionID == nil || *next.ParentExecutionID != branch.ID {
		t.Fatalf("next turn after the branch: %v", next.ParentExecutionID)
	}
}

func TestForkPolicyOverride(t *testing.T) {
	k, _, ctx := sessionKernels(t, "memory")
	source := createInSession(t, k, ctx, "agent-1", "main")
	at := latestSeq(t, k, ctx, source.ID)
	deny := "rules:\n  - id: no-search\n    when:\n      target: search\n    then:\n      decision: deny\n"

	writer := auth.WithClient(ctx, []domain.Scope{domain.ScopeExecutionsWrite})
	if _, err := k.ForkExecution(writer, source.ID, kernel.ForkRequest{Session: "strict", AtSeq: at, PolicyBundle: deny}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("override without policies:write: %v", err)
	}
	fork, err := k.ForkExecution(ctx, source.ID, kernel.ForkRequest{Session: "strict", AtSeq: at, PolicyBundle: deny})
	if err != nil {
		t.Fatal(err)
	}
	if dec := runStep(t, k, ctx, fork.ID, "search", "safe_to_retry", `{}`); dec.Decision != "denied" {
		t.Fatalf("fork under the override: %+v", dec)
	}
	if dec := runStep(t, k, ctx, source.ID, "search", "safe_to_retry", `{}`); dec.Decision != "proceed" {
		t.Fatalf("source under the agent policy: %+v", dec)
	}
}
