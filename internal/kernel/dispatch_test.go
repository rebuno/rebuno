package kernel_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func approvalPolicy() *policy.RuleEngine {
	pe, _ := policy.NewRuleEngine(policy.Config{
		Rules: []policy.Rule{{
			ID:   "approve-fs-write",
			When: policy.Condition{Target: "fs_write"},
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// First delivery: the webhook returns 200. The dispatch must stay
	// in_flight (not be acked) so the lease can expire and be reclaimed.
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("expected 1 delivery, got %d", called)
	}
	did := leaseOf(t, k, exec.ID)
	d, err := ms.GetDispatch(ctx, did.DispatchID)
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
	if len(reclaimed) != 1 || reclaimed[0].ID != did.DispatchID {
		t.Fatalf("expected the in_flight dispatch to be reclaimed, got %d", len(reclaimed))
	}

	// The drain loop re-delivers under the same dispatch id.
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if called != 2 {
		t.Fatalf("expected re-delivery after reclaim, got %d deliveries", called)
	}
	d, err = ms.GetDispatch(ctx, did.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("re-delivered dispatch must stay in_flight, got %s", d.Status)
	}

	// The stalled first attempt is fenced out: its lease named attempt 1, and
	// the re-delivery moved the dispatch to attempt 2.
	args := json.RawMessage(`{"path":"/tmp"}`)
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, Lease: did,
	}); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled attempt must be refused, got %v", err)
	}

	// The re-delivered agent replays into handleExistingStep: the step is
	// still executing (no completion recorded), so a safe_to_retry step
	// proceeds and an at_most_once step resolves as indeterminate.
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: args, Lease: leaseOf(t, k, exec.ID),
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}

	// Heartbeat renews the lease.
	if err := k.Heartbeat(ctx, exec.ID, leaseOf(t, k, exec.ID)); err != nil {
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	did := leaseOf(t, k, exec.ID)

	if err := k.CompleteExecution(ctx, exec.ID, did, json.RawMessage(`{"done":true}`)); err != nil {
		t.Fatal(err)
	}
	d, err := ms.GetDispatch(ctx, did.DispatchID)
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalPolicy()}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Deliver, then submit a step that requires approval.
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	did := leaseOf(t, k, exec.ID)
	dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "fs_write", Args: json.RawMessage(`{"path":"/etc"}`), Lease: did,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "blocked" {
		t.Fatalf("expected blocked, got %s", dec.Decision)
	}

	// The dispatch must be released (exhausted), not left in_flight.
	d, err := ms.GetDispatch(ctx, did.DispatchID)
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	// Attempt 1: deliver succeeds, dispatch stays in_flight.
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	// Let the lease expire and reclaim, then re-deliver (attempt 2).
	time.Sleep(20 * time.Millisecond)
	if _, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10); err != nil {
		t.Fatal(err)
	}
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	// Attempt 3 exceeds MaxAttempts: deliver must fail the execution rather
	// than re-deliver.
	time.Sleep(20 * time.Millisecond)
	if _, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10); err != nil {
		t.Fatal(err)
	}
	if err := k.DrainDispatches(ctx); err != nil {
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	did := leaseOf(t, k, exec.ID)

	// Submit a step — this renews the lease.
	if _, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: "read", Args: json.RawMessage(`{"path":"/tmp"}`), Lease: did,
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

// TestApprovedAtMostOnceStepIsNotRerunAfterCrash approves a step, resumes it,
// then re-delivers the dispatch with no result recorded: the at_most_once step
// must resolve as indeterminate rather than proceed a second time.
func TestApprovedAtMostOnceStepIsNotRerunAfterCrash(t *testing.T) {
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{Policy: approvalPolicy()}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	submit := func(did domain.Lease) domain.StepDecision {
		t.Helper()
		dec, err := k.SubmitStep(ctx, exec.ID, kernel.SubmitStepRequest{
			Kind:        domain.StepKindTool,
			Target:      "fs_write",
			Args:        json.RawMessage(`{"path":"/etc"}`),
			Idempotency: "at_most_once",
			Lease:       did,
		})
		if err != nil {
			t.Fatal(err)
		}
		return dec
	}

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	dec := submit(leaseOf(t, k, exec.ID))
	if dec.Decision != "blocked" {
		t.Fatalf("expected blocked, got %s", dec.Decision)
	}

	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if dec := submit(leaseOf(t, k, exec.ID)); dec.Decision != "proceed" {
		t.Fatalf("approved step must proceed on resume, got %s", dec.Decision)
	}

	// No result is recorded. Reclaiming and re-delivering the dispatch gives it
	// a fresh occurrence namespace, so the same effect maps to the same step.
	reclaimed, err := ms.ReclaimStalled(ctx, time.Now().UTC(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("expected the in_flight dispatch to be reclaimed, got %d", len(reclaimed))
	}
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}

	dec = submit(leaseOf(t, k, exec.ID))
	if dec.Decision != "replay" {
		t.Fatalf("approved at_most_once step must not run again, got %s", dec.Decision)
	}
	if got := string(dec.Error); !strings.Contains(got, "indeterminate") {
		t.Fatalf("expected an indeterminate error, got %s", got)
	}
}

// supersede stalls the execution's live dispatch and re-delivers it, the way a
// reaper and the dispatch loop would, and returns the lease of the attempt that
// replaced it.
func supersede(t *testing.T, k *kernel.Kernel, execID uuid.UUID) domain.Lease {
	t.Helper()
	ctx := context.Background()
	q := k.Deps().Queue
	later := time.Now().UTC().Add(time.Hour)
	if _, err := q.ReclaimStalled(ctx, later, time.Minute, 10); err != nil {
		t.Fatalf("reclaim stalled: %v", err)
	}
	if _, err := q.Claim(ctx, "replica-2", 10, later); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	d := newestDispatch(t, k, execID)
	if d.Status != domain.DispatchInFlight {
		t.Fatalf("re-delivered dispatch must be in flight, got %s", d.Status)
	}
	return domain.Lease{DispatchID: d.ID, Attempt: d.Attempt}
}

func toolStep(target, args string, lease domain.Lease) kernel.SubmitStepRequest {
	return kernel.SubmitStepRequest{
		Kind:   domain.StepKindTool,
		Target: target,
		Args:   json.RawMessage(args),
		Lease:  lease,
	}
}

// A stalled agent that wakes up after its dispatch was reclaimed must be told to
// stop, and must leave the occurrence counter the live attempt replays against
// where it is.
func TestSupersededSubmitIsRefusedAndLeavesCounterAlone(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)

	const args = `{"path":"/tmp"}`
	first, err := k.SubmitStep(ctx, exec.ID, toolStep("read", args, stalled))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CompleteStep(ctx, first.StepID, kernel.CompleteStepRequest{
		Result: json.RawMessage(`{"n":1}`), Lease: stalled,
	}); err != nil {
		t.Fatal(err)
	}

	live := supersede(t, k, exec.ID)

	// The live attempt replays the recorded call from occurrence zero.
	replayed, err := k.SubmitStep(ctx, exec.ID, toolStep("read", args, live))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Decision != "replay" || replayed.StepID != first.StepID {
		t.Fatalf("live attempt must replay %s, got %+v", first.StepID, replayed)
	}

	// The stalled attempt's next identical call is refused outright.
	if _, err := k.SubmitStep(ctx, exec.ID, toolStep("read", args, stalled)); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled attempt must be refused, got %v", err)
	}

	// And it consumed no occurrence, so the live attempt's next identical call
	// is still occurrence 1 and runs for real exactly once.
	next, err := k.SubmitStep(ctx, exec.ID, toolStep("read", args, live))
	if err != nil {
		t.Fatal(err)
	}
	if next.Decision != "proceed" {
		t.Fatalf("live attempt's second identical call must proceed, got %+v", next)
	}
	step, err := k.GetStep(ctx, next.StepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Occurrence != 1 {
		t.Fatalf("expected occurrence 1, got %d", step.Occurrence)
	}
}

