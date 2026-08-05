package kernel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func approvalPolicy() *policy.RuleEngine {
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:       "approve-fs-write",
			Priority: 1,
			When:     policy.Condition{Target: "fs_write"},
			Then: domain.PolicyResult{
				Decision:       domain.DecisionRequireApproval,
				ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
			},
		}},
	})
	return pe
}

// Flow: deliver → no heartbeat → lease expires → ReclaimStalled flips
// the row back to pending → the drain loop re-delivers under the same
// dispatch id → the agent replays into handleExistingStep.
func TestDispatchLeaseSurvivesAck(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  5,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      1 * time.Second,
		DispatchLeaseTimeout: 10 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// First delivery: the webhook returns 200. The dispatch must stay
	// in_flight (not be acked) so the lease can expire and be reclaimed.
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("expected 1 delivery, got %d", called)
	}
	did := dispatchOf(t, k, exec.ID)
	d, err := ms.GetDispatch(ctx, did)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("dispatch must stay in_flight after successful delivery, got %s", d.Status)
	}

	// No heartbeat arrives. Wait for the lease to expire, then ReclaimStalled
	// flips the row back to pending so the drain loop can re-deliver.
	time.Sleep(20 * time.Millisecond)
	reclaimed, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != did {
		t.Fatalf("expected the in_flight dispatch to be reclaimed, got %d", len(reclaimed))
	}

	// The drain loop re-delivers under the same dispatch id.
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if called != 2 {
		t.Fatalf("expected re-delivery after reclaim, got %d deliveries", called)
	}
	d, err = ms.GetDispatch(ctx, did)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("re-delivered dispatch must stay in_flight, got %s", d.Status)
	}

	// The re-delivered agent replays into handleExistingStep: the step is
	// still executing (no completion recorded), so a safe_to_retry step
	// proceeds and an at_most_once step resolves as indeterminate.
	args := json.RawMessage(`{"path":"/tmp"}`)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, DispatchID: did,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("redelivered dispatch must proceed for safe_to_retry, got %s", dec.Decision)
	}
}

// TestDispatchLeaseRenewedByHeartbeat verifies that a heartbeat while the
// dispatch is in_flight resets the lease so ReclaimStalled does not reclaim it.
func TestDispatchLeaseRenewedByHeartbeat(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  5,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      1 * time.Second,
		DispatchLeaseTimeout: 50 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}

	// Heartbeat renews the lease.
	if err := k.Heartbeat(ctx, exec.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now, cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("heartbeat must renew the lease, but %d dispatches were reclaimed", len(reclaimed))
	}
}

// TestCompleteExecutionReleasesLease verifies that completing the execution
// retires the dispatch lease so a healthy execution is not re-dispatched.
func TestCompleteExecutionReleasesLease(t *testing.T) {
	ms := memstore.NewStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  5,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      1 * time.Second,
		DispatchLeaseTimeout: 50 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	did := dispatchOf(t, k, exec.ID)

	if err := k.CompleteExecution(ctx, exec.ID, json.RawMessage(`{"done":true}`)); err != nil {
		t.Fatal(err)
	}
	d, err := ms.GetDispatch(ctx, did)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchExhausted {
		t.Fatalf("complete must release the lease (exhausted), got %s", d.Status)
	}
	// ReclaimStalled must not flip it back to pending.
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now.Add(time.Hour), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatal("exhausted dispatch must not be reclaimed")
	}
}

// TestApprovalBlockReleasesLease verifies that the require_approval branch
// releases the dispatch lease so the execution is not re-delivered every
// 2 minutes (which would hit dispatch_exhausted before the approval timeout).
func TestApprovalBlockReleasesLease(t *testing.T) {
	ms := memstore.NewStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:              "test",
		DispatchMaxAttempts:    5,
		DispatchBaseDelay:      1 * time.Millisecond,
		DispatchTimeout:        1 * time.Second,
		DispatchLeaseTimeout:   50 * time.Millisecond,
		DefaultApprovalTimeout: time.Hour,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy: approvalPolicy(),
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Deliver, then submit a step that requires approval.
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	did := dispatchOf(t, k, exec.ID)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: json.RawMessage(`{"path":"/etc"}`), DispatchID: did,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" {
		t.Fatalf("expected blocked, got %s", dec.Decision)
	}

	// The dispatch must be released (exhausted), not left in_flight.
	d, err := ms.GetDispatch(ctx, did)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchExhausted {
		t.Fatalf("approval block must release the lease (exhausted), got %s", d.Status)
	}
	// ReclaimStalled must not re-deliver.
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now.Add(time.Hour), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatal("approval-blocked dispatch must not be reclaimed")
	}
	// Granting approval enqueues a fresh dispatch.
	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	// The new dispatch is pending.
	dispatches, err := ms.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPending := false
	for _, d := range dispatches {
		if d.Status == domain.DispatchPending {
			foundPending = true
			break
		}
	}
	if !foundPending {
		t.Fatal("expected a pending dispatch after approval grant")
	}
}

// TestDispatchRedeliveryCapFailsExecution verifies the redelivery cap: a
// webhook that always 200s but never completes would otherwise redeliver
// forever (the success branch has no MaxAttempts guard). Once attempts exceed
// MaxAttempts, deliver fails the execution dispatch_exhausted instead of
// re-delivering.
func TestDispatchRedeliveryCapFailsExecution(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  2,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      1 * time.Second,
		DispatchLeaseTimeout: 10 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Attempt 1: deliver succeeds, dispatch stays in_flight.
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// Let the lease expire and reclaim, then re-deliver (attempt 2).
	time.Sleep(20 * time.Millisecond)
	if _, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10); err != nil {
		t.Fatal(err)
	}
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	// Attempt 3 exceeds MaxAttempts: deliver must fail the execution rather
	// than re-deliver.
	time.Sleep(20 * time.Millisecond)
	if _, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10); err != nil {
		t.Fatal(err)
	}
	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	exec, _ = k.GetExecution(ctx, exec.ID)
	if exec.Status != domain.ExecutionFailed {
		t.Fatalf("expected execution failed after redelivery cap, got %s", exec.Status)
	}
	if exec.FailureReason != "dispatch_exhausted" {
		t.Fatalf("expected dispatch_exhausted reason, got %q", exec.FailureReason)
	}
}

// TestSubmitStepRenewsLease verifies that submitting a step renews the dispatch lease.
func TestSubmitStepRenewsLease(t *testing.T) {
	ms := memstore.NewStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  5,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      1 * time.Second,
		DispatchLeaseTimeout: 50 * time.Millisecond,
	}
	k := kernel.New(cfg, kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
	})
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.RunDispatches(ctx, 5); err != nil {
		t.Fatal(err)
	}
	did := dispatchOf(t, k, exec.ID)

	// Submit a step — this renews the lease via TouchDispatch.
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{"path":"/tmp"}`), DispatchID: did,
	}); err != nil {
		t.Fatal(err)
	}

	// ReclaimStalled must not reclaim the dispatch: the step submission
	// renewed locked_at.
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now, cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatalf("step submission must renew the lease, but %d dispatches were reclaimed", len(reclaimed))
	}
}
