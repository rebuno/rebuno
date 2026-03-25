package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

// makeTaintedExecution sets up a running execution and then injects a sequence gap
// in the event store so the projector marks the state as tainted.
func makeTaintedExecution(t *testing.T, k *Kernel, events *mockEventStore, sessions *mockSessionStore) (string, string) {
	t.Helper()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Inject a gap: manually append an event with a sequence number that skips one.
	// The mock event store auto-assigns sequential numbers, so we need to manipulate
	// the stored events directly to create a gap.
	events.mu.Lock()
	evts := events.events[execID]
	if len(evts) > 0 {
		// Bump the last event's sequence to create a gap.
		// e.g., if last seq is 3, set it to 5 so the projector sees a gap (expected 4, got 5).
		evts[len(evts)-1].Sequence = evts[len(evts)-1].Sequence + 1
		events.events[execID] = evts
	}
	events.mu.Unlock()

	// Verify the execution is now tainted.
	state, err := k.projector.Project(ctx, execID)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if !state.Tainted {
		t.Fatal("expected execution to be tainted after sequence gap injection")
	}

	return execID, sessionID
}

func setupRunningExecution(t *testing.T, k *Kernel, sessions *mockSessionStore) (string, string) {
	t.Helper()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	sess, found, _ := sessions.GetByExecution(ctx, execID)
	if found {
		return execID, sess.ID
	}

	sessionID := "test-session-" + execID[:8]
	sessions.Create(ctx, domain.Session{
		ID:          sessionID,
		ExecutionID: execID,
		AgentID:     "agent-1",
		ConsumerID:  "consumer-1",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(2 * time.Minute),
	})

	// Emit execution.started so the projector sees the running status.
	_, err = k.EmitEvent(ctx, execID, "", domain.EventExecutionStarted, nil, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatalf("emit execution.started: %v", err)
	}
	k.events.UpdateExecutionStatus(ctx, execID, domain.ExecutionRunning)

	return execID, sessionID
}

func TestProcessIntentComplete(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentComplete,
			Output: json.RawMessage(`{"answer":"42"}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected intent to be accepted")
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionCompleted {
		t.Fatalf("expected completed, got %s", state.Execution.Status)
	}
}

func TestProcessIntentFail(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:  domain.IntentFail,
			Error: "something went wrong",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected intent to be accepted")
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionFailed {
		t.Fatalf("expected failed, got %s", state.Execution.Status)
	}
}

func TestProcessIntentInvokeTool(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:      domain.IntentInvokeTool,
			ToolID:    "web.search",
			Arguments: json.RawMessage(`{"query":"test"}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got error: %s", result.Error)
	}
	if result.StepID == "" {
		t.Fatal("expected step_id")
	}

	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 0 {
		t.Fatalf("expected 0 dispatched for local tool, got %d", dispatched)
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running, got %s", state.Execution.Status)
	}
	if state.CurrentStep == nil {
		t.Fatal("expected CurrentStep to be set")
	}
	if state.CurrentStep.Status.IsTerminal() {
		t.Fatal("expected CurrentStep to be non-terminal")
	}
}

func TestProcessIntentInvokeToolRemote(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
			Remote: true,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got error: %s", result.Error)
	}

	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 1 {
		t.Fatalf("expected 1 dispatched for remote tool, got %d", dispatched)
	}
}

func TestProcessIntentInvokeToolDeniedByPolicy(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	k.policy = newDenyAllPolicy()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Accepted {
		t.Fatal("expected intent to be denied")
	}
	if result.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestProcessIntentWait(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:       domain.IntentWait,
			SignalType: "human_approval",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected accepted")
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}
}

func TestProcessIntentSessionNotFound(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: "exec-1",
		SessionID:   "nonexistent",
		Intent:      domain.Intent{Type: domain.IntentComplete},
	})
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestProcessIntentSessionExpired(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	sessions.Create(ctx, domain.Session{
		ID:          "session-1",
		ExecutionID: "exec-1",
		ExpiresAt:   time.Now().Add(-time.Hour),
	})

	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: "exec-1",
		SessionID:   "session-1",
		Intent:      domain.Intent{Type: domain.IntentComplete},
	})
	if !errors.Is(err, domain.ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
}

func TestValidateIntent(t *testing.T) {
	tests := []struct {
		name    string
		intent  domain.Intent
		wantErr bool
	}{
		{
			name:   "valid invoke_tool",
			intent: domain.Intent{Type: domain.IntentInvokeTool, ToolID: "web.search"},
		},
		{
			name:    "invoke_tool missing tool_id",
			intent:  domain.Intent{Type: domain.IntentInvokeTool},
			wantErr: true,
		},
		{
			name:   "valid complete",
			intent: domain.Intent{Type: domain.IntentComplete},
		},
		{
			name:   "valid fail",
			intent: domain.Intent{Type: domain.IntentFail, Error: "something broke"},
		},
		{
			name:    "fail missing error",
			intent:  domain.Intent{Type: domain.IntentFail},
			wantErr: true,
		},
		{
			name:   "valid wait",
			intent: domain.Intent{Type: domain.IntentWait, SignalType: "approval"},
		},
		{
			name:    "wait missing signal_type",
			intent:  domain.Intent{Type: domain.IntentWait},
			wantErr: true,
		},
		{
			name:    "unknown intent type",
			intent:  domain.Intent{Type: "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateIntent(tt.intent)
			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestProcessIntentOnTerminalExecution(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Complete the execution first.
	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentComplete, Output: json.RawMessage(`{}`)},
	})

	// Attempting another intent on a terminal execution should fail.
	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentComplete},
	})
	if !errors.Is(err, domain.ErrTerminalExecution) {
		t.Fatalf("expected ErrTerminalExecution, got %v", err)
	}
}