func TestSupersededHeartbeatIsRefused(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)
	live := supersede(t, k, exec.ID)

	if err := k.Heartbeat(ctx, exec.ID, stalled); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled heartbeat must be refused, got %v", err)
	}
	if err := k.Heartbeat(ctx, exec.ID, live); err != nil {
		t.Fatalf("live heartbeat must renew, got %v", err)
	}
}

func TestSupersededStepCompletionIsRefused(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)

	dec, err := k.SubmitStep(ctx, exec.ID, toolStep("read", `{"path":"/tmp"}`, stalled))
	if err != nil {
		t.Fatal(err)
	}
	supersede(t, k, exec.ID)

	if _, err := k.CompleteStep(ctx, dec.StepID, kernel.CompleteStepRequest{
		Result: json.RawMessage(`{"stale":true}`), Lease: stalled,
	}); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled completion must be refused, got %v", err)
	}
	step, err := k.GetStep(ctx, dec.StepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepExecuting {
		t.Fatalf("refused completion must leave the step executing, got %s", step.Status)
	}

	if _, err := k.FailStep(ctx, dec.StepID, kernel.FailStepRequest{
		Error: json.RawMessage(`{"reason":"stale"}`), Lease: stalled,
	}); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled failure must be refused, got %v", err)
	}
}

