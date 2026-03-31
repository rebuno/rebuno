package lifecycle

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/projector"
)

type testFixture struct {
	events      *mockEventStore
	sessions    *mockSessionStore
	signals     *mockSignalStore
	checkpoints *mockCheckpointStore
	agentHub    *mockAgentHub
	emitter     *mockEmitter
}

func newTestFixture() testFixture {
	return testFixture{
		events:      newMockEventStore(),
		sessions:    newMockSessionStore(),
		signals:     newMockSignalStore(),
		checkpoints: newMockCheckpointStore(),
		agentHub:    newMockAgentHub(),
		emitter:     newMockEmitter(),
	}
}

func (f testFixture) manager(executionTimeout time.Duration) *Manager {
	logger := slog.Default()
	proj := projector.New(f.events, f.checkpoints, logger)

	return NewManager(Deps{
		Events:           f.events,
		Sessions:         f.sessions,
		Checkpoints:      f.checkpoints,
		Signals:          f.signals,
		AgentHub:         f.agentHub,
		Locker:           &mockLocker{},
		Projector:        proj,
		Emitter:          f.emitter,
		Logger:           logger,
		ExecutionTimeout: executionTimeout,
	})
}

func createdEvent(agentID string, seq int64) domain.Event {
	return domain.Event{
		Type:     domain.EventExecutionCreated,
		Payload:  mustMarshal(domain.ExecutionCreatedPayload{AgentID: agentID}),
		Sequence: seq,
	}
}

func startedEvent(seq int64) domain.Event {
	return domain.Event{
		Type:     domain.EventExecutionStarted,
		Payload:  mustMarshal(domain.ExecutionStartedPayload{}),
		Sequence: seq,
	}
}

func TestReapSessions(t *testing.T) {
	tests := []struct {
		name             string
		deletedExpired   int
		activeExecIDs    []string
		sessionsForExec  map[string]*domain.Session
		events           map[string][]domain.Event
		wantStatusUpdate map[string]domain.ExecutionStatus
	}{
		{
			name:           "no expired sessions",
			deletedExpired: 0,
		},
		{
			name:           "expired sessions with no orphans",
			deletedExpired: 2,
			activeExecIDs:  []string{"exec-1"},
			sessionsForExec: map[string]*domain.Session{
				"exec-1": {ID: "sess-1", ExecutionID: "exec-1", AgentID: "agent-1"},
			},
		},
		{
			name:            "expired sessions with orphaned running execution",
			deletedExpired:  1,
			activeExecIDs:   []string{"exec-orphan"},
			sessionsForExec: map[string]*domain.Session{},
			events: map[string][]domain.Event{
				"exec-orphan": {
					createdEvent("agent-1", 1),
					startedEvent(2),
				},
			},
			wantStatusUpdate: map[string]domain.ExecutionStatus{
				"exec-orphan": domain.ExecutionPending,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTestFixture()
			f.events.activeIDs = tt.activeExecIDs
			if tt.events != nil {
				f.events.events = tt.events
			}

			f.sessions.deletedExpired = tt.deletedExpired
			if tt.sessionsForExec != nil {
				for _, s := range tt.sessionsForExec {
					f.sessions.sessions[s.ID] = s
				}
			}

			m := f.manager(time.Hour)
			m.reapSessions(context.Background())

			for execID, wantStatus := range tt.wantStatusUpdate {
				gotStatus, ok := f.events.statusUpdates[execID]
				if !ok {
					t.Errorf("expected status update for %s, got none", execID)
					continue
				}
				if gotStatus != wantStatus {
					t.Errorf("execution %s: expected status %s, got %s", execID, wantStatus, gotStatus)
				}
			}
		})
	}
}

