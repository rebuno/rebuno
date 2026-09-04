package kernel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/dispatcher"
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	// A 200 must leave the dispatch in_flight, so the lease can expire.
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

	// No heartbeat, so the lease expires and the row goes back to pending.
	time.Sleep(20 * time.Millisecond)
	reclaimed, err := ms.ReclaimStalled(ctx, time.Now().UTC(), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != did.DispatchID {
		t.Fatalf("expected the in_flight dispatch to be reclaimed, got %d", len(reclaimed))
	}

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

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
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now.Add(time.Hour), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatal("exhausted dispatch must not be reclaimed")
	}
}

// Without the release, redelivery every 2 minutes hits dispatch_exhausted
// before the approval timeout.
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

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

	d, err := ms.GetDispatch(ctx, did.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.DispatchExhausted {
		t.Fatalf("approval block must release the lease (exhausted), got %s", d.Status)
	}
	now := time.Now().UTC()
	reclaimed, err := ms.ReclaimStalled(ctx, now.Add(time.Hour), cfg.DispatchLeaseTimeout, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 0 {
		t.Fatal("approval-blocked dispatch must not be reclaimed")
	}
	if err := k.GrantApproval(ctx, *dec.ApprovalID, kernel.GrantApprovalRequest{DecidedBy: "alice"}); err != nil {
		t.Fatal(err)
	}
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

// The success branch has no MaxAttempts guard of its own, so a webhook that
// always 200s but never completes would redeliver forever.
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	did := leaseOf(t, k, exec.ID)

	// Submitting a step renews the lease.
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
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
// the dispatch, and then its HTTP response is lost. The dispatcher sees the
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
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

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

func TestDispatcherDeliveryAndRetry(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	var lastBody []byte
	var lastSig string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		lastBody, _ = io.ReadAll(r.Body)
		lastSig = r.Header.Get("Rebuno-Signature")
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	// Wait for queue-level exponential backoff.
	time.Sleep(5 * time.Millisecond)
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if called < 2 {
		t.Fatalf("expected retries, called %d", called)
	}
	if lastSig == "" {
		t.Fatal("missing signature")
	}
	if bytes.Contains(lastBody, []byte(`"signature"`)) {
		t.Fatal("signature must not be part of the request body")
	}
	exec, _ = k.GetExecution(ctx, exec.ID)
	if exec.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after ack, got %s", exec.Status)
	}
}

// Regression: an agent 4xx acked the dispatch 'failed' with a NULL
// next_attempt_at, stranding it: never retried, never exhausted.
func TestDispatchRejectionExhaustsAndFails(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusBadRequest) // persistent 4xx
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 2, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	// Attempt 1 (fails, schedules retry), then attempt 2 (hits max, exhausts).
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := k.DrainDispatches(ctx); err != nil {
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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: hung.URL, Secret: "secret"})
	_, _ = k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	start := time.Now()
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("delivery was not bounded by DispatchTimeout, took %v", elapsed)
	}
}

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
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: slow.URL, Secret: "secret"})
	for i := 0; i < n; i++ {
		_, _ = k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	}

	start := time.Now()
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	// Serial delivery would take >= n*perCall (480ms). Concurrent delivery should
	// finish in roughly one perCall window; allow generous slack for CI jitter.
	if elapsed >= n*perCall/2 {
		t.Fatalf("deliveries did not run concurrently: took %v for %d jobs of %v each", elapsed, n, perCall)
	}
}

func TestDispatchNeverExceedsConcurrency(t *testing.T) {
	ms := memstore.NewStore()
	const concurrency = 4
	var live, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := live.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		live.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	k := kernel.New(kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  3,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      5 * time.Second,
		DispatchLeaseTimeout: time.Minute,
		DispatchConcurrency:  concurrency,
	}, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: srv.URL, Secret: "secret"})
	for i := 0; i < concurrency*10; i++ {
		if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if got := peak.Load(); got > concurrency {
		t.Fatalf("%d deliveries ran at once with a pool of %d", got, concurrency)
	}
}

