package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
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
			ToolID: "web_search",
			Remote: true,
		},
	})

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
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
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true, // remote tools get max_attempts=3
		},
	})

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The retry job should be persisted in the queue immediately with a NotBefore delay.
	jobs, err := k.jobQueue.All(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 pending retry job in queue, got %d", len(jobs))
	}
	if jobs[0].StepID != result.StepID {
		t.Fatalf("expected retry job for step %s, got %s", result.StepID, jobs[0].StepID)
	}
	if jobs[0].Attempt != 2 {
		t.Fatalf("expected retry attempt 2, got %d", jobs[0].Attempt)
	}
	if jobs[0].NotBefore.IsZero() {
		t.Fatal("expected retry job to have non-zero NotBefore")
	}
}

func TestSubmitJobResultRetryRoundTrip(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("submit retryable failure: %v", err)
	}

	// Retry job is persisted in queue. Verify step state.
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
		ConsumerID:  "mock-consumer",
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
		ConsumerID:  "mock-consumer",
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
		ToolID: "web_search",
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
		ConsumerID:  "c1",
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
		ConsumerID:  "mock-consumer",
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
		ConsumerID:  "mock-consumer",
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})
	if !errors.Is(err, domain.ErrStepAlreadyResolved) {
		t.Fatalf("expected ErrStepAlreadyResolved, got %v", err)
	}
}

func TestSubmitJobResultAlreadyResolvedMarksIdle(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
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

	// First submit succeeds and marks idle.
	_ = k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "runner-A",
		ConsumerID:  "consumer-A",
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})

	// Mark runner-B as busy to simulate a second runner picking up the same job.
	runnerHub.MarkBusy("runner-B", "consumer-B")

	// Second submit returns ErrStepAlreadyResolved but must still mark idle.
	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "runner-B",
		ConsumerID:  "consumer-B",
		Success:     true,
		Data:        json.RawMessage(`{"result":42}`),
	})
	if !errors.Is(err, domain.ErrStepAlreadyResolved) {
		t.Fatalf("expected ErrStepAlreadyResolved, got %v", err)
	}

	runnerHub.mu.Lock()
	isIdle := runnerHub.idle["runner-B"]
	runnerHub.mu.Unlock()
	if !isIdle {
		t.Fatal("expected runner-B to be marked idle after ErrStepAlreadyResolved")
	}
}

func TestSubmitJobResultStepNotFoundMarksIdle(t *testing.T) {
	k, _, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := setupRunningExecution(t, k, sessions)

	runnerHub.MarkBusy("runner-X", "consumer-X")

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      "nonexistent-step",
		RunnerID:    "runner-X",
		ConsumerID:  "consumer-X",
		Success:     true,
		Data:        json.RawMessage(`{}`),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	runnerHub.mu.Lock()
	isIdle := runnerHub.idle["runner-X"]
	runnerHub.mu.Unlock()
	if !isIdle {
		t.Fatal("expected runner-X to be marked idle after ErrNotFound")
	}
}

func TestSubmitJobResultTaintedMarksIdle(t *testing.T) {
	k, events, _, runnerHub, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Taint the execution by injecting a sequence gap.
	events.mu.Lock()
	evts := events.events[execID]
	if len(evts) > 0 {
		evts[len(evts)-1].Sequence = evts[len(evts)-1].Sequence + 1
	}
	events.events[execID] = evts
	events.mu.Unlock()

	runnerHub.MarkBusy("runner-T", "consumer-T")

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "runner-T",
		ConsumerID:  "consumer-T",
		Success:     true,
		Data:        json.RawMessage(`{"result":"ok"}`),
	})
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}

	runnerHub.mu.Lock()
	isIdle := runnerHub.idle["runner-T"]
	runnerHub.mu.Unlock()
	if !isIdle {
		t.Fatal("expected runner-T to be marked idle after ErrExecutionTainted")
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
			ToolID: "web_search",
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
		ConsumerID:  "mock-consumer",
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

func TestDispatchPendingJobsRemoveFailureKeepsJobInQueue(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()
	jq.removeErr = errors.New("storage unavailable")

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
		JobQueue:    jq,
	})
	ctx := context.Background()

	job := domain.Job{
		ID:     uuid.Must(uuid.NewV7()),
		ToolID: "web_search",
	}
	if err := jq.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	k.DispatchPendingJobs()

	// Dispatch is now claim-first: if Remove fails we must NOT dispatch,
	// otherwise the job could be sent to a runner more than once.
	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 0 {
		t.Fatalf("expected 0 dispatches when claim fails, got %d", dispatched)
	}

	// Job stays in the queue for the next attempt.
	jobs, err := jq.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected job to remain in queue after Remove failure, got %d jobs", len(jobs))
	}
	if jobs[0].ID != job.ID {
		t.Fatalf("expected same job ID in queue, got %s", jobs[0].ID)
	}
}