func TestProcessIntentSessionExecutionMismatch(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID1, _ := setupRunningExecution(t, k, sessions)

	// Create a second execution with its own session.
	execID2, _ := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-2"})
	sessionID2 := "session-for-exec2"
	sessions.Create(ctx, domain.Session{
		ID:          sessionID2,
		ExecutionID: execID2,
		AgentID:     "agent-2",
		ConsumerID:  "consumer-2",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(2 * time.Minute),
	})

	// Try to use session for exec2 against exec1. Should fail validation.
	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID1,
		SessionID:   sessionID2,
		Intent:      domain.Intent{Type: domain.IntentComplete},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for session-execution mismatch, got %v", err)
	}
}

func TestProcessIntentConcurrentStepConflict(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Invoke a local tool (step stays non-terminal until result is submitted).
	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "tool-a",
		},
	})
	if err != nil {
		t.Fatalf("first invoke: %v", err)
	}

	// Attempting a second tool invoke while the first step is still active should fail.
	_, err = k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "tool-b",
		},
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict for concurrent step, got %v", err)
	}
}

func TestProcessIntentRejectsTaintedExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := makeTaintedExecution(t, k, events, sessions)

	_, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentComplete},
	})
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}
}

func TestProcessIntentPopulatesStepCountAndDuration(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	captor := &capturingPolicyEngine{
		result: domain.PolicyResult{Decision: domain.PolicyAllow, RuleID: "test"},
	}
	k.policy = captor

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got error: %s", result.Error)
	}

	captor.mu.Lock()
	captured := captor.last
	captor.mu.Unlock()

	// StepCount should be 0 for fresh execution (no prior steps)
	if captured.StepCount != 0 {
		t.Errorf("expected step_count=0 for fresh execution, got %d", captured.StepCount)
	}

	// DurationMs should be positive (execution was just created)
	if captured.DurationMs < 0 {
		t.Errorf("expected non-negative duration_ms, got %d", captured.DurationMs)
	}
}

func TestProcessIntentRateLimited(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	// Use a policy that allows but with a rate limit of 2 per minute
	k.policy = &mockRateLimitPolicy{
		decision:  domain.PolicyAllow,
		rateLimit: &domain.RateLimitConfig{Max: 2, Window: "1m"},
	}

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// First two calls should succeed
	for i := 0; i < 2; i++ {
		result, err := k.ProcessIntent(ctx, domain.IntentRequest{
			ExecutionID: execID,
			SessionID:   sessionID,
			Intent: domain.Intent{
				Type:   domain.IntentInvokeTool,
				ToolID: "web.search",
			},
		})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if !result.Accepted {
			t.Fatalf("call %d: expected accepted, got error: %s", i+1, result.Error)
		}

		// Complete the step so we can invoke another
		if result.StepID != "" {
			k.SubmitStepResult(ctx, StepResultRequest{
				ExecutionID: execID,
				StepID:      result.StepID,
				SessionID:   sessionID,
				Success:     true,
				Data:        json.RawMessage(`{"ok":true}`),
			})
		}
	}

	// Third call should be rate limited
	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Accepted {
		t.Fatal("expected rate-limited denial")
	}
	if result.Error == "" {
		t.Fatal("expected error message about rate limit")
	}
}

func TestProcessIntentRequireApprovalPolicy(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Switch to require-approval policy.
	k.policy = &mockPolicyEngine{
		decision: domain.PolicyRequireApproval,
		reason:   "needs human review",
		ruleID:   "test-require-approval",
	}

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:      domain.IntentInvokeTool,
			ToolID:    "dangerous.tool",
			Arguments: json.RawMessage(`{"target":"production"}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Accepted {
		t.Fatal("expected intent to be accepted")
	}
	if !result.PendingApproval {
		t.Fatal("expected PendingApproval=true")
	}
	if result.StepID == "" {
		t.Fatal("expected step_id to be set")
	}

	// Verify step was created with approval_required status event.
	foundStepCreated := false
	foundApprovalRequired := false
	foundBlocked := false
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventStepCreated {
			foundStepCreated = true
		}
		if evt.Type == domain.EventStepApprovalRequired {
			foundApprovalRequired = true
		}
		if evt.Type == domain.EventExecutionBlocked {
			foundBlocked = true
		}
	}
	if !foundStepCreated {
		t.Fatal("expected step.created event")
	}
	if !foundApprovalRequired {
		t.Fatal("expected step.approval_required event")
	}
	if !foundBlocked {
		t.Fatal("expected execution.blocked event")
	}

	// Verify execution is blocked.
	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}
	if state.BlockedReason != "approval" {
		t.Fatalf("expected blocked reason approval, got %s", state.BlockedReason)
	}
	if state.BlockedRef != result.StepID {
		t.Fatalf("expected blocked ref %s, got %s", result.StepID, state.BlockedRef)
	}
}
