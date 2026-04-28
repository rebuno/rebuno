package projector

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

type stubEventStore struct{}

func (s stubEventStore) Append(context.Context, domain.Event) error        { return nil }
func (s stubEventStore) AppendBatch(context.Context, []domain.Event) error { return nil }
func (s stubEventStore) GetByExecution(context.Context, string, int64, int) ([]domain.Event, error) {
	return nil, nil
}
func (s stubEventStore) GetLatestSequence(context.Context, string) (int64, error) { return 0, nil }
func (s stubEventStore) ListActiveExecutionIDs(context.Context) ([]string, error) { return nil, nil }
func (s stubEventStore) ListExecutions(context.Context, domain.ExecutionFilter, string, int) ([]domain.ExecutionSummary, string, error) {
	return nil, "", nil
}
func (s stubEventStore) GetExecution(context.Context, string) (*domain.ExecutionSummary, error) {
	return nil, nil
}
func (s stubEventStore) CreateExecution(context.Context, string, string, map[string]string) error {
	return nil
}
func (s stubEventStore) UpdateExecutionStatus(context.Context, string, domain.ExecutionStatus) error {
	return nil
}
func (s stubEventStore) DeleteExecution(context.Context, string) error { return nil }
func (s stubEventStore) ListTerminalExecutions(context.Context, int64, int) ([]string, error) {
	return nil, nil
}

type stubCheckpointStore struct{}

func (s stubCheckpointStore) Get(context.Context, string) (*domain.Checkpoint, bool, error) {
	return nil, false, nil
}
func (s stubCheckpointStore) Save(context.Context, domain.Checkpoint) error { return nil }
func (s stubCheckpointStore) Delete(context.Context, string) error          { return nil }

func makeEvent(executionID string, seq int64, eventType domain.EventType, payload any) *domain.Event {
	data, _ := json.Marshal(payload)
	return &domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: executionID,
		Type:        eventType,
		Payload:     data,
		Timestamp:   time.Now(),
		Sequence:    seq,
	}
}

func makeStepEvent(executionID, stepID string, seq int64, eventType domain.EventType, payload any) *domain.Event {
	evt := makeEvent(executionID, seq, eventType, payload)
	evt.StepID = stepID
	return evt
}

func emptyState() *domain.ExecutionState {
	return &domain.ExecutionState{
		Steps: make(map[string]*domain.Step),
	}
}

func TestExecutionLifecycle(t *testing.T) {
	state := emptyState()
	execID := "exec-1"

	err := applyExecutionCreated(state, makeEvent(execID, 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{
			AgentID: "agent-1",
			Input:   json.RawMessage(`{"prompt":"hello"}`),
			Labels:  map[string]string{"env": "test"},
		}))
	if err != nil {
		t.Fatalf("applyExecutionCreated: %v", err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		t.Errorf("expected pending, got %s", state.Execution.Status)
	}
	if state.Execution.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", state.Execution.AgentID)
	}
	if state.AgentID != "agent-1" {
		t.Errorf("expected top-level AgentID agent-1, got %s", state.AgentID)
	}
	if state.Execution.Labels["env"] != "test" {
		t.Errorf("expected label env=test, got %v", state.Execution.Labels)
	}

	err = applyExecutionStarted(state, makeEvent(execID, 2, domain.EventExecutionStarted, nil))
	if err != nil {
		t.Fatalf("applyExecutionStarted: %v", err)
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Errorf("expected running, got %s", state.Execution.Status)
	}

	err = applyExecutionBlocked(state, makeEvent(execID, 3, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "signal", Ref: "approval"}))
	if err != nil {
		t.Fatalf("applyExecutionBlocked: %v", err)
	}
	if state.Execution.Status != domain.ExecutionBlocked {
		t.Errorf("expected blocked, got %s", state.Execution.Status)
	}
	if state.BlockedReason != "signal" {
		t.Errorf("expected blocked reason signal, got %s", state.BlockedReason)
	}
	if state.BlockedRef != "approval" {
		t.Errorf("expected blocked ref approval, got %s", state.BlockedRef)
	}

	err = applyExecutionResumed(state, makeEvent(execID, 4, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "signal_received"}))
	if err != nil {
		t.Fatalf("applyExecutionResumed: %v", err)
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Errorf("expected running after resume, got %s", state.Execution.Status)
	}
	if state.BlockedReason != "" {
		t.Errorf("expected blocked reason cleared, got %s", state.BlockedReason)
	}
	if state.BlockedRef != "" {
		t.Errorf("expected blocked ref cleared, got %s", state.BlockedRef)
	}

	err = applyExecutionCompleted(state, makeEvent(execID, 5, domain.EventExecutionCompleted,
		domain.ExecutionCompletedPayload{Output: json.RawMessage(`{"result":"done"}`)}))
	if err != nil {
		t.Fatalf("applyExecutionCompleted: %v", err)
	}
	if state.Execution.Status != domain.ExecutionCompleted {
		t.Errorf("expected completed, got %s", state.Execution.Status)
	}
	if string(state.Execution.Output) != `{"result":"done"}` {
		t.Errorf("unexpected output: %s", state.Execution.Output)
	}
}