func TestDispatchPendingJobsRemoveSuccessRemovesJob(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

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
		JobQueue:    jq,
	})
	ctx := context.Background()

	job := domain.Job{
		ID:     uuid.Must(uuid.NewV7()),
		ToolID: "web_search",
	}
	if err := jq.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	k.DispatchPendingJobs()

	jobs, err := jq.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected job removed from queue after successful dispatch, got %d jobs", len(jobs))
	}
}

func TestDispatchPendingJobsNoRunnerDoesNotRemove(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := &mockRunnerHub{
		idle:   make(map[string]bool),
		hasCap: true,
	}
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	// Override Dispatch to return false (no runner available).
	noDispatchHub := &noDispatchRunnerHub{mockRunnerHub: newMockRunnerHub()}

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   noDispatchHub,
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	_ = runnerHub // suppress unused
	ctx := context.Background()

	job := domain.Job{
		ID:     uuid.Must(uuid.NewV7()),
		ToolID: "web_search",
	}
	if err := jq.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	k.DispatchPendingJobs()

	// Job should remain because no runner was available.
	jobs, err := jq.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected job to remain in queue when no runner available, got %d jobs", len(jobs))
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
			ToolID: "web_search",
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
		ConsumerID:  "mock-consumer",
		Success:     true,
		Data:        json.RawMessage(`{"result":"ok"}`),
	})
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}
}

func TestRetryJobPersistedImmediately(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Before the failure, queue should be empty (initial job was dispatched directly).
	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs before retry, got %d", len(jobs))
	}

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "transient",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Retry job must be in queue immediately — crash-safe.
	jobs, _ = k.jobQueue.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected retry job persisted immediately, got %d jobs", len(jobs))
	}
	if jobs[0].Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", jobs[0].Attempt)
	}
	if jobs[0].NotBefore.IsZero() {
		t.Fatal("expected NotBefore to be set for retry delay")
	}
}

func TestDispatchPendingJobsRespectsNotBefore(t *testing.T) {
	k, _, _, runnerHub, _, _ := newTestKernel()
	ctx := context.Background()

	// Enqueue a job with NotBefore far in the future.
	futureJob := domain.Job{
		ID:        uuid.Must(uuid.NewV7()),
		ToolID:    "web_search",
		NotBefore: time.Now().Add(time.Hour),
	}
	k.enqueuePendingJob(futureJob)

	k.DispatchPendingJobs()

	// Job should NOT have been dispatched.
	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 0 {
		t.Fatalf("expected 0 dispatches for future NotBefore, got %d", dispatched)
	}

	// Job should remain in queue.
	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected job to remain in queue, got %d", len(jobs))
	}
}

func TestDispatchPendingJobsDispatchesReadyJob(t *testing.T) {
	k, _, _, runnerHub, _, _ := newTestKernel()
	ctx := context.Background()

	// Enqueue a job with NotBefore in the past.
	readyJob := domain.Job{
		ID:        uuid.Must(uuid.NewV7()),
		ToolID:    "web_search",
		NotBefore: time.Now().Add(-time.Second),
	}
	k.enqueuePendingJob(readyJob)

	k.DispatchPendingJobs()

	// Job should have been dispatched.
	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 1 {
		t.Fatalf("expected 1 dispatch for past NotBefore, got %d", dispatched)
	}

	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected job removed after dispatch, got %d", len(jobs))
	}
}

