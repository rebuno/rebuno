package lifecycle

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/store"
)

type testFixture struct {
	events      *mockEventStore
	sessions    *mockSessionStore
	signals     *mockSignalStore
	checkpoints *mockCheckpointStore
	agentHub    *mockAgentHub
	emitter     *mockEmitter
	assigner    *mockAssigner
}

func newTestFixture() testFixture {
	return testFixture{
		events:      newMockEventStore(),
		sessions:    newMockSessionStore(),
		signals:     newMockSignalStore(),
		checkpoints: newMockCheckpointStore(),
		agentHub:    newMockAgentHub(),
		emitter:     newMockEmitter(),
		assigner:    newMockAssigner(),
	}
}

func (f testFixture) manager(executionTimeout time.Duration) *Manager {
	return f.managerWithMetrics(executionTimeout, nil)
}

func (f testFixture) managerWithMetrics(executionTimeout time.Duration, metrics *observe.Metrics) *Manager {
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
		Assigner:         f.assigner,
		Logger:           logger,
		Metrics:          metrics,
		ExecutionTimeout: executionTimeout,
	})
}

func newTestGauge() prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "test_active_executions",
		Help: "test gauge",
	})
}

func gaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	g.Write(&m)
	return m.GetGauge().GetValue()
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

func startedEventWithSession(sessionID string, seq int64) domain.Event {
	return domain.Event{
		Type:     domain.EventExecutionStarted,
		Payload:  mustMarshal(domain.ExecutionStartedPayload{SessionID: sessionID}),
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
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1}),
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
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1}),
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

	t.Run("execution with stale session is recovered after purge", func(t *testing.T) {
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

		status, ok := f.events.statusUpdates[execID]
		if !ok {
			t.Fatal("expected status update after stale session purge")
		}
		if status != domain.ExecutionPending {
			t.Fatalf("expected status pending, got %s", status)
		}

		if len(f.sessions.sessions) != 0 {
			t.Fatal("expected all sessions to be purged")
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

func TestReapSessionsPendingExecutionWithConnectedAgentAssigned(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = true
	f.events.activeIDs = []string{"exec-pending"}
	f.events.events["exec-pending"] = []domain.Event{
		createdEvent("agent-1", 1),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	f.assigner.mu.Lock()
	var foundAssignment bool
	for _, a := range f.assigner.assigned {
		if a.ExecutionID == "exec-pending" && a.AgentID == "agent-1" {
			foundAssignment = true
		}
	}
	f.assigner.mu.Unlock()

	if !foundAssignment {
		t.Fatal("expected TryAssignExecution call for pending execution with connected agent")
	}
}

func TestReapSessionsPendingExecutionNoAgentSkipped(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = false
	f.events.activeIDs = []string{"exec-pending"}
	f.events.events["exec-pending"] = []domain.Event{
		createdEvent("agent-1", 1),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	f.assigner.mu.Lock()
	assignCount := len(f.assigner.assigned)
	f.assigner.mu.Unlock()

	if assignCount != 0 {
		t.Fatalf("expected no assignment when agent not connected, got %d", assignCount)
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
		Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "local_tool", Attempt: 1}),
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

func TestExecutionTimeoutCancelsActiveSteps(t *testing.T) {
	f := newTestFixture()

	execID := "exec-timeout-with-steps"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		{
			Type:      domain.EventExecutionCreated,
			Payload:   mustMarshal(domain.ExecutionCreatedPayload{AgentID: "agent-1"}),
			Sequence:  1,
			Timestamp: time.Now().Add(-2 * time.Hour),
		},
		startedEvent(2),
		stepCreatedEvent("step-1", 3),
		stepCreatedEvent("step-2", 4),
		{
			StepID:   "step-3",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "local_tool", Attempt: 1}),
			Sequence: 5,
		},
		{
			StepID:   "step-3",
			Type:     domain.EventStepCompleted,
			Payload:  mustMarshal(domain.StepCompletedPayload{}),
			Sequence: 6,
		},
	}

	m := f.manager(1 * time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	cancelledSteps := map[string]bool{}
	foundExecFailed := false
	for _, e := range f.emitter.events {
		if e.EventType == domain.EventStepCancelled {
			cancelledSteps[e.StepID] = true
		}
		if e.EventType == domain.EventExecutionFailed && e.ExecutionID == execID {
			foundExecFailed = true
		}
	}

	if !cancelledSteps["step-1"] {
		t.Error("expected step.cancelled for step-1")
	}
	if !cancelledSteps["step-2"] {
		t.Error("expected step.cancelled for step-2")
	}
	if cancelledSteps["step-3"] {
		t.Error("did not expect step.cancelled for already-terminal step-3")
	}
	if !foundExecFailed {
		t.Error("expected execution.failed event")
	}
}

func TestStepTimeoutCancelsSiblingSteps(t *testing.T) {
	f := newTestFixture()

	execID := "exec-step-timeout-siblings"
	f.events.activeIDs = []string{execID}
	pastDeadline := time.Now().Add(-1 * time.Minute)
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		stepCreatedEvent("step-1", 3),
		{
			StepID:   "step-1",
			Type:     domain.EventStepDispatched,
			Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: pastDeadline}),
			Sequence: 4,
		},
		stepCreatedEvent("step-2", 5),
		stepCreatedEvent("step-3", 6),
		{
			StepID:   "step-4",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "local_tool", Attempt: 1}),
			Sequence: 7,
		},
		{
			StepID:   "step-4",
			Type:     domain.EventStepCompleted,
			Payload:  mustMarshal(domain.StepCompletedPayload{}),
			Sequence: 8,
		},
	}

	m := f.manager(time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	foundStepTimedOut := false
	cancelledSteps := map[string]bool{}
	foundExecFailed := false
	for _, e := range f.emitter.events {
		switch e.EventType {
		case domain.EventStepTimedOut:
			if e.StepID == "step-1" {
				foundStepTimedOut = true
			}
		case domain.EventStepCancelled:
			cancelledSteps[e.StepID] = true
		case domain.EventExecutionFailed:
			foundExecFailed = true
		}
	}

	if !foundStepTimedOut {
		t.Error("expected step.timed_out for step-1")
	}
	if !cancelledSteps["step-2"] {
		t.Error("expected step.cancelled for sibling step-2")
	}
	if !cancelledSteps["step-3"] {
		t.Error("expected step.cancelled for sibling step-3")
	}
	if cancelledSteps["step-1"] {
		t.Error("did not expect step.cancelled for the timed-out step-1 itself")
	}
	if cancelledSteps["step-4"] {
		t.Error("did not expect step.cancelled for already-terminal step-4")
	}
	if !foundExecFailed {
		t.Error("expected execution.failed event")
	}
}