func TestExecutionFailed(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	err := applyExecutionFailed(state, makeEvent("e1", 3, domain.EventExecutionFailed, nil))
	if err != nil {
		t.Fatalf("applyExecutionFailed: %v", err)
	}
	if state.Execution.Status != domain.ExecutionFailed {
		t.Errorf("expected failed, got %s", state.Execution.Status)
	}
}

func TestExecutionCancelled(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))

	err := applyExecutionCancelled(state, makeEvent("e1", 2, domain.EventExecutionCancelled, nil))
	if err != nil {
		t.Fatalf("applyExecutionCancelled: %v", err)
	}
	if state.Execution.Status != domain.ExecutionCancelled {
		t.Errorf("expected cancelled, got %s", state.Execution.Status)
	}
}

func TestStepLifecycle(t *testing.T) {
	state := emptyState()
	execID := "exec-1"
	stepID := "step-1"

	err := applyStepCreated(state, makeStepEvent(execID, stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{
			ToolID:      "web_search",
			ToolVersion: 1,
			Arguments:   json.RawMessage(`{"query":"test"}`),
			MaxAttempts: 3,
			Attempt:     1,
		}))
	if err != nil {
		t.Fatalf("applyStepCreated: %v", err)
	}
	step := state.Steps[stepID]
	if step == nil {
		t.Fatal("step not created")
	}
	if step.Status != domain.StepPending {
		t.Errorf("expected pending, got %s", step.Status)
	}
	if step.ToolID != "web_search" {
		t.Errorf("expected tool web_search, got %s", step.ToolID)
	}
	if state.ActiveSteps[stepID] != step {
		t.Error("expected step to be in ActiveSteps")
	}

	deadline := time.Now().Add(5 * time.Minute)
	err = applyStepDispatched(state, makeStepEvent(execID, stepID, 2, domain.EventStepDispatched,
		domain.StepDispatchedPayload{RunnerID: "runner-1", JobID: "job-1", Deadline: deadline}))
	if err != nil {
		t.Fatalf("applyStepDispatched: %v", err)
	}
	if step.Status != domain.StepDispatched {
		t.Errorf("expected dispatched, got %s", step.Status)
	}
	if step.RunnerID != "runner-1" {
		t.Errorf("expected runner-1, got %s", step.RunnerID)
	}

	err = applyStepStarted(state, makeStepEvent(execID, stepID, 3, domain.EventStepStarted,
		domain.StepStartedPayload{RunnerID: "runner-1"}))
	if err != nil {
		t.Fatalf("applyStepStarted: %v", err)
	}
	if step.Status != domain.StepRunning {
		t.Errorf("expected running, got %s", step.Status)
	}

	err = applyStepCompleted(state, makeStepEvent(execID, stepID, 4, domain.EventStepCompleted,
		domain.StepCompletedPayload{Result: json.RawMessage(`{"data":"found"}`)}))
	if err != nil {
		t.Fatalf("applyStepCompleted: %v", err)
	}
	if step.Status != domain.StepSucceeded {
		t.Errorf("expected succeeded, got %s", step.Status)
	}
	if string(step.Result) != `{"data":"found"}` {
		t.Errorf("unexpected result: %s", step.Result)
	}
	if len(state.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(state.History))
	}
	if state.History[0].ToolID != "web_search" {
		t.Errorf("expected history tool web_search, got %s", state.History[0].ToolID)
	}
}

func TestStepFailed(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1", Attempt: 1}))

	err := applyStepFailed(state, makeStepEvent("e1", stepID, 2, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "connection timeout", Retryable: true}))
	if err != nil {
		t.Fatalf("applyStepFailed: %v", err)
	}
	step := state.Steps[stepID]
	if step.Status != domain.StepFailed {
		t.Errorf("expected failed, got %s", step.Status)
	}
	if step.Error != "connection timeout" {
		t.Errorf("expected error message, got %s", step.Error)
	}
	if !step.Retryable {
		t.Error("expected retryable=true")
	}
}

func TestStepTimedOut(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1"}))

	err := applyStepTimedOut(state, makeStepEvent("e1", stepID, 2, domain.EventStepTimedOut, nil))
	if err != nil {
		t.Fatalf("applyStepTimedOut: %v", err)
	}
	if state.Steps[stepID].Status != domain.StepTimedOut {
		t.Errorf("expected timed_out, got %s", state.Steps[stepID].Status)
	}
}