func TestRecoverPendingRetriesAfterCrash(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: runnerHub},
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// The initial job enqueue from ProcessIntent lands in the queue
	// because noDispatchRunnerHub returns false. Clear it to simulate
	// a clean crash scenario.
	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	// Simulate the events that handleJobRetry would emit, but without
	// enqueuing the retry job (simulating a crash between event emission
	// and job enqueue).
	correlationID := uuid.Must(uuid.NewV7())
	_, err := k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "transient", Retryable: true},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.failed: %v", err)
	}
	_, err = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.retried: %v", err)
	}

	// No jobs in queue — simulating the crash scenario.
	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs before recovery, got %d", len(jobs))
	}

	// Run recovery — should detect the orphaned retry and re-enqueue.
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	jobs, _ = jq.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 recovered job, got %d", len(jobs))
	}
	if jobs[0].StepID != result.StepID {
		t.Fatalf("expected recovered job for step %s, got %s", result.StepID, jobs[0].StepID)
	}
	if jobs[0].Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", jobs[0].Attempt)
	}
	if jobs[0].ToolID != "web_search" {
		t.Fatalf("expected tool_id web.search, got %s", jobs[0].ToolID)
	}
}

func TestRecoverPendingRetriesSkipsAlreadyQueued(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Submit a retryable failure — this persists the job in the queue.
	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		Success:     false,
		Error:       "transient",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job before recovery, got %d", len(jobs))
	}

	// Recovery should not duplicate the job.
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	jobs, _ = k.jobQueue.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected still 1 job after recovery (no duplicate), got %d", len(jobs))
	}
}

func TestRecoverPendingRetriesSkipsTerminalExecution(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Emit retried events.
	correlationID := uuid.Must(uuid.NewV7())
	_, _ = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true},
		uuid.Nil, correlationID)
	_, _ = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2},
		uuid.Nil, correlationID)

	// Cancel the step and mark execution as failed.
	_, _ = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepCancelled,
		domain.StepCancelledPayload{Reason: "cancelled"},
		uuid.Nil, correlationID)
	_, _ = k.EmitEvent(ctx, execID, "",
		domain.EventExecutionFailed,
		domain.ExecutionFailedPayload{Error: "cancelled"},
		uuid.Nil, correlationID)
	_ = k.events.UpdateExecutionStatus(ctx, execID, domain.ExecutionFailed)

	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	// No recovery for terminal executions.
	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs for terminal execution, got %d", len(jobs))
	}
}

func TestDispatchPendingRetryEmitsStepDispatched(t *testing.T) {
	// Regression for #108: a retry-dispatched step must get a fresh
	// step.dispatched event so the projector re-establishes Deadline and
	// DispatchedAt — without it, the timeout watcher skips stalled retries.
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

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
		JobQueue:    jq,
		Config:      KernelConfig{RetryBaseDelay: 1}, // ~0 delay so retry is ready immediately
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Submitting a retryable failure clears the step's Deadline via step.retried
	// and enqueues a retry job. SubmitJobResult's deferred DispatchPendingJobs
	// then claims the retry and must emit step.dispatched.
	before := time.Now()
	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("submit retryable failure: %v", err)
	}

	// Retry job must have been claimed off the queue and dispatched.
	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected retry job to be claimed and dispatched, got %d in queue", len(jobs))
	}

	// A step.dispatched event must be emitted for the retry, carrying the
	// claimed job's deadline and the dispatching runner's ID.
	events.mu.Lock()
	var retryDispatched *domain.Event
	for i := range events.events[execID] {
		evt := events.events[execID][i]
		if evt.Type != domain.EventStepDispatched || evt.StepID != result.StepID {
			continue
		}
		var p domain.StepDispatchedPayload
		if err := json.Unmarshal(evt.Payload, &p); err != nil {
			t.Fatalf("unmarshal step.dispatched payload: %v", err)
		}
		if p.RunnerID == "mock-runner" {
			retryDispatched = &events.events[execID][i]
			break
		}
	}
	events.mu.Unlock()
	if retryDispatched == nil {
		t.Fatal("expected a step.dispatched event for the retry with the real runner ID")
	}

	var payload domain.StepDispatchedPayload
	if err := json.Unmarshal(retryDispatched.Payload, &payload); err != nil {
		t.Fatalf("unmarshal step.dispatched payload: %v", err)
	}
	if payload.Deadline.Before(before.Add(k.config.StepTimeout - time.Second)) {
		t.Fatalf("expected retry deadline near now+StepTimeout, got %v", payload.Deadline)
	}

	// Projected state must reflect a fresh Deadline so the timeout watcher
	// will see the stalled retry.
	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepDispatched {
		t.Fatalf("expected step dispatched after retry, got %s", step.Status)
	}
	if step.Deadline == nil {
		t.Fatal("expected retry to set a fresh Deadline via step.dispatched")
	}
	if !step.Deadline.After(before) {
		t.Fatalf("expected fresh Deadline after %v, got %v", before, step.Deadline)
	}
	if step.DispatchedAt == nil {
		t.Fatal("expected retry to set DispatchedAt via step.dispatched")
	}
	if step.RunnerID != "mock-runner" {
		t.Fatalf("expected step RunnerID set to mock-runner, got %q", step.RunnerID)
	}
}