func TestFailStepTimeoutSkipsTerminalExecution(t *testing.T) {
	t.Run("multiple simultaneous step timeouts emit only one execution.failed", func(t *testing.T) {
		f := newTestFixture()
		f.emitter.feedback = f.events

		execID := "exec-multi-step-timeout"
		f.events.activeIDs = []string{execID}
		pastDeadline := time.Now().Add(-1 * time.Minute)
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				StepID:   "step-1",
				Type:     domain.EventStepCreated,
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1}),
				Sequence: 3,
			},
			{
				StepID:   "step-1",
				Type:     domain.EventStepDispatched,
				Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: pastDeadline}),
				Sequence: 4,
			},
			{
				StepID:   "step-2",
				Type:     domain.EventStepCreated,
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_fetch", Attempt: 1}),
				Sequence: 5,
			},
			{
				StepID:   "step-2",
				Type:     domain.EventStepDispatched,
				Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r2", Deadline: pastDeadline}),
				Sequence: 6,
			},
		}

		m := f.manager(time.Hour)
		m.checkTimeouts(context.Background())

		f.emitter.mu.Lock()
		defer f.emitter.mu.Unlock()

		execFailedCount := 0
		stepTimedOutCount := 0
		for _, e := range f.emitter.events {
			if e.EventType == domain.EventExecutionFailed && e.ExecutionID == execID {
				execFailedCount++
			}
			if e.EventType == domain.EventStepTimedOut {
				stepTimedOutCount++
			}
		}

		if execFailedCount != 1 {
			t.Errorf("expected exactly 1 execution.failed event, got %d", execFailedCount)
		}
		if stepTimedOutCount < 1 {
			t.Error("expected at least 1 step.timed_out event")
		}
	})

	t.Run("failStepTimeout skips already-failed execution", func(t *testing.T) {
		f := newTestFixture()

		execID := "exec-already-failed"
		pastDeadline := time.Now().Add(-1 * time.Minute)
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				StepID:   "step-1",
				Type:     domain.EventStepCreated,
				Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1}),
				Sequence: 3,
			},
			{
				StepID:   "step-1",
				Type:     domain.EventStepDispatched,
				Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: pastDeadline}),
				Sequence: 4,
			},
			{
				Type:     domain.EventExecutionFailed,
				Payload:  mustMarshal(domain.ExecutionFailedPayload{Error: "step timed out"}),
				Sequence: 5,
			},
		}

		m := f.manager(time.Hour)
		m.failStepTimeout(context.Background(), execID, "step-1")

		f.emitter.mu.Lock()
		defer f.emitter.mu.Unlock()

		if len(f.emitter.events) != 0 {
			t.Errorf("expected no events emitted for already-failed execution, got %d", len(f.emitter.events))
		}
	})
}