func TestSignalReceived(t *testing.T) {
	state := emptyState()
	err := applySignalReceived(state, makeEvent("e1", 1, domain.EventSignalReceived,
		domain.SignalReceivedPayload{
			SignalType: "approval",
			Payload:    json.RawMessage(`{"approved":true}`),
		}))
	if err != nil {
		t.Fatalf("applySignalReceived: %v", err)
	}
	if len(state.PendingSignals) != 1 {
		t.Fatalf("expected 1 pending signal, got %d", len(state.PendingSignals))
	}
	if state.PendingSignals[0].SignalType != "approval" {
		t.Errorf("expected signal type approval, got %s", state.PendingSignals[0].SignalType)
	}
}

func TestUnknownStepTaintsState(t *testing.T) {
	state := emptyState()

	err := applyStepDispatched(state, makeStepEvent("e1", "nonexistent", 1, domain.EventStepDispatched,
		domain.StepDispatchedPayload{RunnerID: "r1", Deadline: time.Now()}))
	if err == nil {
		t.Fatal("expected error for unknown step")
	}

	err = applyStepStarted(state, makeStepEvent("e1", "nonexistent", 2, domain.EventStepStarted,
		domain.StepStartedPayload{RunnerID: "r1"}))
	if err == nil {
		t.Fatal("expected error for unknown step")
	}

	err = applyStepCompleted(state, makeStepEvent("e1", "nonexistent", 3, domain.EventStepCompleted,
		domain.StepCompletedPayload{Result: json.RawMessage(`{}`)}))
	if err == nil {
		t.Fatal("expected error for unknown step")
	}
}

func TestCorruptPayloadTaintsState(t *testing.T) {
	state := emptyState()

	evt := &domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: "e1",
		Type:        domain.EventExecutionCreated,
		Payload:     json.RawMessage(`{invalid json`),
		Timestamp:   time.Now(),
		Sequence:    1,
	}
	err := applyExecutionCreated(state, evt)
	if err == nil {
		t.Fatal("expected error for corrupt payload")
	}
}

func TestHandlerRegistration(t *testing.T) {
	p := New(stubEventStore{}, stubCheckpointStore{}, nil)

	expectedTypes := []domain.EventType{
		domain.EventExecutionCreated, domain.EventExecutionStarted,
		domain.EventExecutionBlocked, domain.EventExecutionResumed,
		domain.EventExecutionCompleted, domain.EventExecutionFailed,
		domain.EventExecutionCancelled, domain.EventExecutionReset,
		domain.EventStepCreated, domain.EventStepDispatched,
		domain.EventStepStarted, domain.EventStepCompleted,
		domain.EventStepFailed, domain.EventStepTimedOut,
		domain.EventStepRetried, domain.EventStepCancelled,
		domain.EventStepApprovalRequired,
		domain.EventSignalReceived,
		domain.EventIntentAccepted, domain.EventIntentDenied,
		domain.EventAgentTimeout,
	}

	for _, et := range expectedTypes {
		if _, ok := p.handlers[et]; !ok {
			t.Errorf("no handler registered for %s", et)
		}
	}
}

func TestShouldCheckpoint(t *testing.T) {
	checkpointable := []domain.EventType{
		domain.EventStepCompleted,
		domain.EventExecutionStarted,
		domain.EventExecutionBlocked,
		domain.EventExecutionResumed,
		domain.EventExecutionCompleted,
		domain.EventExecutionFailed,
		domain.EventExecutionCancelled,
	}
	for _, et := range checkpointable {
		if !ShouldCheckpoint(et) {
			t.Errorf("expected ShouldCheckpoint=true for %s", et)
		}
	}

	nonCheckpointable := []domain.EventType{
		domain.EventExecutionCreated,
		domain.EventStepCreated,
		domain.EventStepDispatched,
		domain.EventStepStarted,
		domain.EventStepFailed,
		domain.EventSignalReceived,
	}
	for _, et := range nonCheckpointable {
		if ShouldCheckpoint(et) {
			t.Errorf("expected ShouldCheckpoint=false for %s", et)
		}
	}
}

func TestExecutionReset(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
	applyExecutionBlocked(state, makeEvent("e1", 3, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "signal", Ref: "approval"}))

	err := applyExecutionReset(state, makeEvent("e1", 4, domain.EventExecutionReset, nil))
	if err != nil {
		t.Fatalf("applyExecutionReset: %v", err)
	}
	if state.Execution.Status != domain.ExecutionPending {
		t.Errorf("expected pending after reset, got %s", state.Execution.Status)
	}
	if state.BlockedReason != "" {
		t.Errorf("expected blocked reason cleared, got %s", state.BlockedReason)
	}
	if state.BlockedRef != "" {
		t.Errorf("expected blocked ref cleared, got %s", state.BlockedRef)
	}
}

