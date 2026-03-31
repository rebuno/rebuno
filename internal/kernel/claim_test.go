package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestBuildClaimResult(t *testing.T) {
	k, events, _, _, sessions, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"query":"hello"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, found, _ := sessions.GetByExecution(ctx, execID)
	if !found {
		t.Fatal("expected session to be created")
	}
	if sess.AgentID != "agent-1" {
		t.Fatalf("expected agent-1, got %s", sess.AgentID)
	}
	if sess.ExecutionID != execID {
		t.Fatalf("expected execution_id %s, got %s", execID, sess.ExecutionID)
	}

	evts := events.events[execID]
	foundStarted := false
	for _, e := range evts {
		if e.Type == domain.EventExecutionStarted {
			foundStarted = true
			break
		}
	}
	if !foundStarted {
		t.Fatal("expected execution.started event")
	}
}

func TestBuildClaimResultRejectsNonPending(t *testing.T) {
	k, _, _, _, _, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"query":"hello"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = k.buildClaimResult(ctx, execID, "agent-2", "consumer-2")
	if err == nil {
		t.Fatal("expected error when claiming an already-running execution")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got: %v", err)
	}
}

func TestTryAssignExecutionNoConnection(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"query":"hello"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, found, _ := sessions.GetByExecution(ctx, execID)
	if found {
		t.Fatal("expected no session when no agent is connected")
	}

	evts := events.events[execID]
	for _, e := range evts {
		if e.Type == domain.EventExecutionStarted {
			t.Fatal("did not expect execution.started when no agent connected")
		}
	}
}

func TestHandleAgentDisconnect(t *testing.T) {
	k, events, _, _, sessions, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, found, _ := sessions.GetByExecution(ctx, execID)
	if !found {
		t.Fatal("expected session")
	}
	sessionID := sess.ID

	k.HandleAgentDisconnect(ctx, sessionID)

	_, found, _ = sessions.Get(ctx, sessionID)
	if found {
		t.Fatal("expected session to be deleted after disconnect")
	}

	summary, _ := events.GetExecution(ctx, execID)
	if summary.Status != domain.ExecutionPending {
		t.Fatalf("expected pending after disconnect, got %s", summary.Status)
	}

	evts := events.events[execID]
	foundTimeout := false
	for _, e := range evts {
		if e.Type == domain.EventAgentTimeout {
			foundTimeout = true
			break
		}
	}
	if !foundTimeout {
		t.Fatal("expected agent.timeout event")
	}
}

func TestHandleAgentDisconnectNonexistentSession(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	// Should be a no-op, no panic.
	k.HandleAgentDisconnect(ctx, "nonexistent-session")
}

func TestHandleAgentDisconnectTerminalExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sess, found, _ := sessions.GetByExecution(ctx, execID)
	if !found {
		t.Fatal("expected session")
	}

	// Cancel the execution to put it in terminal state.
	k.CancelExecution(ctx, execID)

	eventCountBefore := len(events.events[execID])

	k.HandleAgentDisconnect(ctx, sess.ID)

	// No execution.reset event should be emitted for a terminal execution.
	for _, e := range events.events[execID][eventCountBefore:] {
		if e.Type == domain.EventExecutionReset {
			t.Fatal("should not emit execution.reset for terminal execution")
		}
	}
}

func TestHandleAgentDisconnectBlockedExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Block the execution on a signal.
	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentWait, SignalType: "approval"},
	})

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked, got %s", state.Execution.Status)
	}

	k.HandleAgentDisconnect(ctx, sessionID)

	// Blocked execution should remain blocked (not reset to pending).
	summary, _ := events.GetExecution(ctx, execID)
	if summary.Status != domain.ExecutionBlocked {
		t.Fatalf("expected blocked after disconnect, got %s", summary.Status)
	}

	// Should still have agent.timeout event.
	foundTimeout := false
	for _, e := range events.events[execID] {
		if e.Type == domain.EventAgentTimeout {
			foundTimeout = true
			break
		}
	}
	if !foundTimeout {
		t.Fatal("expected agent.timeout event for blocked execution disconnect")
	}
}