func TestStepTimeoutDecrementsActiveExecutions(t *testing.T) {
	f := newTestFixture()

	gauge := newTestGauge()
	gauge.Set(5)
	metrics := &observe.Metrics{ActiveExecutions: gauge}

	execID := "exec-timeout-metric"
	f.events.activeIDs = []string{execID}
	pastDeadline := time.Now().Add(-1 * time.Minute)
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		{
			StepID:   "step-1",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1}),
			Sequence: 3,
		},
		{
			StepID:   "step-1",
			Type:     domain.EventStepDispatched,
			Payload:  mustMarshal(domain.StepDispatchedPayload{RunnerID: "r1", Deadline: pastDeadline}),
			Sequence: 4,
		},
	}

	m := f.managerWithMetrics(time.Hour, metrics)
	m.checkTimeouts(context.Background())

	if v := gaugeValue(gauge); v != 4 {
		t.Errorf("expected ActiveExecutions gauge to be 4 after step timeout, got %v", v)
	}
}

func TestExecutionTimeoutDecrementsActiveExecutions(t *testing.T) {
	f := newTestFixture()

	gauge := newTestGauge()
	gauge.Set(3)
	metrics := &observe.Metrics{ActiveExecutions: gauge}

	execID := "exec-timeout-global-metric"
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

	m := f.managerWithMetrics(1*time.Hour, metrics)
	m.checkTimeouts(context.Background())

	if v := gaugeValue(gauge); v != 2 {
		t.Errorf("expected ActiveExecutions gauge to be 2 after execution timeout, got %v", v)
	}
}

func TestRecoverActiveExecutionsInitializesGauge(t *testing.T) {
	t.Run("sets gauge to count of non-terminal executions", func(t *testing.T) {
		f := newTestFixture()

		gauge := newTestGauge()
		gauge.Set(99)
		metrics := &observe.Metrics{ActiveExecutions: gauge}

		f.events.activeIDs = []string{"exec-running", "exec-completed"}
		f.events.events["exec-running"] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}
		f.events.events["exec-completed"] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				Type:     domain.EventExecutionCompleted,
				Payload:  mustMarshal(domain.ExecutionCompletedPayload{}),
				Sequence: 3,
			},
		}

		m := f.managerWithMetrics(time.Hour, metrics)
		m.RecoverActiveExecutions(context.Background())

		if v := gaugeValue(gauge); v != 1 {
			t.Errorf("expected ActiveExecutions gauge to be 1 (only running), got %v", v)
		}
	})

	t.Run("sets gauge to zero when all executions are terminal", func(t *testing.T) {
		f := newTestFixture()

		gauge := newTestGauge()
		gauge.Set(10)
		metrics := &observe.Metrics{ActiveExecutions: gauge}

		f.events.activeIDs = []string{"exec-done"}
		f.events.events["exec-done"] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
			{
				Type:     domain.EventExecutionCompleted,
				Payload:  mustMarshal(domain.ExecutionCompletedPayload{}),
				Sequence: 3,
			},
		}

		m := f.managerWithMetrics(time.Hour, metrics)
		m.RecoverActiveExecutions(context.Background())

		if v := gaugeValue(gauge); v != 0 {
			t.Errorf("expected ActiveExecutions gauge to be 0, got %v", v)
		}
	})
}

