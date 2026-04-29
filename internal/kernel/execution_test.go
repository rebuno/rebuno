package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/memstore"
)

func TestCreateExecution(t *testing.T) {
	k, events, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"query":"hello"}`),
		Labels:  map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID")
	}

	evts := events.events[execID]
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	if evts[0].Type != domain.EventExecutionCreated {
		t.Fatalf("expected execution.created event, got %s", evts[0].Type)
	}
}

func TestCreateExecutionWithConnectedAgent(t *testing.T) {
	k, events, agentHub, _, _, _ := newConnectedTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"query":"hello"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentHub.mu.Lock()
	sentCount := len(agentHub.sent)
	agentHub.mu.Unlock()

	if sentCount == 0 {
		t.Fatal("expected message sent to agent hub when agent is connected")
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
		t.Fatal("expected execution.started event when agent is connected")
	}
}

func TestCreateExecutionMissingAgentID(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	_, err := k.CreateExecution(ctx, CreateExecutionRequest{})
	if err == nil {
		t.Fatal("expected error for missing agent_id")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestGetExecutionNotFound(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	_, err := k.GetExecution(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetExecutionAfterCreate(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"q":"test"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		t.Fatalf("expected pending, got %s", state.Execution.Status)
	}
	if state.AgentID != "agent-1" {
		t.Fatalf("expected agent-1, got %s", state.AgentID)
	}
}

func TestCancelExecution(t *testing.T) {
	k, events, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})

	err := k.CancelExecution(ctx, execID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evts := events.events[execID]
	lastEvt := evts[len(evts)-1]
	if lastEvt.Type != domain.EventExecutionCancelled {
		t.Fatalf("expected execution.cancelled, got %s", lastEvt.Type)
	}
}

func TestCancelExecutionNotFound(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	err := k.CancelExecution(ctx, "nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCancelTerminalExecutionFails(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := k.CreateExecution(ctx, CreateExecutionRequest{AgentID: "agent-1"})
	k.CancelExecution(ctx, execID) // cancel once

	err := k.CancelExecution(ctx, execID) // cancel again
	if !errors.Is(err, domain.ErrTerminalExecution) {
		t.Fatalf("expected ErrTerminalExecution, got %v", err)
	}
}

func TestCancelRunningExecutionWithActiveStep(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Create a step so there's an active non-terminal step.
	k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
		},
	})

	err := k.CancelExecution(ctx, execID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify step.cancelled event was emitted.
	foundStepCancelled := false
	foundExecCancelled := false
	for _, e := range events.events[execID] {
		if e.Type == domain.EventStepCancelled {
			foundStepCancelled = true
		}
		if e.Type == domain.EventExecutionCancelled {
			foundExecCancelled = true
		}
	}
	if !foundStepCancelled {
		t.Fatal("expected step.cancelled event for active step")
	}
	if !foundExecCancelled {
		t.Fatal("expected execution.cancelled event")
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionCancelled {
		t.Fatalf("expected cancelled, got %s", state.Execution.Status)
	}
}

func TestAssignPendingExecutionsWithRealLocker(t *testing.T) {
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

	execID, err := k.CreateExecution(ctx, CreateExecutionRequest{
		AgentID: "agent-1",
		Input:   json.RawMessage(`{"q":"test"}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, err := k.GetExecution(ctx, execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running, got %s", state.Execution.Status)
	}
}