func TestSupersededExecutionTerminalsAreRefused(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)
	live := supersede(t, k, exec.ID)

	if err := k.CompleteExecution(ctx, exec.ID, stalled, json.RawMessage(`{"stale":true}`)); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled completion must be refused, got %v", err)
	}
	if err := k.FailExecution(ctx, exec.ID, stalled, "stale"); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("stalled failure must be refused, got %v", err)
	}
	got, err := k.GetExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ExecutionRunning {
		t.Fatalf("execution must still be running, got %s", got.Status)
	}

	if err := k.CompleteExecution(ctx, exec.ID, live, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("live attempt must complete the execution, got %v", err)
	}
}

// A result recorded after an approval released the lease is still this
// attempt's to record.
func TestCompletionAfterLeaseReleaseStillRecords(t *testing.T) {
	ms := memstore.NewStore()
	pe, err := policy.NewRuleEngine(policy.Config{Rules: []policy.Rule{
		{ID: "allow-read", When: policy.Condition{Target: "read"}, Then: domain.PolicyResult{Decision: domain.DecisionAllow}},
		{ID: "approve-fs-write", When: policy.Condition{Target: "fs_write"}, Then: domain.PolicyResult{
			Decision:       domain.DecisionRequireApproval,
			ApprovalConfig: domain.PolicyApprovalConfig{Timeout: time.Hour, Message: "approve write"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	k := kernel.New(kernel.Config{ReplicaID: "test", DispatchBaseDelay: time.Millisecond},
		memDeps(ms, kernel.Deps{Policy: pe}))
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	lease := leaseOf(t, k, exec.ID)

	// This target proceeds; the next one blocks on approval and releases the lease.
	dec, err2 := k.SubmitStep(ctx, exec.ID, toolStep("read", `{"path":"/tmp"}`, lease))
	if err2 != nil {
		t.Fatal(err2)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %+v", dec)
	}
	blocked, err := k.SubmitStep(ctx, exec.ID, toolStep("fs_write", `{"path":"/etc"}`, lease))
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Decision != "blocked" {
		t.Fatalf("expected blocked, got %+v", blocked)
	}
	d := newestDispatch(t, k, exec.ID)
	if d.Status == domain.DispatchInFlight {
		t.Fatal("an approval block must release the lease")
	}

	if _, err := k.CompleteStep(ctx, dec.StepID, kernel.CompleteStepRequest{
		Result: json.RawMessage(`{"n":1}`), Lease: lease,
	}); err != nil {
		t.Fatalf("a released lease must still record its own result: %v", err)
	}
	step, err := k.GetStep(ctx, dec.StepID)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected succeeded, got %s", step.Status)
	}
}

// reclaim returns the execution's dispatch to the queue the way the reaper does,
// with no claim behind it. The attempt does not move until something claims the
// row, so an agent still running holds a lease whose attempt number is current.
func reclaim(t *testing.T, k *kernel.Kernel, execID uuid.UUID) {
	t.Helper()
	if _, err := k.Deps().Queue.ReclaimStalled(
		context.Background(), time.Now().UTC().Add(time.Hour), time.Minute, 10,
	); err != nil {
		t.Fatalf("reclaim stalled: %v", err)
	}
	if d := newestDispatch(t, k, execID); d.Status != domain.DispatchPending {
		t.Fatalf("dispatch must be back in the queue, got %s", d.Status)
	}
}

func TestReclaimedDispatchCannotStartNewWork(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)
	reclaim(t, k, exec.ID)

	if _, err := k.SubmitStep(ctx, exec.ID, toolStep("read", `{"path":"/tmp"}`, stalled)); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("a reclaimed dispatch must not start an effect, got %v", err)
	}
	steps, err := k.ListSteps(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("a refused submit must record no step, got %d", len(steps))
	}
}

// The branch that re-runs an orphaned safe_to_retry step writes nothing before
// answering proceed, so it needs its own check.
func TestReclaimedDispatchCannotResumeAnExecutingStep(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	stalled := leaseOf(t, k, exec.ID)

	req := toolStep("read", `{"path":"/tmp"}`, stalled)
	dec, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Decision != "proceed" {
		t.Fatalf("expected proceed, got %+v", dec)
	}

	reclaim(t, k, exec.ID)
	if _, err := k.SubmitStep(ctx, exec.ID, req); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("a reclaimed dispatch must not re-run an executing step, got %v", err)
	}
}

// A rate-limit refusal writes no step but does append an event, so it is fenced
// like the rest of the submit path.
func TestReclaimedDispatchCannotRecordARateLimitRefusal(t *testing.T) {
	k, ctx, exec := rateLimitedKernel(t, domain.RateLimitConfig{
		MaxCalls: 1, Window: time.Hour, PerWhat: "execution", MaxWait: time.Minute,
	})
	stalled := leaseOf(t, k, exec.ID)
	if dec := submitRead(t, k, ctx, exec.ID); dec.Decision != "proceed" {
		t.Fatalf("first call must proceed, got %+v", dec)
	}

	reclaim(t, k, exec.ID)
	if _, err := k.SubmitStep(ctx, exec.ID, toolStep("read", `{"path":"/other"}`, stalled)); !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("a reclaimed dispatch must not record a refusal, got %v", err)
	}
	if evts := dispatchEvents(t, k, exec.ID, domain.EventStepRateLimited); len(evts) != 0 {
		t.Fatalf("a refused submit must append no event, got %d", len(evts))
	}
}

// reclaimAfterEntryCheck runs the reaper between a submit's entry check and the
// transaction that records its decision. Only the kernel's own queue is wrapped,
// so the check inside that transaction sees the reclaimed row.
type reclaimAfterEntryCheck struct {
	store.JobQueue
	renews int
}

func (q *reclaimAfterEntryCheck) RenewLease(ctx context.Context, execID uuid.UUID, lease domain.Lease, now time.Time) error {
	if err := q.JobQueue.RenewLease(ctx, execID, lease, now); err != nil {
		return err
	}
	q.renews++
	if q.renews == 1 {
		_, err := q.ReclaimStalled(ctx, now.Add(time.Hour), time.Minute, 10)
		return err
	}
	return nil
}

func TestSubmitIsFencedAfterItsEntryCheck(t *testing.T) {
	ms := memstore.NewStore()
	q := &reclaimAfterEntryCheck{JobQueue: ms}
	k := kernel.New(
		kernel.Config{ReplicaID: "test", DispatchBaseDelay: time.Millisecond},
		memDeps(ms, kernel.Deps{Queue: q, Policy: policy.PermissiveEngine{}}),
	)
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: "http://localhost", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")
	lease := leaseOf(t, k, exec.ID)

	_, err := k.SubmitStep(ctx, exec.ID, toolStep("read", `{"path":"/tmp"}`, lease))
	if !errors.Is(err, domain.ErrLeaseSuperseded) {
		t.Fatalf("a submit whose dispatch was reclaimed mid-flight must be refused, got %v", err)
	}
	// The wrapper reclaims only after a successful entry check, so a refusal
	// here came from the write path.
	if q.renews != 1 {
		t.Fatalf("the entry check should have passed before the reclaim, got %d renews", q.renews)
	}
	steps, err := k.ListSteps(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("a refused submit must record no step, got %d", len(steps))
	}
}

// The agent accepts the webhook and parks the work on an approval, which retires
// the dispatch — and then its HTTP response is lost. The dispatcher sees the
// final attempt fail, but exhaustion is that delivery's verdict, and the
// delivery no longer owns the dispatch: the execution must stay blocked, waiting
// on the approval it raised.
func TestLostAckOnTheFinalAttemptCannotFailAParkedExecution(t *testing.T) {
	ms := memstore.NewStore()
	var k *kernel.Kernel
	ctx := context.Background()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ExecutionID     string `json:"execution_id"`
			DispatchID      string `json:"dispatch_id"`
			DispatchAttempt int    `json:"dispatch_attempt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			return
		}
		dec, err := k.SubmitStep(ctx, uuid.MustParse(p.ExecutionID), toolStep(
			"fs_write", `{"path":"/etc"}`,
			domain.Lease{DispatchID: uuid.MustParse(p.DispatchID), Attempt: p.DispatchAttempt},
		))
		if err != nil {
			t.Error(err)
			return
		}
		if dec.Decision != "blocked" {
			t.Errorf("expected the step to block on approval, got %+v", dec)
		}
		// The agent parked cleanly, but its ack never reaches the kernel.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	k = kernel.New(kernel.Config{
		ReplicaID:           "test",
		DispatchMaxAttempts: 1, // the first delivery is also the last
		DispatchBaseDelay:   time.Millisecond,
		DispatchTimeout:     time.Second,
	}, memDeps(ms, kernel.Deps{Policy: approvalPolicy()}))
	if err := k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`), "")

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := k.GetExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ExecutionBlocked {
		t.Fatalf("execution must still be blocked on its approval, got %s (%s)", got.Status, got.FailureReason)
	}
	pending, err := k.Deps().Approvals.ListPendingApprovalsByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("the approval must still be pending, got %d", len(pending))
	}
}