func TestCheckTimeoutsBlockedExecutionTimesOut(t *testing.T) {
	f := newTestFixture()

	execID := "exec-blocked-expired"
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
			StepID:   "step-1",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web.search", Attempt: 1}),
			Sequence: 3,
		},
		{
			StepID:   "step-1",
			Type:     domain.EventStepApprovalRequired,
			Payload:  mustMarshal(domain.StepApprovalRequiredPayload{ToolID: "web.search", Reason: "policy"}),
			Sequence: 4,
		},
		{
			Type:     domain.EventExecutionBlocked,
			Payload:  mustMarshal(domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "web.search"}),
			Sequence: 5,
		},
	}

	m := f.manager(1 * time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	foundExecFailed := false
	foundStepCancelled := false
	for _, e := range f.emitter.events {
		if e.EventType == domain.EventExecutionFailed && e.ExecutionID == execID {
			foundExecFailed = true
		}
		if e.EventType == domain.EventStepCancelled && e.StepID == "step-1" {
			foundStepCancelled = true
		}
	}
	if !foundExecFailed {
		t.Error("expected execution.failed event for blocked execution that exceeded timeout")
	}
	if !foundStepCancelled {
		t.Error("expected step.cancelled event for active step in timed-out blocked execution")
	}
}

func TestCheckTimeoutsBlockedExecutionNotTimedOut(t *testing.T) {
	// A blocked execution within the timeout window should NOT be failed,
	// and step-level timeouts should still be skipped.
	f := newTestFixture()

	execID := "exec-blocked-fresh"
	f.events.activeIDs = []string{execID}
	pastDeadline := time.Now().Add(-10 * time.Minute)
	f.events.events[execID] = []domain.Event{
		{
			Type:      domain.EventExecutionCreated,
			Payload:   mustMarshal(domain.ExecutionCreatedPayload{AgentID: "agent-1"}),
			Sequence:  1,
			Timestamp: time.Now().Add(-5 * time.Minute),
		},
		startedEvent(2),
		{
			StepID:   "step-1",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web.search", Attempt: 1, Deadline: pastDeadline}),
			Sequence: 3,
		},
		{
			StepID:   "step-1",
			Type:     domain.EventStepApprovalRequired,
			Payload:  mustMarshal(domain.StepApprovalRequiredPayload{ToolID: "web.search", Reason: "policy"}),
			Sequence: 4,
		},
		{
			Type:     domain.EventExecutionBlocked,
			Payload:  mustMarshal(domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "web.search"}),
			Sequence: 5,
		},
	}

	m := f.manager(1 * time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	if len(f.emitter.events) != 0 {
		t.Errorf("expected no events for blocked execution within timeout window, got %d", len(f.emitter.events))
	}
}

func TestCheckTimeoutsSkipsBlockedExecution(t *testing.T) {
	f := newTestFixture()

	execID := "exec-blocked-approval"
	f.events.activeIDs = []string{execID}
	pastDeadline := time.Now().Add(-10 * time.Minute)
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		{
			StepID:   "step-1",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1, Deadline: pastDeadline}),
			Sequence: 3,
		},
		{
			StepID:   "step-1",
			Type:     domain.EventStepApprovalRequired,
			Payload:  mustMarshal(domain.StepApprovalRequiredPayload{ToolID: "web_search", Reason: "policy"}),
			Sequence: 4,
		},
		{
			Type:     domain.EventExecutionBlocked,
			Payload:  mustMarshal(domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "web_search"}),
			Sequence: 5,
		},
	}

	m := f.manager(time.Hour)
	m.checkTimeouts(context.Background())

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	if len(f.emitter.events) != 0 {
		t.Errorf("expected no emitted events for blocked execution awaiting approval, got %d", len(f.emitter.events))
	}
}

