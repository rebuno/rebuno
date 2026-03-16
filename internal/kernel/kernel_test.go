package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

func newTestKernel() (*Kernel, *mockEventStore, *mockAgentHub, *mockRunnerHub, *mockSessionStore, *mockSignalStore) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
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
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
	})

	return k, events, agentHub, runnerHub, sessions, signals
}

// newConnectedTestKernel creates a test kernel with a connected agent hub,
// so executions are immediately assigned upon creation.
func newConnectedTestKernel() (*Kernel, *mockEventStore, *mockAgentHub, *mockRunnerHub, *mockSessionStore, *mockSignalStore) {
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
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
	})

	return k, events, agentHub, runnerHub, sessions, signals
}

func TestEmitEventAppendFails(t *testing.T) {
	k, events, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	events.appendErr = errors.New("database connection lost")

	_, err := k.EmitEvent(ctx, "exec-1", "", domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}, uuid.Nil, uuid.Nil)
	if err == nil {
		t.Fatal("expected error when Append fails")
	}
	if !strings.Contains(err.Error(), "appending event") {
		t.Fatalf("expected 'appending event' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "database connection lost") {
		t.Fatalf("expected underlying error message, got: %v", err)
	}
}

func TestEmitEventsAppendBatchFails(t *testing.T) {
	k, events, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	events.appendErr = errors.New("disk full")

	_, err := k.EmitEvents(ctx, "exec-1", uuid.Nil, []eventEntry{
		{stepID: "s1", eventType: domain.EventStepCreated, payload: domain.StepCreatedPayload{ToolID: "t1"}},
		{stepID: "s1", eventType: domain.EventStepDispatched, payload: domain.StepDispatchedPayload{}},
	})
	if err == nil {
		t.Fatal("expected error when AppendBatch fails")
	}
	if !strings.Contains(err.Error(), "appending event batch") {
		t.Fatalf("expected 'appending event batch' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected underlying error message, got: %v", err)
	}
}

func TestEmitEventMarshalFails(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()
	ctx := context.Background()

	// Channels cannot be marshaled to JSON.
	unmarshalable := make(chan struct{})

	_, err := k.EmitEvent(ctx, "exec-1", "", domain.EventExecutionCreated,
		unmarshalable, uuid.Nil, uuid.Nil)
	if err == nil {
		t.Fatal("expected error when json.Marshal fails on payload")
	}
	if !strings.Contains(err.Error(), "marshaling event payload") {
		t.Fatalf("expected 'marshaling event payload' in error, got: %v", err)
	}
}