func TestExecutionBlockedWithApproval(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	err := applyExecutionBlocked(state, makeEvent("e1", 3, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{
			Reason:    "approval",
			Ref:       "step-1",
			ToolID:    "dangerous_tool",
			Arguments: json.RawMessage(`{"arg":"val"}`),
			Remote:    true,
		}))
	if err != nil {
		t.Fatalf("applyExecutionBlocked: %v", err)
	}
	if len(state.PendingApprovals) == 0 {
		t.Fatal("expected PendingApprovals to be non-empty")
	}
	approval := state.PendingApprovals["step-1"]
	if approval == nil {
		t.Fatal("expected approval for step-1")
	}
	if approval.StepID != "step-1" {
		t.Errorf("expected step-1, got %s", approval.StepID)
	}
	if approval.ToolID != "dangerous_tool" {
		t.Errorf("expected dangerous_tool, got %s", approval.ToolID)
	}
	if !approval.Remote {
		t.Error("expected Remote=true")
	}
}

func TestExecutionBlockedNonApprovalClearsPendingApproval(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	// First block with approval
	applyExecutionBlocked(state, makeEvent("e1", 3, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "t1"}))
	if len(state.PendingApprovals) == 0 {
		t.Fatal("expected PendingApprovals non-empty")
	}

	applyExecutionResumed(state, makeEvent("e1", 4, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "approved"}))

	// Block again with non-approval reason — approvals should have been cleared by resume
	applyExecutionBlocked(state, makeEvent("e1", 5, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "signal", Ref: "wait"}))
	if len(state.PendingApprovals) != 0 {
		t.Fatal("expected PendingApprovals empty after resume + non-approval block")
	}
}

func TestStepCancelled(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1"}))

	err := applyStepCancelled(state, makeStepEvent("e1", stepID, 2, domain.EventStepCancelled, nil))
	if err != nil {
		t.Fatalf("applyStepCancelled: %v", err)
	}
	if state.Steps[stepID].Status != domain.StepCancelled {
		t.Errorf("expected cancelled, got %s", state.Steps[stepID].Status)
	}
	if len(state.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(state.History))
	}
	if state.History[0].Error != "execution cancelled" {
		t.Errorf("expected 'execution cancelled' error in history, got %q", state.History[0].Error)
	}
}

func TestStepCancelledWithCustomReason(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1"}))

	err := applyStepCancelled(state, makeStepEvent("e1", stepID, 2, domain.EventStepCancelled,
		domain.StepCancelledPayload{Reason: "agent disconnected"}))
	if err != nil {
		t.Fatalf("applyStepCancelled: %v", err)
	}
	if state.Steps[stepID].Status != domain.StepCancelled {
		t.Errorf("expected cancelled, got %s", state.Steps[stepID].Status)
	}
	if len(state.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(state.History))
	}
	if state.History[0].Error != "agent disconnected" {
		t.Errorf("expected 'agent disconnected' error in history, got %q", state.History[0].Error)
	}
}

func TestStepFailedAddsToHistory(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1", Attempt: 1}))

	applyStepFailed(state, makeStepEvent("e1", stepID, 2, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "timeout", Retryable: false}))

	if len(state.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(state.History))
	}
	if state.History[0].Status != domain.StepFailed {
		t.Errorf("expected failed status in history, got %s", state.History[0].Status)
	}
	if state.History[0].Error != "timeout" {
		t.Errorf("expected 'timeout' error in history, got %q", state.History[0].Error)
	}
}

func TestStepTimedOutAddsToHistory(t *testing.T) {
	state := emptyState()
	stepID := "step-1"
	applyStepCreated(state, makeStepEvent("e1", stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "t1"}))

	applyStepTimedOut(state, makeStepEvent("e1", stepID, 2, domain.EventStepTimedOut, nil))

	if len(state.History) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(state.History))
	}
	if state.History[0].Error != "step timed out" {
		t.Errorf("expected 'step timed out' error in history, got %q", state.History[0].Error)
	}
}

