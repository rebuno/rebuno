package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/dispatcher"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/payload"
	"github.com/rebuno/rebuno/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

func (k *Kernel) CompleteExecution(ctx context.Context, execID uuid.UUID, lease domain.Lease, output json.RawMessage) error {
	if !lease.Valid() {
		return fmt.Errorf("%w: missing dispatch lease", domain.ErrValidation)
	}
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
		if err := tx.RenewLease(ctx, execID, lease, time.Now().UTC()); err != nil {
			return err
		}
		if _, err := tx.Append(ctx, execID, domain.EventExecutionCompleted, payload.Execution(execID, domain.ExecutionCompleted, output, "")); err != nil {
			return err
		}
		if err := tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionCompleted, output, ""); err != nil {
			return err
		}
		return releaseDispatchesLocked(ctx, tx, execID)
	}); err != nil {
		return err
	}
	k.d.Observer.RecordExecutionTerminal(string(domain.ExecutionCompleted))
	return nil
}

func (k *Kernel) FailExecution(ctx context.Context, execID uuid.UUID, lease domain.Lease, reason string) error {
	if !lease.Valid() {
		return fmt.Errorf("%w: missing dispatch lease", domain.ErrValidation)
	}
	return k.failExecution(ctx, execID, lease, reason)
}

func (k *Kernel) failExecution(ctx context.Context, execID uuid.UUID, lease domain.Lease, reason string) error {
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
		if err := tx.RenewLease(ctx, execID, lease, time.Now().UTC()); err != nil {
			return err
		}
		if _, err := tx.Append(ctx, execID, domain.EventExecutionFailed, payload.Execution(execID, domain.ExecutionFailed, nil, reason)); err != nil {
			return err
		}
		if err := tx.UpdateExecutionStatus(ctx, execID, domain.ExecutionFailed, nil, reason); err != nil {
			return err
		}
		return releaseDispatchesLocked(ctx, tx, execID)
	}); err != nil {
		return err
	}
	k.d.Observer.RecordExecutionTerminal(string(domain.ExecutionFailed))
	return nil
}

func (k *Kernel) EnqueueReDrive(ctx context.Context, execID uuid.UUID) error {
	return k.enqueueDispatch(ctx, execID)
}

func (k *Kernel) Heartbeat(ctx context.Context, execID uuid.UUID, lease domain.Lease) error {
	if !lease.Valid() {
		return fmt.Errorf("%w: missing dispatch lease", domain.ErrValidation)
	}
	return k.d.Queue.RenewLease(ctx, execID, lease, time.Now().UTC())
}

const (
	dispatchPoll = 250 * time.Millisecond
	reapInterval = 2 * time.Second
)

func (k *Kernel) reap(ctx context.Context) {
	// Reaping is bounded by the lease, but a short lease has to be reaped sooner.
	t := time.NewTicker(max(time.Millisecond, min(reapInterval, k.cfg.DispatchLeaseTimeout/4)))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := k.reclaimAllStalled(ctx); err != nil {
				k.workerError("reclaim stalled dispatches", err)
			}
		}
	}
}

// RunDispatcher logs store errors and backs off rather than returning them.
func (k *Kernel) RunDispatcher(ctx context.Context) error {
	return k.runDispatches(ctx, dispatchPoll)
}

func (k *Kernel) DrainDispatches(ctx context.Context) error {
	return k.runDispatches(ctx, 0)
}