func TestFailStepTimeoutSkipsBlockedExecution(t *testing.T) {
	f := newTestFixture()

	execID := "exec-blocked-direct"
	pastDeadline := time.Now().Add(-10 * time.Minute)
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
		{
			StepID:   "step-1",
			Type:     domain.EventStepCreated,
			Payload:  mustMarshal(domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1, Deadline: pastDeadline}),
			Sequence: 3,
		},
		{
			StepID:   "step-1",
			Type:     domain.EventStepApprovalRequired,
			Payload:  mustMarshal(domain.StepApprovalRequiredPayload{ToolID: "web_search", Reason: "policy"}),
			Sequence: 4,
		},
		{
			Type:     domain.EventExecutionBlocked,
			Payload:  mustMarshal(domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "web_search"}),
			Sequence: 5,
		},
	}

	m := f.manager(time.Hour)
	m.failStepTimeout(context.Background(), execID, "step-1")

	f.emitter.mu.Lock()
	defer f.emitter.mu.Unlock()

	if len(f.emitter.events) != 0 {
		t.Errorf("expected no events emitted for blocked execution, got %d", len(f.emitter.events))
	}
}

func TestReapSessionsRunningExecutionWithConnectedAgentRecreatesSession(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = true
	f.agentHub.pickResult = true
	f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}

	execID := "exec-running"
	originalSessionID := "sess-original-A"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEventWithSession(originalSessionID, 2),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	// The reaper should have recreated a session reusing the original ID.
	f.sessions.mu.Lock()
	var foundSession bool
	for _, s := range f.sessions.sessions {
		if s.ID == originalSessionID && s.ExecutionID == execID && s.AgentID == "agent-1" && s.ConsumerID == "consumer-1" {
			foundSession = true
		}
	}
	f.sessions.mu.Unlock()

	if !foundSession {
		t.Fatal("expected session to be recreated with original session ID for running execution with connected agent")
	}

	// The execution should NOT be reset to pending — it should stay running.
	if _, ok := f.events.statusUpdates[execID]; ok {
		t.Fatal("did not expect status update; execution should remain running with new session")
	}
}

func TestReapSessionsRunningExecutionWithConnectedAgentNoPickFallsThrough(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = true
	f.agentHub.pickResult = false // PickConnection returns false

	execID := "exec-running"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
	}

	m := f.manager(time.Hour)
	m.reapSessions(context.Background())

	// No session should be created since PickConnection failed.
	f.sessions.mu.Lock()
	sessionCount := len(f.sessions.sessions)
	f.sessions.mu.Unlock()

	if sessionCount != 0 {
		t.Fatalf("expected no sessions when PickConnection fails, got %d", sessionCount)
	}
}

func TestReapSessionsBlockedExecutionWithConnectedAgentSkipped(t *testing.T) {
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = true
	f.agentHub.pickResult = true
	f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}

	execID := "exec-blocked"
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
	m.reapSessions(context.Background())

	// Blocked execution with connected agent should be skipped (not recreated, not orphaned).
	f.sessions.mu.Lock()
	sessionCount := len(f.sessions.sessions)
	f.sessions.mu.Unlock()

	if sessionCount != 0 {
		t.Fatalf("expected no session recreation for blocked execution, got %d", sessionCount)
	}

	if _, ok := f.events.statusUpdates[execID]; ok {
		t.Fatal("did not expect status update for blocked execution with connected agent")
	}
}