func TestUnknownStepFailedTimedOutCancelled(t *testing.T) {
	state := emptyState()

	err := applyStepFailed(state, makeStepEvent("e1", "nonexistent", 1, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err"}))
	if err == nil {
		t.Fatal("expected error for unknown step on step.failed")
	}

	err = applyStepTimedOut(state, makeStepEvent("e1", "nonexistent", 2, domain.EventStepTimedOut, nil))
	if err == nil {
		t.Fatal("expected error for unknown step on step.timed_out")
	}

	err = applyStepCancelled(state, makeStepEvent("e1", "nonexistent", 3, domain.EventStepCancelled, nil))
	if err == nil {
		t.Fatal("expected error for unknown step on step.cancelled")
	}
}

func TestCorruptPayloadOnStepCreated(t *testing.T) {
	state := emptyState()
	evt := &domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: "e1",
		StepID:      "s1",
		Type:        domain.EventStepCreated,
		Payload:     json.RawMessage(`{invalid`),
		Timestamp:   time.Now(),
		Sequence:    1,
	}
	err := applyStepCreated(state, evt)
	if err == nil {
		t.Fatal("expected error for corrupt step.created payload")
	}
}

func TestCorruptPayloadOnExecutionBlocked(t *testing.T) {
	state := emptyState()
	evt := &domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: "e1",
		Type:        domain.EventExecutionBlocked,
		Payload:     json.RawMessage(`not-json`),
		Timestamp:   time.Now(),
		Sequence:    1,
	}
	err := applyExecutionBlocked(state, evt)
	if err == nil {
		t.Fatal("expected error for corrupt execution.blocked payload")
	}
}

func TestCorruptPayloadOnExecutionCompleted(t *testing.T) {
	state := emptyState()
	evt := &domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: "e1",
		Type:        domain.EventExecutionCompleted,
		Payload:     json.RawMessage(`broken`),
		Timestamp:   time.Now(),
		Sequence:    1,
	}
	err := applyExecutionCompleted(state, evt)
	if err == nil {
		t.Fatal("expected error for corrupt execution.completed payload")
	}
}

func TestMultipleStepsTracked(t *testing.T) {
	state := emptyState()

	applyStepCreated(state, makeStepEvent("e1", "s1", 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "tool-a"}))
	applyStepCreated(state, makeStepEvent("e1", "s2", 2, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "tool-b"}))

	if len(state.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(state.Steps))
	}
	if len(state.ActiveSteps) != 2 {
		t.Fatalf("expected 2 active steps, got %d", len(state.ActiveSteps))
	}
	if state.ActiveSteps["s1"] == nil || state.ActiveSteps["s2"] == nil {
		t.Error("expected both s1 and s2 in ActiveSteps")
	}
	if state.Steps["s1"].ToolID != "tool-a" {
		t.Errorf("expected s1 tool tool-a, got %s", state.Steps["s1"].ToolID)
	}
}

// --- Projector corruption recovery tests ---

// inMemEventStore is a simple in-memory event store for projector tests that
// need to exercise the Project method end-to-end (through replay).
type inMemEventStore struct {
	stubEventStore
	events map[string][]domain.Event
}

func newInMemEventStore() *inMemEventStore {
	return &inMemEventStore{events: make(map[string][]domain.Event)}
}

func (s *inMemEventStore) Append(_ context.Context, event domain.Event) error {
	seq := int64(len(s.events[event.ExecutionID]) + 1)
	event.Sequence = seq
	s.events[event.ExecutionID] = append(s.events[event.ExecutionID], event)
	return nil
}

func (s *inMemEventStore) AppendBatch(_ context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	execID := events[0].ExecutionID
	base := int64(len(s.events[execID]))
	for i := range events {
		events[i].Sequence = base + int64(i) + 1
		s.events[execID] = append(s.events[execID], events[i])
	}
	return nil
}

func (s *inMemEventStore) GetByExecution(_ context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	var result []domain.Event
	for _, e := range s.events[executionID] {
		if e.Sequence > afterSequence {
			result = append(result, e)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

// corruptCheckpointStore returns a corrupt checkpoint on first call, then nothing.
type corruptCheckpointStore struct {
	stubCheckpointStore
	executionID string
}

func (s *corruptCheckpointStore) Get(_ context.Context, executionID string) (*domain.Checkpoint, bool, error) {
	if executionID == s.executionID {
		return &domain.Checkpoint{
			ExecutionID: executionID,
			Sequence:    5,
			StateData:   json.RawMessage(`{corrupt data that is not valid json!`),
		}, true, nil
	}
	return nil, false, nil
}

func TestSequenceGapMarksTainted(t *testing.T) {
	es := newInMemEventStore()
	ctx := context.Background()
	execID := "exec-gap"

	// Create normal events at seq 1, 2.
	createdPayload, _ := json.Marshal(domain.ExecutionCreatedPayload{AgentID: "a1"})
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionCreated,
		Payload:     createdPayload,
		Timestamp:   time.Now(),
	})
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionStarted,
		Payload:     json.RawMessage(`null`),
		Timestamp:   time.Now(),
	})

	// Inject a gap: bump event at index 1 from seq 2 to seq 4.
	es.events[execID][1].Sequence = 4

	p := New(es, stubCheckpointStore{}, nil)
	state, err := p.Project(ctx, execID)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if !state.Tainted {
		t.Fatal("expected state to be tainted due to sequence gap")
	}
	if state.TaintedReason == "" {
		t.Fatal("expected tainted reason to be set")
	}
}

