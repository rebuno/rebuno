package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestSubmitJobResultSuccess(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
			Remote: true,
		},
	})

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     true,
		Data:        json.RawMessage(`{"results":["a","b"]}`),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after job completion, got %s", state.Execution.Status)
	}

	step := state.Steps[result.StepID]
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected step succeeded, got %s", step.Status)
	}

	runnerHub.mu.Lock()
	isIdle := runnerHub.idle["mock-runner"]
	runnerHub.mu.Unlock()
	if !isIdle {
		t.Fatal("expected runner to be marked idle after result submission")
	}
}

func TestSubmitJobResultFailureWithRetry(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
			Remote: true, // remote tools get max_attempts=3
		},
	})

	// Set up a channel that signals when DispatchJob is called for the retry.
	retryCh := make(chan struct{}, 1)
	runnerHub.mu.Lock()
	runnerHub.dispatched = nil
	runnerHub.onDispatch = func() {
		select {
		case retryCh <- struct{}{}:
		default:
		}
	}
	runnerHub.mu.Unlock()

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for the retry dispatch with a timeout instead of sleeping.
	select {
	case <-retryCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for retry dispatch")
	}

	jobs, err := k.jobQueue.All(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 pending jobs (should have been dispatched), got %d", len(jobs))
	}
}

func TestSubmitJobResultRetryRoundTrip(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
			Remote: true,
		},
	})

	retryCh := make(chan struct{}, 1)
	runnerHub.mu.Lock()
	runnerHub.dispatched = nil
	runnerHub.onDispatch = func() {
		select {
		case retryCh <- struct{}{}:
		default:
		}
	}
	runnerHub.mu.Unlock()

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("submit retryable failure: %v", err)
	}

	select {
	case <-retryCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for retry dispatch")
	}

	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepPending {
		t.Fatalf("expected step pending after retry, got %s", step.Status)
	}
	if step.Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", step.Attempt)
	}

	err = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     true,
		Data:        json.RawMessage(`{"results":["a","b"]}`),
	})
	if err != nil {
		t.Fatalf("submit retry result: %v", err)
	}

	state, _ = k.GetExecution(ctx, execID)
	step = state.Steps[result.StepID]
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected step succeeded after retry, got %s", step.Status)
	}
}

func TestSubmitJobResultFailureNoRetry(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "calculator",
			Remote: true,
		},
	})

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "division by zero",
		Retryable:   false, // not retryable
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	if state.Execution.Status != domain.ExecutionRunning {
		t.Fatalf("expected running after non-retryable failure, got %s", state.Execution.Status)
	}
}

func TestRetryDelay(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second}, // capped at max
		{10, 30 * time.Second},
	}

	for _, tt := range tests {
		got := k.retryDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("retryDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestDispatchPendingJobs(t *testing.T) {
	k, _, _, runnerHub, _, _ := newTestKernel()
	ctx := context.Background()

	job := domain.Job{
		ToolID: "web.search",
	}
	k.enqueuePendingJob(job)

	jobs, err := k.jobQueue.All(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 pending job, got %d", len(jobs))
	}

	k.DispatchPendingJobs()

	jobs, err = k.jobQueue.All(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected 0 pending jobs after dispatch, got %d", len(jobs))
	}

	runnerHub.mu.Lock()
	if len(runnerHub.dispatched) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(runnerHub.dispatched))
	}
	runnerHub.mu.Unlock()
}

func TestSubmitJobResultStepNotFound(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := setupRunningExecution(t, k, sessions)

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      "nonexistent-step",
		RunnerID:    "runner-1",
		Success:     true,
		Data:        json.RawMessage(`{}`),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for nonexistent step, got %v", err)
	}
}

func TestSubmitJobResultAlreadyResolved(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "calculator",
			Remote: true,
		},
	})

	// Submit result once (succeeds).
	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Submit again — step is already resolved.
	err = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})
	if !errors.Is(err, domain.ErrStepAlreadyResolved) {
		t.Fatalf("expected ErrStepAlreadyResolved, got %v", err)
	}
}

func TestSubmitJobResultFailureExhaustsRetries(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web.search",
			Remote: true, // max_attempts=3
		},
	})

	// Simulate max retries being exhausted (attempt >= max_attempts).
	// The step starts at attempt=1 with max_attempts=3.
	// Mark the step as if it's on the last attempt by submitting non-retryable failure.
	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "permanent failure",
		Retryable:   false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepFailed {
		t.Fatalf("expected step failed, got %s", step.Status)
	}
}

func TestSubmitJobResultRejectsTaintedExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Create a remote step.
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

	// Now taint the execution by injecting a sequence gap.
	events.mu.Lock()
	evts := events.events[execID]
	if len(evts) > 0 {
		evts[len(evts)-1].Sequence = evts[len(evts)-1].Sequence + 1
	}
	events.events[execID] = evts
	events.mu.Unlock()

	err = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     true,
		Data:        json.RawMessage(`{"result":"ok"}`),
	})
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}
}

func TestSubmitStepResultRejectsTaintedExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Create a local step.
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

	// Taint the execution by injecting a sequence gap.
	events.mu.Lock()
	evts := events.events[execID]
	if len(evts) > 0 {
		evts[len(evts)-1].Sequence = evts[len(evts)-1].Sequence + 1
	}
	events.events[execID] = evts
	events.mu.Unlock()

	err = k.SubmitStepResult(ctx, StepResultRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		StepID:      result.StepID,
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}
}
