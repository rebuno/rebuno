package kernel_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/identity"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

// submitExecutingStep creates an execution, claims its dispatch, and submits a
// tool step so that the step lands in the executing state with started_at set.
// It returns the execution id, the step id, and the memstore (for direct
// manipulation of started_at to simulate staleness).
func submitExecutingStep(t *testing.T, stalledTimeout time.Duration) (uuid.UUID, string, *memstore.Store, *kernel.Kernel, context.Context) {
	t.Helper()
	ms := memstore.NewStore()
	cfg := kernel.Config{
		ReplicaID:          "test",
		StepStalledTimeout: stalledTimeout,
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
		Policy:     policy.PermissiveEngine{},
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	// Claim the dispatch so it moves to in_flight, then ack it to simulate the
	// agent acking and subsequently dying mid-step.
	q := k.Deps().Queue
	claimed, err := q.Claim(ctx, "replica-1", 10, time.Now().UTC())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v (n=%d)", err, len(claimed))
	}
	if err := q.Ack(ctx, claimed[0].ID, domain.DispatchAcked, nil); err != nil {
		t.Fatal(err)
	}

	args := json.RawMessage(`{"path":"/tmp"}`)
	argsHash, _ := identity.ComputeArgsHash(args)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", argsHash, 0)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, DispatchID: claimed[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %s", dec.Decision)
	}
	return exec.ID, stepID, ms, k, ctx
}

// ageStalledStep rewrites the step's started_at far enough in the past that it
// is older than the stalled cutoff, simulating an agent that died mid-step.
func ageStalledStep(t *testing.T, ms *memstore.Store, stepID string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	step, err := ms.GetStep(ctx, stepID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-age)
	step.StartedAt = &old
	if err := ms.Upsert(ctx, step); err != nil {
		t.Fatal(err)
	}
}

// countLiveDispatches returns the number of pending/in_flight/failed dispatches
// for the execution — a re-dispatch should add one.
func countLiveDispatches(t *testing.T, k *kernel.Kernel, ctx context.Context, execID uuid.UUID) int {
	t.Helper()
	dispatches, err := k.Deps().Queue.ListDispatchesByExecution(ctx, execID)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, d := range dispatches {
		switch d.Status {
		case domain.DispatchPending, domain.DispatchInFlight, domain.DispatchFailed:
			live++
		}
	}
	return live
}