func TestCorruptCheckpointFallsBackToFullReplay(t *testing.T) {
	es := newInMemEventStore()
	ctx := context.Background()
	execID := "exec-corrupt-cp"

	// Add events.
	createdPayload, _ := json.Marshal(domain.ExecutionCreatedPayload{AgentID: "a1"})
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionCreated,
		Payload:     createdPayload,
		Timestamp:   time.Now(),
	})
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionStarted,
		Payload:     json.RawMessage(`null`),
		Timestamp:   time.Now(),
	})

	// Use a corrupt checkpoint store.
	cp := &corruptCheckpointStore{executionID: execID}
	p := New(es, cp, nil)

	state, err := p.Project(ctx, execID)
	if err != nil {
		t.Fatalf("project should succeed with full replay fallback, got: %v", err)
	}
	if state.Tainted {
		t.Fatal("state should not be tainted after successful full replay")
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after full replay, got %s", state.Execution.Status)
	}
	if state.Execution.AgentID != "a1" {
		t.Fatalf("expected agent_id a1, got %s", state.Execution.AgentID)
	}
}

func TestUnknownEventTypeDuringReplayContinues(t *testing.T) {
	es := newInMemEventStore()
	ctx := context.Background()
	execID := "exec-unknown-evt"

	// Create a normal event.
	createdPayload, _ := json.Marshal(domain.ExecutionCreatedPayload{AgentID: "a1"})
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionCreated,
		Payload:     createdPayload,
		Timestamp:   time.Now(),
	})

	// Inject an unknown event type.
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventType("unknown.future.event"),
		Payload:     json.RawMessage(`{}`),
		Timestamp:   time.Now(),
	})

	// Add another normal event after the unknown one.
	es.Append(ctx, domain.Event{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		Type:        domain.EventExecutionStarted,
		Payload:     json.RawMessage(`null`),
		Timestamp:   time.Now(),
	})

	p := New(es, stubCheckpointStore{}, nil)
	state, err := p.Project(ctx, execID)
	if err != nil {
		t.Fatalf("project should succeed despite unknown event type, got: %v", err)
	}
	// Should not be tainted — unknown events are warned but skipped.
	if state.Tainted {
		t.Fatal("state should not be tainted for unknown event type (only logged as warning)")
	}
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running (events after unknown type should still apply), got %s", state.Execution.Status)
	}
	if state.LastSequence != 3 {
		t.Fatalf("expected last sequence 3, got %d", state.LastSequence)
	}
}

func TestResumedClearsPendingApproval(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
	applyExecutionBlocked(state, makeEvent("e1", 3, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "t1"}))

	if len(state.PendingApprovals) == 0 {
		t.Fatal("expected PendingApprovals non-empty")
	}

	applyExecutionResumed(state, makeEvent("e1", 4, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "approved"}))

	if len(state.PendingApprovals) != 0 {
		t.Fatal("expected PendingApprovals cleared after resume")
	}
}

func TestResetClearsActiveStepsAndPendingApprovals(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
	applyStepCreated(state, makeStepEvent("e1", "s1", 3, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "tool-a", Attempt: 1}))

	if len(state.ActiveSteps) == 0 {
		t.Fatal("expected ActiveSteps to be non-empty before reset")
	}

	applyStepCancelled(state, makeStepEvent("e1", "s1", 4, domain.EventStepCancelled,
		domain.StepCancelledPayload{Reason: "agent disconnected"}))
	applyExecutionReset(state, makeEvent("e1", 5, domain.EventExecutionReset,
		domain.ExecutionResetPayload{Reason: "agent_disconnect", FromStatus: "running"}))

	if state.Execution.Status != domain.ExecutionPending {
		t.Fatalf("expected pending after reset, got %s", state.Execution.Status)
	}
	if state.HasActiveSteps() {
		t.Fatal("expected no active steps after reset")
	}
	if len(state.PendingApprovals) != 0 {
		t.Fatal("expected PendingApprovals empty after reset")
	}
}