func TestDispatchPendingRetryNoEventWhenNoRunner(t *testing.T) {
	// When no runner is available, the job is re-enqueued and no
	// step.dispatched should be emitted — otherwise the projector would
	// believe the step is dispatched while it's still sitting in the queue.
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: runnerHub},
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID := "exec-" + uuid.Must(uuid.NewV7()).String()
	if err := events.CreateExecution(ctx, execID, "agent-1", nil); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	job := domain.Job{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: execID,
		StepID:      "step-1",
		ToolID:      "web_search",
		Deadline:    time.Now().Add(time.Minute),
	}
	if err := jq.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	k.DispatchPendingJobs()

	// Job stays in queue because no runner picked it up.
	jobs, _ := jq.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected job re-enqueued, got %d in queue", len(jobs))
	}

	events.mu.Lock()
	for _, evt := range events.events[execID] {
		if evt.Type == domain.EventStepDispatched {
			events.mu.Unlock()
			t.Fatal("expected no step.dispatched when no runner accepted the job")
		}
	}
	events.mu.Unlock()
}

func TestRecoverPendingRetryDispatchEmitsStepDispatched(t *testing.T) {
	// Regression for #108: jobs recovered via RecoverPendingRetries must
	// also emit step.dispatched on dispatch so the timeout watcher sees a
	// fresh Deadline.
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

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
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Simulate crash window: step.failed + step.retried written, but the
	// retry job was never enqueued.
	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	correlationID := uuid.Must(uuid.NewV7())
	_, err := k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "transient", Retryable: true},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.failed: %v", err)
	}
	_, err = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.retried: %v", err)
	}

	before := time.Now()
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	// Recovery enqueues and then DispatchPendingJobs claims the job.
	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected recovered job to be dispatched, got %d in queue", len(jobs))
	}

	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepDispatched {
		t.Fatalf("expected recovered step to be dispatched, got %s", step.Status)
	}
	if step.Deadline == nil {
		t.Fatal("expected recovered retry to set a fresh Deadline via step.dispatched")
	}
	if !step.Deadline.After(before) {
		t.Fatalf("expected fresh Deadline after %v, got %v", before, step.Deadline)
	}
	if step.DispatchedAt == nil {
		t.Fatal("expected recovered retry to set DispatchedAt via step.dispatched")
	}
}

func TestHandleJobRetryEmitsEventsAtomically(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	eventsBefore := len(events.events[execID])

	err := k.SubmitJobResult(ctx, domain.JobResult{
		ExecutionID: execID,
		StepID:      result.StepID,
		RunnerID:    "mock-runner",
		ConsumerID:  "mock-consumer",
		Success:     false,
		Error:       "timeout",
		Retryable:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events.mu.Lock()
	evts := events.events[execID]
	newEvents := evts[eventsBefore:]
	events.mu.Unlock()

	if len(newEvents) != 2 {
		t.Fatalf("expected exactly 2 new events (step.failed + step.retried), got %d", len(newEvents))
	}
	if newEvents[0].Type != domain.EventStepFailed {
		t.Fatalf("expected first event to be step.failed, got %s", newEvents[0].Type)
	}
	if newEvents[1].Type != domain.EventStepRetried {
		t.Fatalf("expected second event to be step.retried, got %s", newEvents[1].Type)
	}
	if newEvents[0].CorrelationID != newEvents[1].CorrelationID {
		t.Fatal("expected both retry events to share the same correlation ID")
	}
}

func TestRecoverPendingRetriesHandlesOrphanedFailedRetryable(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: runnerHub},
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	// Simulate crash window: only step.failed (retryable) was written,
	// step.retried was NOT written.
	correlationID := uuid.Must(uuid.NewV7())
	_, err := k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "transient", Retryable: true},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.failed: %v", err)
	}

	// Verify step is stuck: failed + retryable, no step.retried.
	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepFailed {
		t.Fatalf("expected step failed, got %s", step.Status)
	}
	if !step.Retryable {
		t.Fatal("expected step to be retryable")
	}

	// Recovery should detect the orphaned failed+retryable step and recover it.
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 recovered job, got %d", len(jobs))
	}
	if jobs[0].StepID != result.StepID {
		t.Fatalf("expected recovered job for step %s, got %s", result.StepID, jobs[0].StepID)
	}
	if jobs[0].Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", jobs[0].Attempt)
	}

	// The recovery should have emitted step.retried.
	state, _ = k.GetExecution(ctx, execID)
	step = state.Steps[result.StepID]
	if step.Status != domain.StepPending {
		t.Fatalf("expected step pending after recovery, got %s", step.Status)
	}
	if step.Attempt != 2 {
		t.Fatalf("expected attempt 2 after recovery, got %d", step.Attempt)
	}
}