func (k *Kernel) runDispatches(ctx context.Context, poll time.Duration) error {
	concurrency := k.cfg.DispatchConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	active := 0
	completed := make(chan struct{}, concurrency)
	defer func() {
		for ; active > 0; active-- {
			<-completed
		}
	}()

	// A replica starting after a crash reclaims before the reaper's first tick.
	if err := k.reclaimAllStalled(ctx); err != nil {
		if poll <= 0 {
			return err
		}
		k.workerError("reclaim stalled dispatches", err)
	}
	if poll > 0 {
		go k.reap(ctx)
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if active == concurrency {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-completed:
				active--
			}
		}
	idle:
		for active > 0 {
			select {
			case <-completed:
				active--
			default:
				break idle
			}
		}

		jobs, err := k.d.Queue.Claim(ctx, k.cfg.ReplicaID, concurrency-active, time.Now().UTC())
		if err != nil {
			if poll <= 0 {
				return err
			}
			k.workerError("claim dispatches", err)
			if err := wait(ctx, poll); err != nil {
				return err
			}
			continue
		}
		k.d.Observer.RecordQueueDepth(len(jobs))
		active += len(jobs)
		for _, job := range jobs {
			go func(job domain.Dispatch) {
				defer func() { completed <- struct{}{} }()
				if err := k.deliver(ctx, job); err != nil {
					k.log.Info("delivery error", "dispatch_id", job.ID, "err", err)
				}
			}(job)
		}
		if len(jobs) > 0 {
			k.log.Info("dispatch drain", "jobs", len(jobs))
			continue
		}
		if poll <= 0 {
			return nil
		}
		if err := wait(ctx, poll); err != nil {
			return err
		}
	}
}

func (k *Kernel) workerError(msg string, err error) {
	k.d.Observer.RecordWorkerError("dispatch")
	k.log.Error(msg, "err", err)
}

func (k *Kernel) leaseTimeout(agent domain.Agent) time.Duration {
	if agent.LeaseTimeoutSeconds > 0 {
		return time.Duration(agent.LeaseTimeoutSeconds * float64(time.Second))
	}
	return k.cfg.DispatchLeaseTimeout
}

// reclaimBatch bounds one statement's locks; reclaiming pages until drained.
const reclaimBatch = 100

func (k *Kernel) reclaimAllStalled(ctx context.Context) error {
	for {
		reclaimed, err := k.d.Queue.ReclaimStalled(ctx, time.Now().UTC(), k.cfg.DispatchLeaseTimeout, reclaimBatch)
		if err != nil {
			return err
		}
		k.d.Observer.RecordReclaimedStalled(len(reclaimed))
		if len(reclaimed) < reclaimBatch {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
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

	lease := domain.Lease{DispatchID: d.ID, Attempt: d.Attempt}
	exec, err := k.d.Executions.GetExecution(ctx, d.ExecutionID)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	if exec.Status.IsTerminal() {
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchDiscarded, payload.Dispatch(d.ID, d.ExecutionID, domain.DispatchExhausted, d.Attempt)); err != nil {
			return err
		}
		return ignoreSuperseded(k.d.Queue.Ack(ctx, d.ID, d.Attempt, domain.DispatchExhausted, nil))
	}

	if d.Attempt > d.MaxAttempts {
		return ignoreSuperseded(k.failExecution(ctx, d.ExecutionID, lease, domain.ReasonDispatchExhausted))
	}
	agent, err := k.d.Agents.GetAgent(ctx, exec.AgentID)
	if err != nil {
		return err
	}
	start := time.Now()
	res := k.d.Dispatcher.Deliver(ctx, agent.WebhookURL, agent.Secret, d.ExecutionID, lease, k.leaseTimeout(agent))
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
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchAcked, payload.Dispatch(d.ID, d.ExecutionID, domain.DispatchAcked, d.Attempt)); err != nil {
			return err
		}
		return nil
	default:
		if _, err := k.d.Events.Append(ctx, d.ExecutionID, domain.EventDispatchFailed, payload.Dispatch(d.ID, d.ExecutionID, domain.DispatchFailed, d.Attempt)); err != nil {
			return err
		}
		if d.Attempt >= d.MaxAttempts {
			return ignoreSuperseded(k.failExecution(ctx, d.ExecutionID, lease, domain.ReasonDispatchExhausted))
		}
		next := time.Now().UTC().Add(dispatcher.BackoffDelay(k.cfg.DispatchBaseDelay, k.cfg.DispatchMaxDelay, d.Attempt))
		return ignoreSuperseded(k.d.Queue.Ack(ctx, d.ID, d.Attempt, domain.DispatchFailed, &next))
	}
}

func ignoreSuperseded(err error) error {
	if errors.Is(err, domain.ErrLeaseSuperseded) {
		return nil
	}
	return err
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