func TestReassignIfNeededAssignsViaRealFlow(t *testing.T) {
	t.Run("orphaned running execution is assigned to connected agent without reconnect", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true

		execID := "exec-orphan-reassign"
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}

		m := f.manager(time.Hour)
		result := m.reassignIfNeeded(context.Background(), execID, "agent-1", "test")

		if !result {
			t.Fatal("expected reassignIfNeeded to return true")
		}

		status, ok := f.events.statusUpdates[execID]
		if !ok {
			t.Fatal("expected status update for orphaned execution")
		}
		if status != domain.ExecutionPending {
			t.Fatalf("expected status pending, got %s", status)
		}

		f.assigner.mu.Lock()
		var foundAssignment bool
		for _, a := range f.assigner.assigned {
			if a.ExecutionID == execID && a.AgentID == "agent-1" {
				foundAssignment = true
			}
		}
		f.assigner.mu.Unlock()

		if !foundAssignment {
			t.Fatal("expected TryAssignExecution to be called for the orphaned execution")
		}

		f.agentHub.mu.Lock()
		for _, msg := range f.agentHub.sent {
			if msg.Type == "execution.pending" {
				t.Fatal("should not send execution.pending; must use TryAssignExecution instead")
			}
		}
		f.agentHub.mu.Unlock()
	})

	t.Run("pending execution with connected agent is assigned during recovery", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true

		execID := "exec-pending-recover"
		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
		}

		m := f.manager(time.Hour)
		m.RecoverActiveExecutions(context.Background())

		f.assigner.mu.Lock()
		var foundAssignment bool
		for _, a := range f.assigner.assigned {
			if a.ExecutionID == execID && a.AgentID == "agent-1" {
				foundAssignment = true
			}
		}
		f.assigner.mu.Unlock()

		if !foundAssignment {
			t.Fatal("expected TryAssignExecution to be called during recovery for pending execution with connected agent")
		}
	})

	t.Run("no assignment when agent is disconnected", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = false

		execID := "exec-orphan-no-agent"
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}

		m := f.manager(time.Hour)
		result := m.reassignIfNeeded(context.Background(), execID, "agent-1", "test")

		if result {
			t.Fatal("expected reassignIfNeeded to return false when no agent connected")
		}

		f.assigner.mu.Lock()
		assignCount := len(f.assigner.assigned)
		f.assigner.mu.Unlock()

		if assignCount != 0 {
			t.Fatalf("expected no assignment when agent not connected, got %d", assignCount)
		}
	})

	t.Run("returns false when assignment fails", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true
		f.assigner.assignFail = true

		execID := "exec-assign-fail"
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2),
		}

		m := f.manager(time.Hour)
		result := m.reassignIfNeeded(context.Background(), execID, "agent-1", "test")

		if result {
			t.Fatal("expected reassignIfNeeded to return false when assignment fails")
		}

		f.assigner.mu.Lock()
		assignCount := len(f.assigner.assigned)
		f.assigner.mu.Unlock()

		if assignCount != 1 {
			t.Fatalf("expected 1 assignment attempt, got %d", assignCount)
		}
	})
}

func TestRecreateSessionForRunningReusesOriginalSessionID(t *testing.T) {
	t.Run("agent can use original session ID after session reaping", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true
		f.agentHub.pickResult = true
		f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}

		execID := "exec-running-recover"
		originalSessionID := "sess-original-42"

		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEventWithSession(originalSessionID, 2),
		}

		m := f.manager(time.Hour)
		m.recreateSessionForRunning(context.Background(), execID, "agent-1")

		// Verify the session was recreated with the original ID.
		f.sessions.mu.Lock()
		sess, ok := f.sessions.sessions[originalSessionID]
		f.sessions.mu.Unlock()

		if !ok {
			t.Fatal("expected session to be recreated with the original session ID")
		}
		if sess.ExecutionID != execID {
			t.Errorf("expected execution ID %s, got %s", execID, sess.ExecutionID)
		}
		if sess.AgentID != "agent-1" {
			t.Errorf("expected agent ID agent-1, got %s", sess.AgentID)
		}
		if sess.ConsumerID != "consumer-1" {
			t.Errorf("expected consumer ID consumer-1, got %s", sess.ConsumerID)
		}
	})

	t.Run("falls back to new ID when execution.started has no session ID", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true
		f.agentHub.pickResult = true
		f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}

		execID := "exec-running-no-sess"

		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEvent(2), // no session ID in payload
		}

		m := f.manager(time.Hour)
		m.recreateSessionForRunning(context.Background(), execID, "agent-1")

		f.sessions.mu.Lock()
		sessionCount := len(f.sessions.sessions)
		var foundForExec bool
		for _, s := range f.sessions.sessions {
			if s.ExecutionID == execID {
				foundForExec = true
			}
		}
		f.sessions.mu.Unlock()

		if sessionCount != 1 {
			t.Fatalf("expected 1 session created, got %d", sessionCount)
		}
		if !foundForExec {
			t.Fatal("expected a session to be created for the execution")
		}
	})

	t.Run("uses latest session ID when multiple execution.started events exist", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true
		f.agentHub.pickResult = true
		f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-3"}

		execID := "exec-reassigned"
		staleSessionID := "sess-stale-first"
		latestSessionID := "sess-latest-second"

		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEventWithSession(staleSessionID, 2),
			{
				Type:     domain.EventAgentTimeout,
				Payload:  mustMarshal(domain.AgentTimeoutPayload{SessionID: staleSessionID}),
				Sequence: 3,
			},
			{
				Type:     domain.EventExecutionReset,
				Payload:  mustMarshal(domain.ExecutionResetPayload{Reason: "recovery", FromStatus: "running"}),
				Sequence: 4,
			},
			startedEventWithSession(latestSessionID, 5),
		}

		m := f.manager(time.Hour)
		m.recreateSessionForRunning(context.Background(), execID, "agent-1")

		f.sessions.mu.Lock()
		_, hasLatest := f.sessions.sessions[latestSessionID]
		_, hasStale := f.sessions.sessions[staleSessionID]
		f.sessions.mu.Unlock()

		if !hasLatest {
			t.Fatal("expected session to be recreated with the latest session ID")
		}
		if hasStale {
			t.Fatal("should not recreate session with the stale first session ID")
		}
	})

	t.Run("agent lookup with original session ID succeeds after reaping", func(t *testing.T) {
		f := newTestFixture()
		f.agentHub.hasConn = true
		f.agentHub.pickResult = true
		f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}
		f.sessions.deletedExpired = 1

		execID := "exec-agent-recovery"
		originalSessionID := "sess-agent-known"

		f.events.activeIDs = []string{execID}
		f.events.events[execID] = []domain.Event{
			createdEvent("agent-1", 1),
			startedEventWithSession(originalSessionID, 2),
		}

		m := f.manager(time.Hour)
		m.reapSessions(context.Background())

		// Simulate what the agent would do: look up its session by the
		// original session ID that it received via execution.assigned.
		sess, found, err := f.sessions.Get(context.Background(), originalSessionID)
		if err != nil {
			t.Fatalf("unexpected error looking up session: %v", err)
		}
		if !found {
			t.Fatal("agent's original session ID should resolve after session recreation")
		}
		if sess.ExecutionID != execID {
			t.Errorf("expected execution ID %s, got %s", execID, sess.ExecutionID)
		}
	})
}