func TestDispatchLoopClaimsWhileBusy(t *testing.T) {
	ms := memstore.NewStore()
	var mu sync.Mutex
	var slowSeen bool
	var delivered atomic.Int64
	slowStarted := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isSlow := !slowSeen
		slowSeen = true
		mu.Unlock()
		if isSlow {
			close(slowStarted)
			<-release
		}
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	k := kernel.New(kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  3,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      5 * time.Second,
		DispatchLeaseTimeout: time.Minute,
		DispatchConcurrency:  4,
	}, memDeps(ms, kernel.Deps{}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: srv.URL, Secret: "secret"})
	if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- k.RunDispatcher(ctx) }()

	<-slowStarted
	const later = 5
	for i := 0; i < later; i++ {
		if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for delivered.Load() < later && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := delivered.Load()
	close(release)
	cancel()
	<-done
	if got != later {
		t.Fatalf("delivered %d of %d dispatches enqueued while a worker was busy", got, later)
	}
}

func TestReclaimDrainsStalledBacklog(t *testing.T) {
	ms := memstore.NewStore()
	var delivered atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	k := kernel.New(kernel.Config{
		ReplicaID:            "test",
		DispatchMaxAttempts:  5,
		DispatchBaseDelay:    1 * time.Millisecond,
		DispatchTimeout:      5 * time.Second,
		DispatchLeaseTimeout: time.Minute,
		DispatchConcurrency:  4,
	}, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: srv.URL, Secret: "secret"})

	// Must exceed the reclaim query's page size for the drain to matter.
	const stranded = 250
	dead, expired := "dead-replica", time.Now().UTC().Add(-time.Hour)
	for i := 0; i < stranded; i++ {
		exec, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		ds, err := ms.ListDispatchesByExecution(ctx, exec.ID)
		if err != nil || len(ds) != 1 {
			t.Fatalf("dispatches for %s: %v, %v", exec.ID, ds, err)
		}
		d := ds[0]
		d.Status, d.LockedBy, d.LockedAt = domain.DispatchInFlight, &dead, &expired
		if err := ms.Enqueue(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if got := delivered.Load(); got != stranded {
		t.Fatalf("recovered %d of %d stranded dispatches in one pass", got, stranded)
	}
}

// A dispatch reclaimed after a crashed attempt is redelivered under the SAME id
// (ReclaimStalled updates the row in place). The resumed agent replays from the
// top, so its occurrence counting has to restart too.
func TestReclaimedDispatchReplaysFromZero(t *testing.T) {
	k, ctx := setup(t)
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	q := k.Deps().Queue
	now := time.Now().UTC()

	claimed, err := q.Claim(ctx, "replica-1", 10, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v (n=%d)", err, len(claimed))
	}
	did := domain.Lease{DispatchID: claimed[0].ID, Attempt: claimed[0].Attempt}

	args := json.RawMessage(`{"path":"/tmp"}`)
	req := kernel.SubmitStepRequest{Kind: domain.StepKindTool, Target: "read", Args: args, Lease: did}
	first, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CompleteStep(ctx, first.StepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{"n":1}`), Lease: did}); err != nil {
		t.Fatal(err)
	}

	// Agent crashes; the lease expires and the dispatch is reclaimed and redelivered.
	if _, err := q.ReclaimStalled(ctx, now.Add(time.Hour), time.Minute, 10); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := q.Claim(ctx, "replica-2", 10, now.Add(time.Hour))
	if err != nil || len(reclaimed) != 1 {
		t.Fatalf("reclaim-claim: %v (n=%d)", err, len(reclaimed))
	}
	if reclaimed[0].ID != did.DispatchID {
		t.Fatalf("expected the same dispatch id on redelivery, got %s", reclaimed[0].ID)
	}

	req.Lease = domain.Lease{DispatchID: reclaimed[0].ID, Attempt: reclaimed[0].Attempt}
	resumed, err := k.SubmitStep(ctx, exec.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Decision != "replay" {
		t.Fatalf("redelivered dispatch must replay, got %s (would re-run the effect)", resumed.Decision)
	}
}

// dispatchEvents returns the events of the given type, oldest first.
func dispatchEvents(t *testing.T, k *kernel.Kernel, execID uuid.UUID, typ string) []map[string]any {
	t.Helper()
	events, err := k.GetEvents(context.Background(), execID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, ev := range events {
		if ev.Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal %s payload: %v", ev.Type, err)
		}
		out = append(out, payload)
	}
	return out
}

// dispatch.acked must report the dispatch row's own attempt.
func TestDispatchAckedRecordsRealAttempt(t *testing.T) {
	ms := memstore.NewStore()
	called := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if called < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 3, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}

	acked := dispatchEvents(t, k, exec.ID, domain.EventDispatchAcked)
	if len(acked) != 1 {
		t.Fatalf("expected one dispatch.acked, got %d", len(acked))
	}
	if got := acked[0]["attempt"]; got != float64(2) {
		t.Fatalf("dispatch.acked must report the second attempt, got %v", got)
	}
	queued := dispatchEvents(t, k, exec.ID, domain.EventDispatchQueued)
	if len(queued) != 1 || queued[0]["attempt"] != float64(0) {
		t.Fatalf("dispatch.queued must report attempt 0, got %v", queued)
	}
}

// Every delivery attempt is recorded, including the one that reaches
// max_attempts and fails the execution.
func TestFinalDispatchFailureIsRecorded(t *testing.T) {
	ms := memstore.NewStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 3, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))

	for i := 0; i < cfg.DispatchMaxAttempts; i++ {
		if err := k.DrainDispatches(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	failed := dispatchEvents(t, k, exec.ID, domain.EventDispatchFailed)
	if len(failed) != cfg.DispatchMaxAttempts {
		t.Fatalf("expected %d dispatch.failed events, got %d", cfg.DispatchMaxAttempts, len(failed))
	}
	for i, p := range failed {
		if got := p["attempt"]; got != float64(i+1) {
			t.Fatalf("dispatch.failed %d reports attempt %v", i, got)
		}
	}
	got, _ := k.GetExecution(ctx, exec.ID)
	if got.Status != domain.ExecutionFailed || got.FailureReason != "dispatch_exhausted" {
		t.Fatalf("expected dispatch_exhausted failure, got %s %q", got.Status, got.FailureReason)
	}
}

func TestReleasedDispatchRecordsNoEvent(t *testing.T) {
	ms := memstore.NewStore()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := kernel.Config{ReplicaID: "test", DispatchMaxAttempts: 3, DispatchBaseDelay: 1 * time.Millisecond, DispatchTimeout: 1 * time.Second}
	k := kernel.New(cfg, memDeps(ms, kernel.Deps{}))
	ctx := context.Background()
	_ = k.RegisterAgent(ctx, domain.Agent{ID: "agent-1", WebhookURL: ts.URL, Secret: "secret"})
	exec, _ := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{}`))
	if err := k.DrainDispatches(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.CompleteExecution(ctx, exec.ID, leaseOf(t, k, exec.ID), json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	if got := dispatchEvents(t, k, exec.ID, domain.EventDispatchDiscarded); len(got) != 0 {
		t.Fatalf("completing an execution must not record a dispatch event, got %v", got)
	}
	events, err := k.GetEvents(ctx, exec.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != domain.EventExecutionCompleted {
		t.Fatalf("execution.completed must be the final event, got %s", last.Type)
	}

	// The rows are still retired, so the drain loop cannot re-deliver them.
	dispatches, err := ms.ListDispatchesByExecution(ctx, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) == 0 {
		t.Fatal("expected at least one dispatch row")
	}
	for _, d := range dispatches {
		if d.Status != domain.DispatchExhausted {
			t.Fatalf("dispatch %s must be retired, got %s", d.ID, d.Status)
		}
	}
}