func TestResumedBySignalRemovesConsumedSignal(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	// Receive multiple signals.
	applySignalReceived(state, makeEvent("e1", 3, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "signal-1", Payload: json.RawMessage(`{"n":1}`)}))
	applySignalReceived(state, makeEvent("e1", 4, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "signal-2", Payload: json.RawMessage(`{"n":2}`)}))
	applySignalReceived(state, makeEvent("e1", 5, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "signal-3", Payload: json.RawMessage(`{"n":3}`)}))

	if len(state.PendingSignals) != 3 {
		t.Fatalf("expected 3 pending signals, got %d", len(state.PendingSignals))
	}

	// Block on signal, then resume by signal-2.
	applyExecutionBlocked(state, makeEvent("e1", 6, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "signal", Ref: "signal-2"}))
	applyExecutionResumed(state, makeEvent("e1", 7, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "signal received: signal-2"}))

	if len(state.PendingSignals) != 2 {
		t.Fatalf("expected 2 pending signals after consuming signal-2, got %d", len(state.PendingSignals))
	}
	for _, s := range state.PendingSignals {
		if s.SignalType == "signal-2" {
			t.Fatal("signal-2 should have been removed from PendingSignals")
		}
	}
}

func TestResumedByNonSignalDoesNotAffectPendingSignals(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	applySignalReceived(state, makeEvent("e1", 3, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "signal-1", Payload: json.RawMessage(`{}`)}))

	applyExecutionBlocked(state, makeEvent("e1", 4, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "approval", Ref: "step-1", ToolID: "t1"}))
	applyExecutionResumed(state, makeEvent("e1", 5, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "approved"}))

	if len(state.PendingSignals) != 1 {
		t.Fatalf("expected 1 pending signal unchanged, got %d", len(state.PendingSignals))
	}
}

func TestResumedBySignalRemovesOnlyFirstMatch(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))

	// Two signals of the same type.
	applySignalReceived(state, makeEvent("e1", 3, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "dup", Payload: json.RawMessage(`{"n":1}`)}))
	applySignalReceived(state, makeEvent("e1", 4, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "dup", Payload: json.RawMessage(`{"n":2}`)}))

	if len(state.PendingSignals) != 2 {
		t.Fatalf("expected 2 pending signals, got %d", len(state.PendingSignals))
	}

	applyExecutionBlocked(state, makeEvent("e1", 5, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "signal", Ref: "dup"}))
	applyExecutionResumed(state, makeEvent("e1", 6, domain.EventExecutionResumed,
		domain.ExecutionResumedPayload{Reason: "signal received: dup"}))

	if len(state.PendingSignals) != 1 {
		t.Fatalf("expected 1 remaining pending signal, got %d", len(state.PendingSignals))
	}
	if state.PendingSignals[0].SignalType != "dup" {
		t.Errorf("expected remaining signal type dup, got %s", state.PendingSignals[0].SignalType)
	}
}

func TestTerminalStatesClearPendingSignals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		applyFunc func(*domain.ExecutionState, *domain.Event) error
		eventType domain.EventType
		payload   any
	}{
		{
			name:      "completed",
			applyFunc: applyExecutionCompleted,
			eventType: domain.EventExecutionCompleted,
			payload:   domain.ExecutionCompletedPayload{Output: json.RawMessage(`{}`)},
		},
		{
			name:      "failed",
			applyFunc: applyExecutionFailed,
			eventType: domain.EventExecutionFailed,
			payload:   nil,
		},
		{
			name:      "cancelled",
			applyFunc: applyExecutionCancelled,
			eventType: domain.EventExecutionCancelled,
			payload:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := emptyState()
			applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
				domain.ExecutionCreatedPayload{AgentID: "a1"}))
			applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
			applySignalReceived(state, makeEvent("e1", 3, domain.EventSignalReceived,
				domain.SignalReceivedPayload{SignalType: "s1", Payload: json.RawMessage(`{}`)}))
			applySignalReceived(state, makeEvent("e1", 4, domain.EventSignalReceived,
				domain.SignalReceivedPayload{SignalType: "s2", Payload: json.RawMessage(`{}`)}))

			if len(state.PendingSignals) != 2 {
				t.Fatalf("expected 2 pending signals before terminal, got %d", len(state.PendingSignals))
			}

			err := tc.applyFunc(state, makeEvent("e1", 5, tc.eventType, tc.payload))
			if err != nil {
				t.Fatalf("apply %s: %v", tc.name, err)
			}
			if len(state.PendingSignals) != 0 {
				t.Fatalf("expected PendingSignals cleared after %s, got %d", tc.name, len(state.PendingSignals))
			}
		})
	}
}

func TestResetClearsPendingSignals(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
	applySignalReceived(state, makeEvent("e1", 3, domain.EventSignalReceived,
		domain.SignalReceivedPayload{SignalType: "s1", Payload: json.RawMessage(`{}`)}))

	if len(state.PendingSignals) != 1 {
		t.Fatalf("expected 1 pending signal, got %d", len(state.PendingSignals))
	}

	applyExecutionReset(state, makeEvent("e1", 4, domain.EventExecutionReset,
		domain.ExecutionResetPayload{Reason: "recovery", FromStatus: "running"}))

	if len(state.PendingSignals) != 0 {
		t.Fatal("expected PendingSignals cleared after reset")
	}
}