func TestCheckTimeouts(t *testing.T) {
	t.Run("step timeout triggers step.timed_out and execution.failed", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-timeout-step"
		f.events.activeIDs = []string{execID}
		pastDeadline := time.Now().Add(-1 * time.Minute)
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				StepID:   "step-1",
				Type:     domain.EventStepCreated,
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web.search", Attempt: 1}),
				Sequence: 3,
			},
			{
				StepID:   "step-1",
				Type:     domain.EventStepDispatched,
				Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: pastDeadline}),
				Sequence: 4,
			},
		}

		m := f.manager(time.Hour)
		m.checkTimeouts(context.Background())

		f.emitter.mu.Lock()
		defer f.emitter.mu.Unlock()

		foundStepTimedOut := false
		foundExecFailed := false
		for _, e := range f.emitter.events {
			if e.EventType == domain.EventStepTimedOut && e.StepID == "step-1" {
				foundStepTimedOut = true
			}
			if e.EventType == domain.EventExecutionFailed && e.ExecutionID == execID {
				foundExecFailed = true
			}
		}
		if !foundStepTimedOut {
			t.Error("expected step.timed_out event to be emitted")
		}
		if !foundExecFailed {
			t.Error("expected execution.failed event to be emitted")
		}
	})

	t.Run("execution timeout triggers execution.failed", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-timeout-global"
		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			{
				Type:      domain.EventExecutionCreated,
				Payload:   mustMarshal(domain.ExecutionCreatedPayload{AgentID: "agent-1"}),
				Sequence:  1,
				Timestamp: time.Now().Add(-2 * time.Hour),
			},
			startedEvent(2),
		}

		m := f.manager(1 * time.Hour)
		m.checkTimeouts(context.Background())

		f.emitter.mu.Lock()
		defer f.emitter.mu.Unlock()

		foundExecFailed := false
		for _, e := range f.emitter.events {
			if e.EventType == domain.EventExecutionFailed && e.ExecutionID == execID {
				foundExecFailed = true
			}
		}
		if !foundExecFailed {
			t.Error("expected execution.failed event to be emitted for execution timeout")
		}
	})

	t.Run("no timeout when step has future deadline", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-no-timeout"
		f.events.activeIDs = []string{execID}
		futureDeadline := time.Now().Add(10 * time.Minute)
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				StepID:   "step-1",
				Type:     domain.EventStepCreated,
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web.search", Attempt: 1}),
				Sequence: 3,
			},
			{
				StepID:   "step-1",
				Type:     domain.EventStepDispatched,
				Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: futureDeadline}),
				Sequence: 4,
			},
		}

		m := f.manager(time.Hour)
		m.checkTimeouts(context.Background())

		f.emitter.mu.Lock()
		defer f.emitter.mu.Unlock()

		if len(f.emitter.events) != 0 {
			t.Errorf("expected no emitted events, got %d", len(f.emitter.events))
		}
	})
}

func TestCleanupTerminalExecutions(t *testing.T) {
	t.Run("deletes terminal executions and their data", func(t *testing.T) {
		f := newTestFixture()
		f.events.terminalIDs = []string{"exec-done-1", "exec-done-2"}

		m := f.manager(time.Hour)
		m.cleanupTerminalExecutions(context.Background(), 168*time.Hour)

		if len(f.events.deletedIDs) != 2 {
			t.Fatalf("expected 2 deleted executions, got %d", len(f.events.deletedIDs))
		}
		if len(f.checkpoints.deletedIDs) != 2 {
			t.Fatalf("expected 2 deleted checkpoints, got %d", len(f.checkpoints.deletedIDs))
		}
		if len(f.signals.clearedIDs) != 2 {
			t.Fatalf("expected 2 cleared signal sets, got %d", len(f.signals.clearedIDs))
		}
	})

	t.Run("no terminal executions to clean up", func(t *testing.T) {
		f := newTestFixture()

		m := f.manager(time.Hour)
		m.cleanupTerminalExecutions(context.Background(), 168*time.Hour)

		if len(f.events.deletedIDs) != 0 {
			t.Fatalf("expected 0 deleted executions, got %d", len(f.events.deletedIDs))
		}
	})
}

func TestRecoverActiveExecutions(t *testing.T) {
	t.Run("orphaned running execution reassigned to pending", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-orphan"
		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}

		m := f.manager(time.Hour)
		m.RecoverActiveExecutions(context.Background())

		status, ok := f.events.statusUpdates[execID]
		if !ok {
			t.Fatal("expected status update for orphaned execution")
		}
		if status != domain.ExecutionPending {
			t.Fatalf("expected status pending, got %s", status)
		}
	})

	t.Run("execution with active session is not reassigned", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-active"
		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}
		f.sessions.sessions["sess-1"] = &domain.Session{
			ID:          "sess-1",
			ExecutionID: execID,
			AgentID:     "agent-1",
		}

		m := f.manager(time.Hour)
		m.RecoverActiveExecutions(context.Background())

		if _, ok := f.events.statusUpdates[execID]; ok {
			t.Fatal("did not expect status update for execution with active session")
		}
	})

	t.Run("terminal execution reconciles status", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-completed"
		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				Type:     domain.EventExecutionCompleted,
				Payload:  mustMarshal(domain.ExecutionCompletedPayload{}),
				Sequence: 3,
			},
		}

		m := f.manager(time.Hour)
		m.RecoverActiveExecutions(context.Background())

		status, ok := f.events.statusUpdates[execID]
		if !ok {
			t.Fatal("expected status update for terminal reconciliation")
		}
		if status != domain.ExecutionCompleted {
			t.Fatalf("expected status completed, got %s", status)
		}
	})
}