func TestStartRetryDispatcherDispatchesDelayedJob(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

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
		JobQueue:    jq,
		Config: KernelConfig{
			RetryCheckInterval: 50 * time.Millisecond,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Enqueue a job with NotBefore just slightly in the future.
	job := domain.Job{
		ID:        uuid.Must(uuid.NewV7()),
		ToolID:    "web_search",
		NotBefore: time.Now().Add(80 * time.Millisecond),
	}
	k.enqueuePendingJob(job)

	// Before starting the dispatcher, the job should not be dispatched.
	k.DispatchPendingJobs()
	runnerHub.mu.Lock()
	if len(runnerHub.dispatched) != 0 {
		t.Fatal("expected no dispatches before NotBefore elapses")
	}
	runnerHub.mu.Unlock()

	k.StartRetryDispatcher(ctx)
	defer k.Shutdown()

	// Wait for the ticker to fire after NotBefore elapses.
	time.Sleep(300 * time.Millisecond)

	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 1 {
		t.Fatalf("expected 1 dispatch after retry dispatcher fires, got %d", dispatched)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected job removed from queue after dispatch, got %d", len(jobs))
	}
}

func TestStartRetryDispatcherDoubleStartIsNoop(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer k.Shutdown()

	k.StartRetryDispatcher(ctx)
	k.StartRetryDispatcher(ctx)
	k.StartRetryDispatcher(ctx)

	// If the guard works, Shutdown should complete quickly because only one
	// goroutine was started. Without the guard, retryWg would have count 3
	// but only one context cancel, potentially causing issues.
	cancel()

	done := make(chan struct{})
	go func() {
		k.retryWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success — only one goroutine was running
	case <-time.After(2 * time.Second):
		t.Fatal("retry dispatcher goroutine did not exit — double-start guard may be broken")
	}
}

func TestStartRetryDispatcherStopsOnShutdown(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	k.StartRetryDispatcher(ctx)

	// Shutdown should wait for the dispatcher goroutine to exit.
	done := make(chan struct{})
	go func() {
		k.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return in time — retry dispatcher goroutine may be stuck")
	}
}

func TestStartRetryDispatcherStopsOnContextCancel(t *testing.T) {
	k, _, _, _, _, _ := newTestKernel()

	ctx, cancel := context.WithCancel(context.Background())
	k.StartRetryDispatcher(ctx)

	cancel()

	done := make(chan struct{})
	go func() {
		k.retryWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("retry dispatcher goroutine did not exit after context cancellation")
	}
}

func TestRecoverPendingRetriesSkipsExhaustedRetries(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: runnerHub},
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true, // max_attempts=3
		},
	})

	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	// Simulate retries up to attempt 3, then a final failed+retryable on attempt 3.
	// Emit step.failed + step.retried for attempt 1→2.
	cid := uuid.Must(uuid.NewV7())
	_, _ = k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true}, uuid.Nil, cid)
	_, _ = k.EmitEvent(ctx, execID, result.StepID, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2}, uuid.Nil, cid)
	// Emit step.failed + step.retried for attempt 2→3.
	cid2 := uuid.Must(uuid.NewV7())
	_, _ = k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true}, uuid.Nil, cid2)
	_, _ = k.EmitEvent(ctx, execID, result.StepID, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 3}, uuid.Nil, cid2)
	// Now fail on attempt 3 (max_attempts=3) with retryable=true — but should NOT retry.
	cid3 := uuid.Must(uuid.NewV7())
	_, _ = k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true}, uuid.Nil, cid3)

	// Recovery should NOT create a job because attempt == maxAttempts.
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 recovered jobs for exhausted retries, got %d", len(jobs))
	}
}

