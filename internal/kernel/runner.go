package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

func (k *Kernel) RegisterRunner(ctx context.Context, runner domain.Runner) error {
	return k.runners.Register(ctx, runner)
}

func (k *Kernel) SubmitJobResult(ctx context.Context, result domain.JobResult) error {
	release, err := k.locker.Acquire(ctx, "execution:"+result.ExecutionID)
	if err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer release()

	state, err := k.projector.Project(ctx, result.ExecutionID)
	if err != nil {
		return err
	}
	if state.Tainted {
		return domain.ErrExecutionTainted
	}

	step, ok := state.Steps[result.StepID]
	if !ok {
		return fmt.Errorf("%w: step %s", domain.ErrNotFound, result.StepID)
	}
	if step.Status.IsTerminal() {
		return domain.ErrStepAlreadyResolved
	}

	correlationID := uuid.Must(uuid.NewV7())

	if result.Success {
		if err := k.handleJobSuccess(ctx, result, step, correlationID); err != nil {
			return err
		}
	} else if result.Retryable && step.Attempt < step.MaxAttempts {
		if err := k.handleJobRetry(ctx, result, step, correlationID); err != nil {
			return err
		}
	} else {
		if err := k.handleJobFailure(ctx, result, correlationID); err != nil {
			return err
		}
	}

	if result.RunnerID != "" && result.ConsumerID != "" {
		k.runnerHub.MarkIdle(result.RunnerID, result.ConsumerID)
	}
	k.DispatchPendingJobs()

	return nil
}

func (k *Kernel) handleJobSuccess(ctx context.Context, result domain.JobResult, step *domain.Step, correlationID uuid.UUID) error {
	_, err := k.EmitEvent(ctx, result.ExecutionID, result.StepID,
		domain.EventStepCompleted,
		domain.StepCompletedPayload{Result: result.Data},
		uuid.Nil, correlationID)
	if err != nil {
		return err
	}

	if k.metrics != nil {
		k.metrics.StepDuration.WithLabelValues(step.ToolID).Observe(time.Since(step.CreatedAt).Seconds())
	}

	k.notifyAgentStepResult(ctx, result.ExecutionID, StepResult{
		ExecutionID: result.ExecutionID,
		StepID:      result.StepID,
		Status:      domain.StepSucceeded,
		Result:      result.Data,
		Error:       result.Error,
	})
	return nil
}

func (k *Kernel) handleJobRetry(ctx context.Context, result domain.JobResult, step *domain.Step, correlationID uuid.UUID) error {
	_, err := k.EmitEvent(ctx, result.ExecutionID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: result.Error, Retryable: true},
		uuid.Nil, correlationID)
	if err != nil {
		return err
	}

	nextAttempt := step.Attempt + 1
	_, err = k.EmitEvent(ctx, result.ExecutionID, result.StepID,
		domain.EventStepRetried,
		domain.StepRetriedPayload{NextAttempt: nextAttempt},
		uuid.Nil, correlationID)
	if err != nil {
		return err
	}

	delay := k.retryDelay(nextAttempt)
	retryJob := domain.Job{
		ID:          uuid.Must(uuid.NewV7()),
		ExecutionID: result.ExecutionID,
		StepID:      result.StepID,
		Attempt:     nextAttempt,
		ToolID:      step.ToolID,
		ToolVersion: step.ToolVersion,
		Arguments:   step.Arguments,
		Deadline:    time.Now().Add(k.config.StepTimeout),
		NotBefore:   time.Now().Add(delay),
	}

	k.enqueuePendingJob(retryJob)
	return nil
}

func (k *Kernel) handleJobFailure(ctx context.Context, result domain.JobResult, correlationID uuid.UUID) error {
	_, err := k.EmitEvent(ctx, result.ExecutionID, result.StepID,
		domain.EventStepFailed,
		domain.StepFailedPayload{Error: result.Error, Retryable: false},
		uuid.Nil, correlationID)
	if err != nil {
		return err
	}

	k.notifyAgentStepResult(ctx, result.ExecutionID, StepResult{
		ExecutionID: result.ExecutionID,
		StepID:      result.StepID,
		Status:      domain.StepFailed,
		Result:      result.Data,
		Error:       result.Error,
	})
	return nil
}

func (k *Kernel) notifyAgentStepResult(ctx context.Context, executionID string, sr StepResult) {
	session, found, err := k.sessions.GetByExecution(ctx, executionID)
	if err != nil {
		k.logger.Warn("failed to lookup session for step result delivery",
			slog.String("execution_id", executionID),
			slog.String("error", err.Error()),
		)
		return
	}
	if !found {
		return
	}

	payload, err := json.Marshal(sr)
	if err != nil {
		k.logger.Error("failed to marshal step result for SSE delivery", slog.String("error", err.Error()))
		return
	}
	k.agentHub.SendToSession(session.ID, store.AgentMessage{
		Type:    "tool.result",
		Payload: payload,
	})
}