func TestReapSessionsCancelledContext(t *testing.T) {
	f := newTestFixture()
	f.events.activeIDs = []string{"exec-1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := f.manager(time.Hour)
	m.reapSessions(ctx)

	// Should return early without checking active executions.
	if len(f.events.statusUpdates) != 0 {
		t.Fatal("expected no status updates when context is cancelled")
	}
}

func TestCheckTimeoutsCancelledContext(t *testing.T) {
	f := newTestFixture()
	f.events.activeIDs = []string{"exec-1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := f.manager(time.Hour)
	m.checkTimeouts(ctx)

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()
	if len(f.emitter.events) != 0 {
		t.Fatal("expected no events emitted when context is cancelled")
	}
}

func TestCleanupTerminalExecutionsCancelledContext(t *testing.T) {
	f := newTestFixture()
	f.events.terminalIDs = []string{"exec-done-1"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := f.manager(time.Hour)
	m.cleanupTerminalExecutions(ctx, 168*time.Hour)

	if len(f.events.deletedIDs) != 0 {
		t.Fatal("expected no deletions when context is cancelled")
	}
}

func TestCheckTimeoutsSkipsTerminalExecution(t *testing.T) {
	f := newTestFixture()

	execID := "exec-already-done"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		{
			Type:      domain.EventExecutionCreated,
			Payload:   mustMarshal(domain.ExecutionCreatedPayload{AgentID: "agent-1"}),
			Sequence:  1,
			Timestamp: time.Now().Add(-2 * time.Hour),
		},
		startedEvent(2),
		{
			Type:     domain.EventExecutionCompleted,
			Payload:  mustMarshal(domain.ExecutionCompletedPayload{}),
			Sequence: 3,
		},
	}

	m := f.manager(time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	if len(f.emitter.events) != 0 {
		t.Errorf("expected no emitted events for terminal execution, got %d", len(f.emitter.events))
	}
}

func TestCheckTimeoutsZeroExecutionTimeout(t *testing.T) {
	f := newTestFixture()

	execID := "exec-no-global-timeout"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		{
			Type:      domain.EventExecutionCreated,
			Payload:   mustMarshal(domain.ExecutionCreatedPayload{AgentID: "agent-1"}),
			Sequence:  1,
			Timestamp: time.Now().Add(-100 * time.Hour),
		},
		startedEvent(2),
	}

	// Zero timeout means no execution-level timeout enforcement.
	m := f.manager(0)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	if len(f.emitter.events) != 0 {
		t.Errorf("expected no events when execution timeout is 0, got %d", len(f.emitter.events))
	}
}

func TestRecoverActiveExecutionsBlockedOrphan(t *testing.T) {
	f := newTestFixture()

	execID := "exec-blocked-orphan"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		{
			Type:     domain.EventExecutionBlocked,
			Payload:  mustMarshal(domain.ExecutionBlockedPayload{Reason: "signal", Ref: "approval"}),
			Sequence: 3,
		},
	}

	m := f.manager(time.Hour)
	m.RecoverActiveExecutions(context.Background())

	status, ok := f.events.statusUpdates[execID]
	if !ok {
		t.Fatal("expected status update for blocked orphaned execution")
	}
	if status != domain.ExecutionPending {
		t.Fatalf("expected status pending, got %s", status)
	}
}

func TestReapSessionsOrphanedPendingExecutionNotReassigned(t *testing.T) {
	// A pending execution without a session should not be touched by the reaper
	// because handleOrphanedExecution returns early for pending status.
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.events.activeIDs = []string{"exec-pending"}
	f.events.events["exec-pending"] = []domain.Event{
		createdEvent("agent-1", 1),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	if _, ok := f.events.statusUpdates["exec-pending"]; ok {
		t.Fatal("did not expect status update for already-pending execution")
	}
}

func stepCreatedEvent(stepID string, seq int64) domain.Event {
	return domain.Event{
		StepID:   stepID,
		Type:     domain.EventStepCreated,
		Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "local.tool", Attempt: 1}),
		Sequence: seq,
	}
}

func TestRecoverActiveExecutionsCancelsOrphanedSteps(t *testing.T) {
	f := newTestFixture()

	execID := "exec-orphan-step"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		stepCreatedEvent("step-1", 3),
	}

	m := f.manager(time.Hour)
	m.RecoverActiveExecutions(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	foundStepCancelled := false
	for _, e := range f.emitter.events {
		if e.EventType == domain.EventStepCancelled && e.StepID == "step-1" {
			foundStepCancelled = true
		}
	}
	if !foundStepCancelled {
		t.Error("expected step.cancelled event for orphaned step during recovery")
	}

	status, ok := f.events.statusUpdates[execID]
	if !ok {
		t.Fatal("expected status update for orphaned execution")
	}
	if status != domain.ExecutionPending {
		t.Fatalf("expected status pending, got %s", status)
	}
}

func TestReapSessionsCancelsOrphanedSteps(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	execID := "exec-orphan-step"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		stepCreatedEvent("step-1", 3),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	foundStepCancelled := false
	for _, e := range f.emitter.events {
		if e.EventType == domain.EventStepCancelled && e.StepID == "step-1" {
			foundStepCancelled = true
		}
	}
	if !foundStepCancelled {
		t.Error("expected step.cancelled event for orphaned step during session reaping")
	}
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