func TestRecoverPendingRetriesAfterRecoverActiveExecutions(t *testing.T) {
	events := newMockEventStore()
	checkpoints := newMockCheckpointStore()
	agentHub := newMockAgentHub()
	runnerHub := newMockRunnerHub()
	signals := newMockSignalStore()
	sessions := newMockSessionStore()
	runners := newMockRunnerStore()
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      events,
		Checkpoints: checkpoints,
		AgentHub:    agentHub,
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: runnerHub},
		Signals:     signals,
		Sessions:    sessions,
		Runners:     runners,
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Clear the queue and simulate crash: step.failed + step.retried emitted,
	// but retry job never enqueued.
	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	correlationID := uuid.Must(uuid.NewV7())
	_, err := k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "transient", Retryable: true},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.failed: %v", err)
	}
	_, err = k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2},
		uuid.Nil, correlationID)
	if err != nil {
		t.Fatalf("emit step.retried: %v", err)
	}

	// Simulate startup sequence: RecoverPendingRetries runs after active
	// execution recovery, matching the order in server.go and dev.go.
	// RecoverPendingRetries should detect and re-enqueue the orphaned retry.
	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 recovered job after startup sequence, got %d", len(jobs))
	}
	if jobs[0].StepID != result.StepID {
		t.Fatalf("expected recovered job for step %s, got %s", result.StepID, jobs[0].StepID)
	}
	if jobs[0].Attempt != 2 {
		t.Fatalf("expected attempt 2, got %d", jobs[0].Attempt)
	}
}

func TestRecordStepStartedAfterCompletionIsIgnored(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// Complete the step via SubmitJobResult.
	err := k.SubmitJobResult(ctx, domain.JobResult{
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

	// Late RecordStepStarted after step is already completed should be silently ignored.
	err = k.RecordStepStarted(ctx, execID, result.StepID, "mock-runner")
	if err != nil {
		t.Fatalf("expected no error for late RecordStepStarted, got %v", err)
	}

	// Verify step is still in succeeded status, not reverted to running.
	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepSucceeded {
		t.Fatalf("expected step to remain succeeded after late RecordStepStarted, got %s", step.Status)
	}
}

func TestRecordStepStartedForAlreadyRunningIsIgnored(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	result, _ := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
		},
	})

	// First RecordStepStarted should succeed.
	err := k.RecordStepStarted(ctx, execID, result.StepID, "mock-runner")
	if err != nil {
		t.Fatalf("first RecordStepStarted: %v", err)
	}

	// Second RecordStepStarted should be silently ignored (already running).
	err = k.RecordStepStarted(ctx, execID, result.StepID, "mock-runner")
	if err != nil {
		t.Fatalf("expected no error for duplicate RecordStepStarted, got %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepRunning {
		t.Fatalf("expected step running, got %s", step.Status)
	}
}

func TestRecordStepStartedForUnknownStepReturnsError(t *testing.T) {
	k, _, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, _ := setupRunningExecution(t, k, sessions)

	err := k.RecordStepStarted(ctx, execID, "nonexistent-step", "mock-runner")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for nonexistent step, got %v", err)
	}
}