func (k *Kernel) RecordStepStarted(ctx context.Context, executionID, stepID, runnerID string) error {
	_, err := k.EmitEvent(ctx, executionID, stepID,
		domain.EventStepStarted,
		domain.StepStartedPayload{RunnerID: runnerID},
		uuid.Nil, uuid.Nil)
	return err
}

func (k *Kernel) UnregisterRunner(ctx context.Context, runnerID string) error {
	return k.runners.Delete(ctx, runnerID)
}

func (k *Kernel) ListExecutions(ctx context.Context, filter domain.ExecutionFilter, cursor string, limit int) ([]domain.ExecutionSummary, string, error) {
	return k.events.ListExecutions(ctx, filter, cursor, limit)
}

func (k *Kernel) GetEvents(ctx context.Context, executionID string, afterSequence int64, limit int) ([]domain.Event, error) {
	return k.events.GetByExecution(ctx, executionID, afterSequence, limit)
}

func (k *Kernel) retryDelay(attempt int) time.Duration {
	delay := k.config.RetryBaseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	if delay > k.config.RetryMaxDelay {
		delay = k.config.RetryMaxDelay
	}
	return delay
}

func (k *Kernel) enqueuePendingJob(job domain.Job) {
	if err := k.jobQueue.Enqueue(context.Background(), job); err != nil {
		k.logger.Warn("failed to enqueue pending job",
			slog.String("job_id", job.ID.String()),
			slog.String("error", err.Error()),
		)
	}
}

func (k *Kernel) DispatchPendingJobs() {
	ctx := context.Background()
	jobs, err := k.jobQueue.All(ctx)
	if err != nil {
		k.logger.Warn("failed to read pending jobs", slog.String("error", err.Error()))
		return
	}

	now := time.Now()
	for _, job := range jobs {
		if !job.NotBefore.IsZero() && now.Before(job.NotBefore) {
			continue
		}

		payload, err := json.Marshal(job)
		if err != nil {
			k.logger.Warn("failed to marshal pending job",
				slog.String("job_id", job.ID.String()),
				slog.String("error", err.Error()),
			)
			continue
		}
		msg := store.RunnerMessage{Type: "job.assigned", Payload: payload}
		info, dispatched := k.runnerHub.Dispatch(job.ToolID, msg)
		if dispatched {
			k.runnerHub.MarkBusy(info.RunnerID, info.ConsumerID)
			if err := k.jobQueue.Remove(ctx, job.ID); err != nil {
				k.logger.Error("failed to remove dispatched job from queue, may be dispatched again",
					slog.String("job_id", job.ID.String()),
					slog.String("runner_id", info.RunnerID),
					slog.String("error", err.Error()),
				)
			}
			k.logger.Debug("dispatched pending job",
				slog.String("job_id", job.ID.String()),
				slog.String("runner_id", info.RunnerID),
			)
		}
	}
}

func (k *Kernel) RecoverPendingRetries(ctx context.Context) error {
	execIDs, err := k.events.ListActiveExecutionIDs(ctx)
	if err != nil {
		return fmt.Errorf("listing active executions: %w", err)
	}

	existingJobs, err := k.jobQueue.All(ctx)
	if err != nil {
		return fmt.Errorf("listing pending jobs: %w", err)
	}
	coveredSteps := make(map[string]struct{}, len(existingJobs))
	for _, j := range existingJobs {
		coveredSteps[j.StepID] = struct{}{}
	}

	recovered := 0
	for _, execID := range execIDs {
		state, err := k.projector.Project(ctx, execID)
		if err != nil {
			k.logger.Warn("recovery: failed to project execution",
				slog.String("execution_id", execID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if state.Tainted || state.Execution.Status.IsTerminal() {
			continue
		}

		for _, step := range state.Steps {
			if step.Status != domain.StepPending || step.Attempt < 2 {
				continue
			}
			if _, ok := coveredSteps[step.ID]; ok {
				continue
			}

			job := domain.Job{
				ID:          uuid.Must(uuid.NewV7()),
				ExecutionID: execID,
				StepID:      step.ID,
				Attempt:     step.Attempt,
				ToolID:      step.ToolID,
				ToolVersion: step.ToolVersion,
				Arguments:   step.Arguments,
				Deadline:    time.Now().Add(k.config.StepTimeout),
			}
			k.enqueuePendingJob(job)
			coveredSteps[step.ID] = struct{}{}
			recovered++
		}
	}

	if recovered > 0 {
		k.logger.Info("recovered pending retry jobs",
			slog.Int("count", recovered),
		)
		k.DispatchPendingJobs()
	}
	return nil
}
