package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

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
			ToolID: "web.search",
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
			ToolID: "web.search",
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
		ToolID: "web.search",
	}
	if err := jq.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	k.DispatchPendingJobs()

	// Job should still be dispatched to the runner.
	runnerHub.mu.Lock()
	dispatched := len(runnerHub.dispatched)
	runnerHub.mu.Unlock()
	if dispatched != 1 {
		t.Fatalf("expected 1 dispatch, got %d", dispatched)
	}

	// But the job should remain in the queue because Remove failed.
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
		ToolID: "web.search",
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
	noDispatchHub := &noDispatchRunnerHub{}

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
		ToolID: "web.search",
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
			ToolID: "web.search",
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
		ToolID:    "web.search",
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
		ToolID:    "web.search",
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
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: *runnerHub},
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
			ToolID: "web.search",
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
	if jobs[0].ToolID != "web.search" {
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
			ToolID: "web.search",
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
			ToolID: "web.search",
			Remote: true,
		},
	})

	// Emit retried events.
	correlationID := uuid.Must(uuid.NewV7())
	k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true},
		uuid.Nil, correlationID)
	k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2},
		uuid.Nil, correlationID)

	// Cancel the step and mark execution as failed.
	k.EmitEvent(ctx, execID, result.StepID,
		domain.EventStepCancelled,
		domain.StepCancelledPayload{Reason: "cancelled"},
		uuid.Nil, correlationID)
	k.EmitEvent(ctx, execID, "",
		domain.EventExecutionFailed,
		domain.ExecutionFailedPayload{Error: "cancelled"},
		uuid.Nil, correlationID)
	k.events.UpdateExecutionStatus(ctx, execID, domain.ExecutionFailed)

	if err := k.RecoverPendingRetries(ctx); err != nil {
		t.Fatalf("RecoverPendingRetries: %v", err)
	}

	// No recovery for terminal executions.
	jobs, _ := k.jobQueue.All(ctx)
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs for terminal execution, got %d", len(jobs))
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
			ToolID: "web.search",
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
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: *runnerHub},
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
			ToolID: "web.search",
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
		RunnerHub:   &noDispatchRunnerHub{mockRunnerHub: *runnerHub},
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
			ToolID: "web.search",
			Remote: true, // max_attempts=3
		},
	})

	jq.mu.Lock()
	jq.jobs = nil
	jq.mu.Unlock()

	// Simulate retries up to attempt 3, then a final failed+retryable on attempt 3.
	// Emit step.failed + step.retried for attempt 1→2.
	cid := uuid.Must(uuid.NewV7())
	k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true}, uuid.Nil, cid)
	k.EmitEvent(ctx, execID, result.StepID, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 2}, uuid.Nil, cid)
	// Emit step.failed + step.retried for attempt 2→3.
	cid2 := uuid.Must(uuid.NewV7())
	k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
		domain.StepFailedPayload{Error: "err", Retryable: true}, uuid.Nil, cid2)
	k.EmitEvent(ctx, execID, result.StepID, domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: 3}, uuid.Nil, cid2)
	// Now fail on attempt 3 (max_attempts=3) with retryable=true — but should NOT retry.
	cid3 := uuid.Must(uuid.NewV7())
	k.EmitEvent(ctx, execID, result.StepID, domain.EventStepFailed,
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
