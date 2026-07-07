package kernel

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/dispatcher"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (k *Kernel) CompleteExecution(ctx context.Context, execID uuid.UUID, output json.RawMessage) error {
	release, err := k.d.Locker.Acquire(ctx, lockKey(execID))
	if err != nil {
		return err
	}
	defer release()

	exec, err := k.d.Executions.GetExecution(ctx, execID)
	if err != nil {
		return err
	}
	if exec.Status.IsTerminal() {
		return domain.ErrExecutionTerminal
	}
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if _, err := tx.Append(ctx, execID, domain.EventExecutionCompleted, projector.ExecutionPayload(execID, domain.ExecutionCompleted, output, "")); err != nil {
			return err
		}
		return tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionCompleted, output, "")
	}); err != nil {
		return err
	}
	k.d.Observer.RecordExecutionTerminal(string(domain.ExecutionCompleted))
	return nil
}

func (k *Kernel) FailExecution(ctx context.Context, execID uuid.UUID, reason string) error {
	release, err := k.d.Locker.Acquire(ctx, lockKey(execID))
	if err != nil {
		return err
	}
	defer release()

	exec, err := k.d.Executions.GetExecution(ctx, execID)
	if err != nil {
		return err
	}
	if exec.Status.IsTerminal() {
		return domain.ErrExecutionTerminal
	}
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if _, err := tx.Append(ctx, execID, domain.EventExecutionFailed, projector.ExecutionPayload(execID, domain.ExecutionFailed, nil, reason)); err != nil {
			return err
		}
		return tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionFailed, nil, reason)
	}); err != nil {
		return err
	}
	k.d.Observer.RecordExecutionTerminal(string(domain.ExecutionFailed))
	return nil
}

func (k *Kernel) EnqueueReDrive(ctx context.Context, execID uuid.UUID) error {
	return k.enqueueDispatch(ctx, execID)
}

func (k *Kernel) RunDispatches(ctx context.Context, batch int) error {
	now := time.Now().UTC()
	// Reclaim dispatches left in_flight by a crashed replica before claiming new work.
	reclaimed, err := k.d.Queue.ReclaimStalled(ctx, now, k.cfg.DispatchLeaseTimeout, batch)
	if err != nil {
		return err
	}
	k.d.Observer.RecordReclaimedStalled(len(reclaimed))
	jobs, err := k.d.Queue.Claim(ctx, k.cfg.ReplicaID, batch, now)
	if err != nil {
		return err
	}
	k.d.Observer.RecordQueueDepth(len(jobs))
	if len(jobs) == 0 {
		return nil
	}
	k.log.Info("dispatch drain", "jobs", len(jobs))

	// Deliver concurrently with a bounded worker pool. Each delivery makes a
	// blocking, timeout-bounded webhook call; running them serially would let one
	// slow agent stall delivery for every other agent claimed in the same batch.
	concurrency := k.cfg.DispatchConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(jobs) {
		concurrency = len(jobs)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func(job domain.Dispatch) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := k.deliver(ctx, job); err != nil {
				k.log.Info("delivery error", "dispatch_id", job.ID, "err", err)
			}
		}(job)
	}
	wg.Wait()
	return nil
}

func (k *Kernel) deliver(ctx context.Context, d domain.Dispatch) error {
	ctx, span := k.d.Observer.Tracer().Start(ctx, "dispatch.deliver",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(
		attribute.String("dispatch.id", d.ID.String()),
		attribute.String("execution.id", d.ExecutionID.String()),
		attribute.Int("dispatch.attempt", d.Attempt),
	)

	exec, err := k.d.Executions.GetExecution(ctx, d.ExecutionID)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if exec.Status.IsTerminal() {
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchExhausted, projector.DispatchPayload(d.ID, d.ExecutionID, domain.DispatchExhausted, d.Attempt)); err != nil {
			return err
		}
		return k.d.Queue.Ack(ctx, d.ID, domain.DispatchExhausted, nil)
	}
	agent, err := k.d.Agents.GetAgent(ctx, exec.AgentID)
	if err != nil {
		return err
	}
	start := time.Now()
	res := k.d.Dispatcher.Deliver(ctx, agent.WebhookURL, agent.Secret, d.ExecutionID, d.ID)
	k.d.Observer.RecordDispatchLatency(time.Since(start))
	if res.Outcome != dispatcher.OutcomeSuccess || res.Err != nil {
		k.log.Info("dispatch attempt", "dispatch_id", d.ID, "outcome", res.Outcome, "status", res.StatusCode, "err", res.Err)
	}
	k.d.Observer.RecordDispatchOutcome(outcomeName(res.Outcome))
	span.SetAttributes(
		attribute.String("dispatch.outcome", outcomeName(res.Outcome)),
		attribute.Int("http.status_code", res.StatusCode),
	)
	if res.Outcome != dispatcher.OutcomeSuccess || res.Err != nil {
		span.SetStatus(codes.Error, "dispatch "+outcomeName(res.Outcome))
	}
	switch res.Outcome {
	case dispatcher.OutcomeSuccess:
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchAcked, projector.DispatchPayload(d.ID, d.ExecutionID, domain.DispatchAcked, res.AttemptCount)); err != nil {
			return err
		}
		return k.d.Queue.Ack(ctx, d.ID, domain.DispatchAcked, nil)
	default:
		if d.Attempt >= d.MaxAttempts {
			if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchExhausted, projector.DispatchPayload(d.ID, d.ExecutionID, domain.DispatchExhausted, d.Attempt)); err != nil {
				return err
			}
			_ = k.FailExecution(ctx, d.ExecutionID, "dispatch_exhausted")
			return k.d.Queue.Ack(ctx, d.ID, domain.DispatchExhausted, nil)
		}
		next := time.Now().UTC().Add(dispatcher.BackoffDelay(k.cfg.DispatchBaseDelay, k.cfg.DispatchMaxDelay, d.Attempt))
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchFailed, projector.DispatchPayload(d.ID, d.ExecutionID, domain.DispatchFailed, d.Attempt)); err != nil {
			return err
		}
		return k.d.Queue.Ack(ctx, d.ID, domain.DispatchFailed, &next)
	}
}

func outcomeName(o dispatcher.Outcome) string {
	switch o {
	case dispatcher.OutcomeSuccess:
		return "success"
	case dispatcher.OutcomeRejected:
		return "rejected"
	default:
		return "exhausted"
	}
}