func TestRecordStepStartedAfterFailureIsIgnored(t *testing.T) {
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

	// Fail the step.
	err := k.SubmitJobResult(ctx, domain.JobResult{
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

	// Late RecordStepStarted after step has failed should be silently ignored.
	err = k.RecordStepStarted(ctx, execID, result.StepID, "mock-runner")
	if err != nil {
		t.Fatalf("expected no error for late RecordStepStarted after failure, got %v", err)
	}

	state, _ := k.GetExecution(ctx, execID)
	step := state.Steps[result.StepID]
	if step.Status != domain.StepFailed {
		t.Fatalf("expected step to remain failed, got %s", step.Status)
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

func TestRecordStepStartedRejectsTaintedExecution(t *testing.T) {
	k, events, _, _, sessions, _ := newTestKernel()
	ctx := context.Background()

	execID, sessionID := setupRunningExecution(t, k, sessions)

	// Create a remote step.
	result, err := k.ProcessIntent(ctx, domain.IntentRequest{
		ExecutionID: execID,
		SessionID:   sessionID,
		Intent: domain.Intent{
			Type:   domain.IntentInvokeTool,
			ToolID: "web_search",
			Remote: true,
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

	err = k.RecordStepStarted(ctx, execID, result.StepID, "mock-runner")
	if !errors.Is(err, domain.ErrExecutionTainted) {
		t.Fatalf("expected ErrExecutionTainted, got %v", err)
	}
}

func TestDispatchPendingJobsConcurrentNoDuplicate(t *testing.T) {
	runnerHub := &countingRunnerHub{
		idle: make(map[string]bool),
	}
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      newMockEventStore(),
		Checkpoints: newMockCheckpointStore(),
		AgentHub:    newMockAgentHub(),
		RunnerHub:   runnerHub,
		Signals:     newMockSignalStore(),
		Sessions:    newMockSessionStore(),
		Runners:     newMockRunnerStore(),
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		job := domain.Job{
			ID:     uuid.Must(uuid.NewV7()),
			ToolID: "web_search",
		}
		if err := jq.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	const goroutines = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k.DispatchPendingJobs()
		}()
	}
	wg.Wait()

	runnerHub.mu.Lock()
	dispatched := runnerHub.dispatchCount
	runnerHub.mu.Unlock()

	if dispatched != 5 {
		t.Fatalf("expected exactly 5 dispatches (one per job), got %d", dispatched)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected all jobs removed from queue, got %d remaining", len(jobs))
	}
}

func TestDispatchPendingJobsConcurrentBusyMarkingRace(t *testing.T) {
	runnerHub := &singleConnRunnerHub{}
	jq := newMockJobQueue()

	k := NewKernel(Deps{
		Events:      newMockEventStore(),
		Checkpoints: newMockCheckpointStore(),
		AgentHub:    newMockAgentHub(),
		RunnerHub:   runnerHub,
		Signals:     newMockSignalStore(),
		Sessions:    newMockSessionStore(),
		Runners:     newMockRunnerStore(),
		Locker:      &mockLocker{},
		Policy:      newAllowAllPolicy(),
		JobQueue:    jq,
	})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		job := domain.Job{
			ID:     uuid.Must(uuid.NewV7()),
			ToolID: "tool",
		}
		if err := jq.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	const goroutines = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k.DispatchPendingJobs()
		}()
	}
	wg.Wait()

	runnerHub.mu.Lock()
	dispatched := runnerHub.dispatchCount
	runnerHub.mu.Unlock()

	if dispatched != 1 {
		t.Fatalf("expected exactly 1 dispatch (runner has 1 conn, becomes busy), got %d", dispatched)
	}

	jobs, _ := jq.All(ctx)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs remaining (only 1 runner conn), got %d", len(jobs))
	}
}

type countingRunnerHub struct {
	mu            sync.Mutex
	dispatchCount int
	idle          map[string]bool
}

func (m *countingRunnerHub) Dispatch(_ string, _ store.RunnerMessage) (store.RunnerConnInfo, bool) {
	m.mu.Lock()
	m.dispatchCount++
	m.mu.Unlock()
	return store.RunnerConnInfo{RunnerID: "runner", ConsumerID: "c1"}, true
}

func (m *countingRunnerHub) SendTo(_, _ string, _ store.RunnerMessage) bool { return true }
func (m *countingRunnerHub) MarkBusy(runnerID, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[runnerID] = false
}
func (m *countingRunnerHub) MarkIdle(runnerID, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[runnerID] = true
}
func (m *countingRunnerHub) HasCapability(_ string) bool             { return true }
func (m *countingRunnerHub) UpdateCapabilities(_ string, _ []string) {}

type singleConnRunnerHub struct {
	mu            sync.Mutex
	dispatchCount int
	busy          bool
}

func (m *singleConnRunnerHub) Dispatch(_ string, _ store.RunnerMessage) (store.RunnerConnInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return store.RunnerConnInfo{}, false
	}
	m.busy = true
	m.dispatchCount++
	return store.RunnerConnInfo{RunnerID: "runner", ConsumerID: "c1"}, true
}

func (m *singleConnRunnerHub) SendTo(_, _ string, _ store.RunnerMessage) bool { return true }
func (m *singleConnRunnerHub) MarkBusy(_, _ string)                           {}
func (m *singleConnRunnerHub) MarkIdle(_ string, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy = false
}
func (m *singleConnRunnerHub) HasCapability(_ string) bool             { return true }
func (m *singleConnRunnerHub) UpdateCapabilities(_ string, _ []string) {}
