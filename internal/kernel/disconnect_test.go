package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestHandleAgentDisconnectCancelsOrphanedSteps(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "local.tool",
		},
	})
	if err != nil {
		t.Fatalf("invoke tool: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got error: %s", result.Error)
	}
	stepID := result.StepID

	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if !state.HasActiveSteps() {
		t.Fatal("expected active steps before disconnect")
	}

	k.HandleAgentDisconnect(ctx, sessionID)

	events.mu.Lock()
	var foundStepCancelled bool
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventStepCancelled && evt.StepID == stepID {
			foundStepCancelled = true
		}
	}
	events.mu.Unlock()

	if !foundStepCancelled {
		t.Fatal("expected step.cancelled event for orphaned step after disconnect")
	}

	state, err = k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution after disconnect: %v", err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		t.Fatalf("expected pending, got %s", state.Execution.Status)
	}
	if state.HasActiveSteps() {
		t.Fatal("expected no active steps after reset")
	}
}

func TestHandleAgentDisconnectAllowsNewToolAfterReassignment(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "local.tool",
		},
	})
	if err != nil {
		t.Fatalf("invoke tool: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted, got error: %s", result.Error)
	}

	k.HandleAgentDisconnect(ctx, sessionID)

	newSessionID := "new-session-" + execID[:8]
	sessions.Create(ctx, domain.Session{
		ID:          newSessionID,
		ExecutionID: execID,
		AgentID:     "agent-1",
		ConsumerID:  "consumer-2",
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(2 * time.Minute),
	})
	k.EmitEvent(ctx, execID, "", domain.EventExecutionStarted, nil, uuid.Nil, uuid.Nil)
	k.events.UpdateExecutionStatus(ctx, execID, domain.ExecutionRunning)

	result, err = k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   newSessionID,
		Intent: domain.Intent{
			Type:      domain.IntentInvokeTool,
			ToolID:    "another.tool",
			Arguments: json.RawMessage(`{}`),
		},
	})
	if err != nil {
		t.Fatalf("new agent invoke tool after reassignment: %v", err)
	}
	if !result.Accepted {
		t.Fatalf("expected accepted after reassignment, got error: %s", result.Error)
	}
}

func TestHandleAgentDisconnectSkipsTerminalSteps(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "local.tool",
		},
	})
	if err != nil {
		t.Fatalf("invoke tool: %v", err)
	}

	k.SubmitStepResult(ctx, StepResultRequest{
		ExecutionID: execID,
		StepID:      result.StepID,
		SessionID:   sessionID,
		Success:     true,
		Data:        json.RawMessage(`{"ok":true}`),
	})

	k.HandleAgentDisconnect(ctx, sessionID)

	events.mu.Lock()
	cancelCount := 0
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventStepCancelled {
			cancelCount++
		}
	}
	events.mu.Unlock()

	if cancelCount != 0 {
		t.Fatalf("expected no step.cancelled events for already-terminal step, got %d", cancelCount)
	}
}

func TestHandleAgentDisconnectReassignsWhenAgentAlreadyReconnected(t *testing.T) {
	k, events, agentHub, _, sessions, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Agent is still connected (simulates reconnect completing before disconnect handler).
	agentHub.mu.Lock()
	agentHub.hasConn = true
	agentHub.mu.Unlock()

	k.HandleAgentDisconnect(ctx, sessionID)

	// The execution should have been immediately reassigned (back to running)
	// because the agent was already connected when the reset happened.
	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after reconnect-race reassignment, got %s", state.Execution.Status)
	}

	// Verify the full event sequence: agent.timeout, execution.reset, execution.started.
	events.mu.Lock()
	var foundReset, foundRestarted bool
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventExecutionReset {
			foundReset = true
		}
		if evt.Type == domain.EventExecutionStarted && foundReset {
			foundRestarted = true
		}
	}
	events.mu.Unlock()

	if !foundReset {
		t.Fatal("expected execution.reset event")
	}
	if !foundRestarted {
		t.Fatal("expected execution.started event after reset (reassignment)")
	}
}

func TestHandleAgentDisconnectStaysPendingWhenNoAgent(t *testing.T) {
	k, _, agentHub, _, sessions, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Agent has no connections (truly disconnected).
	agentHub.mu.Lock()
	agentHub.hasConn = false
	agentHub.mu.Unlock()

	k.HandleAgentDisconnect(ctx, sessionID)

	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		t.Fatalf("expected pending when no agent connected, got %s", state.Execution.Status)
	}
}

func TestHandleAgentDisconnectTerminalExecutionNoOp(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent:      domain.Intent{Type: domain.IntentComplete, Output: json.RawMessage(`{}`)},
	})

	events.mu.Lock()
	countBefore := len(events.events[execID])
	events.mu.Unlock()

	k.HandleAgentDisconnect(ctx, sessionID)

	events.mu.Lock()
	countAfter := len(events.events[execID])
	events.mu.Unlock()

	if countAfter != countBefore {
		t.Fatalf("expected no new events after disconnect on terminal execution, before=%d after=%d", countBefore, countAfter)
	}
}

func TestHandleAgentDisconnectNoDeadlockWithRealLocker(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newConnectedMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   runnerHub,
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      memstore.NewLocker(),
		Policy:      newAllowAllPolicy(),
	})

	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	agentHub.mu.Lock()
	agentHub.hasConn = true
	agentHub.mu.Unlock()

	done := make(chan struct{})
	go func() {
		k.HandleAgentDisconnect(ctx, sessionID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleAgentDisconnect deadlocked — buildClaimResult tried to re-acquire the execution lock")
	}

	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after reassignment, got %s", state.Execution.Status)
	}
}