func TestReapSessionsRunningExecutionRevalidatesUnderLock(t *testing.T) {
	// Simulates the race from issue #86: execution is running when the reaper
	// first checks, but HandleAgentDisconnect resets it to pending before
	// recreateSessionForRunning runs. The reaper must re-project under lock
	// and skip session recreation when the execution is no longer running.
	f := newTestFixture()
	f.sessions.deletedExpired = 1
	f.agentHub.hasConn = true
	f.agentHub.pickResult = true
	f.agentHub.connInfo = store.ConnInfo{ConsumerID: "consumer-1"}

	execID := "exec-running-race"
	f.events.activeIDs = []string{execID}
	f.events.events[execID] = []domain.Event{
		createdEvent("agent-1", 1),
		startedEvent(2),
	}

	// Use a locker that simulates HandleAgentDisconnect: when the lock is
	// acquired, the execution is reset to pending (adding a reset event and
	// updating status) before the reaper can re-project.
	locker := &callbackLocker{
		onAcquire: func() {
			f.events.mu.Lock()
			f.events.events[execID] = append(f.events.events[execID],
				domain.Event{
					Type:     domain.EventExecutionReset,
					Payload:  mustMarshal(domain.ExecutionResetPayload{Reason: "recovery", FromStatus: "running"}),
					Sequence: int64(len(f.events.events[execID]) + 1),
				},
			)
			f.events.mu.Unlock()
		},
	}

	logger := slog.Default()
	proj := projector.New(f.events, f.checkpoints, logger)
	m := NewManager(Deps{
		Events:           f.events,
		Sessions:         f.sessions,
		Checkpoints:      f.checkpoints,
		Signals:          f.signals,
		AgentHub:         f.agentHub,
		Locker:           locker,
		Projector:        proj,
		Emitter:          f.emitter,
		Assigner:         f.assigner,
		Logger:           logger,
		ExecutionTimeout: time.Hour,
	})

	m.reapSessions(context.Background())

	// No session should be created because the execution is no longer running.
	f.sessions.mu.Lock()
	sessionCount := len(f.sessions.sessions)
	f.sessions.mu.Unlock()

	if sessionCount != 0 {
		t.Fatalf("expected no session recreation when execution was reset to pending under lock, got %d sessions", sessionCount)
	}
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