func TestStepRetriedReAddsToActiveSteps(t *testing.T) {
	state := emptyState()
	execID := "exec-1"
	stepID := "step-1"

	applyStepCreated(state, makeStepEvent(execID, stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1, MaxAttempts: 3}))

	if !state.HasActiveSteps() {
		t.Fatal("expected active steps after creation")
	}

	applyStepFailed(state, makeStepEvent(execID, stepID, 2, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "connection timeout", Retryable: true}))

	if state.HasActiveSteps() {
		t.Fatal("expected no active steps after failure")
	}

	err := applyStepRetried(state, makeStepEvent(execID, stepID, 3, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2}))
	if err != nil {
		t.Fatalf("applyStepRetried: %v", err)
	}

	if !state.HasActiveSteps() {
		t.Fatal("expected HasActiveSteps=true after retry")
	}
	if state.ActiveSteps[stepID] == nil {
		t.Fatal("expected retried step in ActiveSteps map")
	}
	step := state.Steps[stepID]
	if step.Status != domain.StepPending {
		t.Errorf("expected pending after retry, got %s", step.Status)
	}
	if step.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", step.Attempt)
	}
	if step.Error != "" {
		t.Errorf("expected error cleared, got %q", step.Error)
	}
	if step.CompletedAt != nil {
		t.Error("expected CompletedAt cleared")
	}
	if step.Retryable {
		t.Error("expected Retryable=false after retry")
	}
}

func TestStepRetriedThenCompletedClearsActiveSteps(t *testing.T) {
	state := emptyState()
	execID := "exec-1"
	stepID := "step-1"

	applyStepCreated(state, makeStepEvent(execID, stepID, 1, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "web_search", Attempt: 1, MaxAttempts: 3}))
	applyStepFailed(state, makeStepEvent(execID, stepID, 2, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "timeout", Retryable: true}))
	applyStepRetried(state, makeStepEvent(execID, stepID, 3, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2}))

	if !state.HasActiveSteps() {
		t.Fatal("expected active steps after retry")
	}

	applyStepCompleted(state, makeStepEvent(execID, stepID, 4, domain.EventStepCompleted,
		domain.StepCompletedPayload{Result: json.RawMessage(`{"ok":true}`)}))

	if state.HasActiveSteps() {
		t.Fatal("expected no active steps after retried step completes")
	}
}

func TestStepRetriedUnknownStepReturnsError(t *testing.T) {
	state := emptyState()
	err := applyStepRetried(state, makeStepEvent("e1", "nonexistent", 1, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2}))
	if err == nil {
		t.Fatal("expected error for unknown step on step.retried")
	}
}

func TestResetClearsBlockedApprovalState(t *testing.T) {
	state := emptyState()
	applyExecutionCreated(state, makeEvent("e1", 1, domain.EventExecutionCreated,
		domain.ExecutionCreatedPayload{AgentID: "a1"}))
	applyExecutionStarted(state, makeEvent("e1", 2, domain.EventExecutionStarted, nil))
	applyStepCreated(state, makeStepEvent("e1", "s1", 3, domain.EventStepCreated,
		domain.StepCreatedPayload{ToolID: "tool-a", Attempt: 1}))
	applyExecutionBlocked(state, makeEvent("e1", 4, domain.EventExecutionBlocked,
		domain.ExecutionBlockedPayload{Reason: "approval", Ref: "s1", ToolID: "tool-a"}))

	if len(state.PendingApprovals) == 0 {
		t.Fatal("expected PendingApprovals non-empty before reset")
	}
	if len(state.ActiveSteps) == 0 {
		t.Fatal("expected ActiveSteps non-empty before reset")
	}

	applyStepCancelled(state, makeStepEvent("e1", "s1", 5, domain.EventStepCancelled,
		domain.StepCancelledPayload{Reason: "agent disconnected"}))
	applyExecutionReset(state, makeEvent("e1", 6, domain.EventExecutionReset,
		domain.ExecutionResetPayload{Reason: "recovery", FromStatus: "blocked"}))

	if state.Execution.Status != domain.ExecutionPending {
		t.Fatalf("expected pending, got %s", state.Execution.Status)
	}
	if state.HasActiveSteps() {
		t.Fatal("expected no active steps after reset")
	}
	if len(state.PendingApprovals) != 0 {
		t.Fatal("expected PendingApprovals empty after reset")
	}
	if state.BlockedReason != "" {
		t.Fatalf("expected empty blocked reason, got %s", state.BlockedReason)
	}
}