// TestReclaimStalledExecutionsRedispatches verifies that an execution whose
// step has been executing past the stalled timeout gets re-dispatched. This is
// the core fix for issue #126: the agent died mid-step after acking, so
// ReclaimStalled (which only touches in_flight dispatches) never fires.
func TestReclaimStalledExecutionsRedispatches(t *testing.T) {
	execID, stepID, ms, k, ctx := submitExecutingStep(t, 50*time.Millisecond)

	// Before aging: no new dispatch, step still executing.
	before := countLiveDispatches(t, k, ctx, execID)
	if before != 0 {
		t.Fatalf("expected 0 live dispatches after ack, got %d", before)
	}

	// Age the step past the stalled timeout and run the worker.
	ageStalledStep(t, ms, stepID, 100*time.Millisecond)
	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	after := countLiveDispatches(t, k, ctx, execID)
	if after != 1 {
		t.Fatalf("expected 1 live dispatch after reclaim, got %d", after)
	}

	// The re-dispatched agent would replay the recorded step: it is still
	// executing (not yet completed), so SubmitStep returns proceed.
	did := dispatchOf(t, k, execID)
	dec, err := k.SubmitStep(ctx, execID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{"path":"/tmp"}`), DispatchID: did,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed on re-dispatched executing step, got %s", dec.Decision)
	}
}

// TestReclaimStalledExecutionsSkipsFreshStep verifies that a step still within
// the stalled timeout is not re-dispatched: the agent is presumed alive.
func TestReclaimStalledExecutionsSkipsFreshStep(t *testing.T) {
	execID, _, _, k, ctx := submitExecutingStep(t, time.Hour)

	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := countLiveDispatches(t, k, ctx, execID); got != 0 {
		t.Fatalf("expected no re-dispatch for fresh step, got %d live dispatches", got)
	}
}

// TestReclaimStalledExecutionsSkipsCompletedStep verifies that a step that was
// executing when ListStalledSteps ran but completed before the re-dispatch
// (under the lock) is not re-dispatched.
func TestReclaimStalledExecutionsSkipsCompletedStep(t *testing.T) {
	execID, stepID, ms, k, ctx := submitExecutingStep(t, 50*time.Millisecond)
	ageStalledStep(t, ms, stepID, 100*time.Millisecond)

	// Complete the step so it is no longer executing.
	if _, err := k.CompleteStep(ctx, stepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}

	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := countLiveDispatches(t, k, ctx, execID); got != 0 {
		t.Fatalf("expected no re-dispatch for completed step, got %d live dispatches", got)
	}
}

// TestReclaimStalledExecutionsSkipsExistingLiveDispatch verifies that the
// worker does not enqueue a second dispatch when one is already live (e.g. the
// agent was already re-dispatched by another path such as an approval grant).
func TestReclaimStalledExecutionsSkipsExistingLiveDispatch(t *testing.T) {
	execID, stepID, ms, k, ctx := submitExecutingStep(t, 50*time.Millisecond)
	ageStalledStep(t, ms, stepID, 100*time.Millisecond)

	// Simulate a prior re-dispatch (e.g. from an approval grant or manual
	// EnqueueReDrive) leaving a pending dispatch in place.
	if err := k.EnqueueReDrive(ctx, execID); err != nil {
		t.Fatal(err)
	}
	before := countLiveDispatches(t, k, ctx, execID)
	if before != 1 {
		t.Fatalf("expected 1 live dispatch before reclaim, got %d", before)
	}

	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := countLiveDispatches(t, k, ctx, execID); got != 1 {
		t.Fatalf("expected no additional re-dispatch, got %d live dispatches", got)
	}
}

// TestReclaimStalledExecutionsSkipsTerminalExecution verifies that an execution
// which was cancelled (terminal) between the scan and the re-dispatch is not
// re-dispatched.
func TestReclaimStalledExecutionsSkipsTerminalExecution(t *testing.T) {
	execID, stepID, ms, k, ctx := submitExecutingStep(t, 50*time.Millisecond)
	ageStalledStep(t, ms, stepID, 100*time.Millisecond)

	if err := k.CancelExecution(ctx, execID); err != nil {
		t.Fatal(err)
	}

	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := countLiveDispatches(t, k, ctx, execID); got != 0 {
		t.Fatalf("expected no re-dispatch for terminal execution, got %d live dispatches", got)
	}
}

// TestReclaimStalledExecutionsDefaultsTimeout verifies that a zero
// StepStalledTimeout falls back to DispatchLeaseTimeout.
func TestReclaimStalledExecutionsDefaultsTimeout(t *testing.T) {
	// Build the kernel with only DispatchLeaseTimeout set; StepStalledTimeout
	// should default to it.
	ms := memstore.NewStore()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchLeaseTimeout: 50 * time.Millisecond,
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
		Policy:     policy.PermissiveEngine{},
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	q := k.Deps().Queue
	claimed, err := q.Claim(ctx, "replica-1", 10, time.Now().UTC())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v (n=%d)", err, len(claimed))
	}
	if err := q.Ack(ctx, claimed[0].ID, domain.DispatchAcked, nil); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"path":"/tmp"}`)
	argsHash, _ := identity.ComputeArgsHash(args)
	stepID := identity.ComputeStepID(exec.ID, domain.StepKindTool, "read", argsHash, 0)
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, DispatchID: claimed[0].ID,
	}); err != nil {
		t.Fatal(err)
	}
	ageStalledStep(t, ms, stepID, 100*time.Millisecond)

	if err := k.ReclaimStalledExecutions(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := countLiveDispatches(t, k, ctx, exec.ID); got != 1 {
		t.Fatalf("expected re-dispatch using defaulted timeout, got %d live dispatches", got)
	}
}
