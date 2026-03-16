package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestSendSignal(t *testing.T) {
	k, events, _, _, _, signals := newTestKernel()
	ctx := context.Background()

	execID, _ := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})

	err := k.SendSignal(ctx, execID, "approval", json.RawMessage(`{"approved":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventSignalReceived {
			found = true
		}
	}
	if !found {
		t.Fatal("expected signal.received event")
	}

	pending, _ := signals.GetPending(ctx, execID)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending signal, got %d", len(pending))
	}
}

func TestSendSignalResumesBlockedExecution(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:       domain.IntentWait,
			SignalType: "approval",
		},
	})

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}

	err := k.SendSignal(ctx, execID, "approval", json.RawMessage(`{"ok":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ = k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after signal, got %s", state.Execution.Status)
	}
}

func TestSendSignalValidation(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	err := k.SendSignal(ctx, "", "type", nil)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for empty execution_id, got %v", err)
	}

	err = k.SendSignal(ctx, "exec-1", "", nil)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for empty signal_type, got %v", err)
	}
}

func TestSendSignalToTerminalExecution(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})
	k.CancelExecution(ctx, execID)

	err := k.SendSignal(ctx, execID, "approval", json.RawMessage(`{}`))
	if !errors.Is(err, domain.ErrTerminalExecution) {
		t.Fatalf("expected ErrTerminalExecution, got %v", err)
	}
}

func TestSendSignalNonMatchingTypeDoesNotResume(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Block on "approval" signal type.
	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentWait, SignalType: "approval"},
	})

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}

	// Send a signal with a different type — should NOT resume.
	err := k.SendSignal(ctx, execID, "other_signal", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ = k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected still blocked after non-matching signal, got %s", state.Execution.Status)
	}
}

// setupApprovalBlockedExecution creates an execution that is blocked waiting for approval
// on a step. Returns execID, sessionID, stepID.
func setupApprovalBlockedExecution(t *testing.T, k *Kernel, sessions *mockSessionStore) (string, string, string) {
	t.Helper()
	ctx := context.Background()

	// Use require-approval policy.
	k.policy = &mockPolicyEngine{
		decision: domain.PolicyRequireApproval,
		reason:   "requires human approval",
		ruleID:   "test-approval",
	}

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "dangerous.tool",
		},
	})
	if err != nil {
		t.Fatalf("process intent: %v", err)
	}
	if !result.PendingApproval {
		t.Fatal("expected PendingApproval=true")
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}
	if state.BlockedReason != "approval" {
		t.Fatalf("expected blocked reason approval, got %s", state.BlockedReason)
	}

	return execID, sessionID, result.StepID
}

func TestApprovalGrantedResumesExecution(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _, stepID := setupApprovalBlockedExecution(t, k, sessions)

	payload := json.RawMessage(`{"step_id":"` + stepID + `","approved":true}`)
	err := k.SendSignal(ctx, execID, "step.approve", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after approval granted, got %s", state.Execution.Status)
	}
}

func TestApprovalDeniedFailsStepAndResumesExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _, stepID := setupApprovalBlockedExecution(t, k, sessions)

	payload := json.RawMessage(`{"step_id":"` + stepID + `","approved":false,"reason":"too risky"}`)
	err := k.SendSignal(ctx, execID, "step.approve", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after approval denied, got %s", state.Execution.Status)
	}

	step := state.Steps[stepID]
	if step == nil {
		t.Fatal("step not found in state")
	}
	if step.Status != domain.StepFailed {
		t.Fatalf("expected step failed after denial, got %s", step.Status)
	}

	// Verify step.failed event was emitted.
	foundStepFailed := false
	foundResumed := false
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventStepFailed {
			foundStepFailed = true
		}
		if evt.Type == domain.EventExecutionResumed {
			foundResumed = true
		}
	}
	if !foundStepFailed {
		t.Fatal("expected step.failed event after denial")
	}
	if !foundResumed {
		t.Fatal("expected execution.resumed event after denial")
	}
}

func TestApprovalStepIDMismatch(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _, _ := setupApprovalBlockedExecution(t, k, sessions)

	// Send approval with wrong step_id.
	payload := json.RawMessage(`{"step_id":"wrong-step-id","approved":true}`)
	err := k.SendSignal(ctx, execID, "step.approve", payload)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for step_id mismatch, got %v", err)
	}

	// Execution should remain blocked.
	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected still blocked after mismatched step_id, got %s", state.Execution.Status)
	}
}

func TestApprovalMalformedPayload(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _, _ := setupApprovalBlockedExecution(t, k, sessions)

	// Invalid JSON in a json.RawMessage fails at the EmitEvent stage (marshaling
	// signal.received), which prevents the approval handler from being reached.
	// This test verifies that such a signal is rejected with an error.
	payload := json.RawMessage(`{not valid json}`)
	err := k.SendSignal(ctx, execID, "step.approve", payload)
	if err == nil {
		t.Fatal("expected error for malformed approval payload")
	}
	// The error occurs during event emission because json.RawMessage validates on marshal.
	if !strings.Contains(err.Error(), "marshaling event payload") && !strings.Contains(err.Error(), "parsing approval payload") {
		t.Fatalf("expected marshaling or parsing error, got: %v", err)
	}

	// Execution should remain blocked since the signal was rejected.
	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected still blocked after malformed payload, got %s", state.Execution.Status)
	}
}
