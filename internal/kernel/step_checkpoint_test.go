package kernel

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func newTestKernelWithCheckpoints() (*Kernel, *mockEventStore, *mockCheckpointStore, *mockSessionStore) {
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

	return k, events, checkpoints, sessions
}

func TestSubmitStepResultSuccessCreatesCheckpoint(t *testing.T) {
	k, _, checkpoints, sessions := newTestKernelWithCheckpoints()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "calculator",
		},
	})
	if err != nil {
		t.Fatalf("process intent: %v", err)
	}

	// Clear any checkpoints created by prior operations so we can
	// verify that SubmitStepResult creates one.
	delete(checkpoints.checkpoints, execID)

	err = k.SubmitStepResult(ctx, StepResultRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		StepID:      result.StepID,
		Success:     true,
		Data:        json.RawMessage(`{"answer":42}`),
	})
	if err != nil {
		t.Fatalf("submit step result: %v", err)
	}

	cp, found := checkpoints.checkpoints[execID]
	if !found {
		t.Fatal("expected checkpoint after successful step completion, but none was saved")
	}
	if cp.Sequence == 0 {
		t.Fatal("checkpoint sequence should be > 0")
	}
}

func TestSubmitStepResultFailureDoesNotCheckpoint(t *testing.T) {
	k, _, checkpoints, sessions := newTestKernelWithCheckpoints()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "calculator",
		},
	})
	if err != nil {
		t.Fatalf("process intent: %v", err)
	}

	// Clear checkpoints to isolate the failure path.
	delete(checkpoints.checkpoints, execID)

	err = k.SubmitStepResult(ctx, StepResultRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		StepID:      result.StepID,
		Success:     false,
		Error:       "something went wrong",
	})
	if err != nil {
		t.Fatalf("submit step result: %v", err)
	}

	// EventStepFailed is NOT in ShouldCheckpoint, so no checkpoint expected.
	if _, found := checkpoints.checkpoints[execID]; found {
		t.Fatal("did not expect checkpoint after step failure (EventStepFailed is not checkpoint-worthy)")
	}
}

func TestSubmitJobResultSuccessCreatesCheckpoint(t *testing.T) {
	k, _, checkpoints, sessions := newTestKernelWithCheckpoints()
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
		t.Fatalf("process intent: %v", err)
	}

	// Clear checkpoints to isolate the job result path.
	delete(checkpoints.checkpoints, execID)

	err = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     true,
		Data:        json.RawMessage(`{"results":["a","b"]}`),
	})
	if err != nil {
		t.Fatalf("submit job result: %v", err)
	}

	cp, found := checkpoints.checkpoints[execID]
	if !found {
		t.Fatal("expected checkpoint after successful job completion, but none was saved")
	}
	if cp.Sequence == 0 {
		t.Fatal("checkpoint sequence should be > 0")
	}
}

func TestSubmitJobResultFailureNoRetryDoesNotCheckpoint(t *testing.T) {
	k, _, checkpoints, sessions := newTestKernelWithCheckpoints()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "calculator",
			Remote: true,
		},
	})
	if err != nil {
		t.Fatalf("process intent: %v", err)
	}

	delete(checkpoints.checkpoints, execID)

	err = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     false,
		Error:       "division by zero",
		Retryable:   false,
	})
	if err != nil {
		t.Fatalf("submit job result: %v", err)
	}

	// EventStepFailed is NOT in ShouldCheckpoint, so no checkpoint expected.
	if _, found := checkpoints.checkpoints[execID]; found {
		t.Fatal("did not expect checkpoint after non-retryable job failure")
	}
}
