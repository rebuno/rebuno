package kernel

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/dispatcher"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/observe"
	"github.com/rebuno/kernel/internal/policy"
	"github.com/rebuno/kernel/internal/projector"
	"github.com/rebuno/kernel/internal/ratelimit"
	"github.com/rebuno/kernel/internal/store"
)

type Config struct {
	DispatchMaxAttempts      int
	DispatchBaseDelay        time.Duration
	DispatchMaxDelay         time.Duration
	DispatchTimeout          time.Duration
	DispatchConcurrency      int
	DispatchLeaseTimeout     time.Duration
	DefaultApprovalTimeout   time.Duration
	ExecutionDeadlineTimeout time.Duration
	ExecutionCleanupInterval time.Duration
	ExecutionRetention       time.Duration
	LeaderLockKey            string
	ReplicaID                string
}

func DefaultConfig() Config {
	return Config{
		DispatchMaxAttempts:      5,
		DispatchBaseDelay:        1 * time.Second,
		DispatchMaxDelay:         30 * time.Second,
		DispatchTimeout:          30 * time.Second,
		DispatchConcurrency:      8,
		DispatchLeaseTimeout:     2 * time.Minute,
		DefaultApprovalTimeout:   15 * time.Minute,
		ExecutionCleanupInterval: 10 * time.Minute,
		ExecutionRetention:       24 * time.Hour,
		LeaderLockKey:            "rebuno_scheduler_leader",
		ReplicaID:                "replica-1",
	}
}

type Deps struct {
	Events      store.EventStore
	Steps       store.StepStore
	Executions  store.ExecutionStore
	Agents      store.AgentStore
	Approvals   store.ApprovalStore
	Queue       store.JobQueue
	Locker      store.Locker
	UnitOfWork  store.UnitOfWork
	Policy      policy.Engine
	Dispatcher  *dispatcher.Dispatcher
	RateLimiter ratelimit.Limiter
	Logger      *slog.Logger
	Observer    *observe.Observer
}

type Kernel struct {
	cfg Config
	d   Deps
	log *slog.Logger
}

func New(cfg Config, d Deps) *Kernel {
	for name, dep := range map[string]any{
		"Events":     d.Events,
		"Steps":      d.Steps,
		"Executions": d.Executions,
		"Agents":     d.Agents,
		"Approvals":  d.Approvals,
		"Queue":      d.Queue,
		"Locker":     d.Locker,
		"UnitOfWork": d.UnitOfWork,
	} {
		if dep == nil {
			panic("kernel.New: missing required dependency: " + name)
		}
	}
	// Default the dispatch timeout defensively: a zero timeout produces an
	// http.Client with no deadline, so a single unresponsive agent webhook would
	// occupy a delivery slot indefinitely. Set before the dispatcher is built.
	if cfg.DispatchTimeout <= 0 {
		cfg.DispatchTimeout = 30 * time.Second
	}
	if cfg.DispatchConcurrency <= 0 {
		cfg.DispatchConcurrency = 8
	}
	if d.Policy == nil {
		d.Policy = policy.PermissiveEngine{}
	}
	if d.Dispatcher == nil {
		d.Dispatcher = dispatcher.New(nil, dispatcher.Config{
			MaxAttempts: cfg.DispatchMaxAttempts,
			BaseDelay:   cfg.DispatchBaseDelay,
			MaxDelay:    cfg.DispatchMaxDelay,
			Timeout:     cfg.DispatchTimeout,
		}, d.Logger)
	}
	if d.RateLimiter == nil {
		d.RateLimiter = ratelimit.NoOp()
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	if d.Observer == nil {
		d.Observer = observe.Default()
	}
	if cfg.ReplicaID == "" {
		cfg.ReplicaID = uuid.NewString()
	}
	if cfg.DispatchLeaseTimeout == 0 {
		cfg.DispatchLeaseTimeout = 2 * time.Minute
	}
	if cfg.LeaderLockKey == "" {
		cfg.LeaderLockKey = "rebuno_scheduler_leader"
	}
	return &Kernel{cfg: cfg, d: d, log: log}
}

func (k *Kernel) CreateExecution(ctx context.Context, agentID string, input json.RawMessage, agentVersion string) (domain.Execution, error) {
	if _, err := k.d.Agents.GetAgent(ctx, agentID); err != nil {
		return domain.Execution{}, err
	}
	now := time.Now().UTC()
	exec := domain.Execution{
		ID:           uuid.Must(uuid.NewV7()),
		AgentID:      agentID,
		AgentVersion: agentVersion,
		Input:        input,
		Status:       domain.ExecutionPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if k.cfg.ExecutionDeadlineTimeout > 0 {
		deadline := now.Add(k.cfg.ExecutionDeadlineTimeout)
		exec.DeadlineAt = &deadline
	}
	createdPayload := projector.ExecutionPayload(exec.ID, exec.Status, nil, "")
	startedPayload := projector.ExecutionPayload(exec.ID, domain.ExecutionRunning, nil, "")
	if exec.DeadlineAt != nil {
		createdPayload["deadline_at"] = *exec.DeadlineAt
		startedPayload["deadline_at"] = *exec.DeadlineAt
	}
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if err := tx.CreateExecution(ctx, exec); err != nil {
			return err
		}
		if _, err := tx.AppendBatch(ctx, exec.ID, []store.EventRecord{
			{Type: domain.EventExecutionCreated, Payload: createdPayload},
			{Type: domain.EventExecutionStarted, Payload: startedPayload},
		}); err != nil {
			return err
		}
		if err := tx.UpdateExecutionStatus(ctx, exec.ID, domain.ExecutionRunning, nil, ""); err != nil {
			return err
		}
		return k.enqueueDispatchTx(ctx, tx, exec.ID)
	}); err != nil {
		return domain.Execution{}, err
	}
	k.d.Observer.RecordExecutionCreated()
	exec.Status = domain.ExecutionRunning
	return exec, nil
}

func (k *Kernel) Deps() Deps {
	return k.d
}

func (k *Kernel) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return k.d.Executions.GetExecution(ctx, id)
}

// MaxListExecutionsLimit caps the page size a caller can request.
const MaxListExecutionsLimit = 200

func (k *Kernel) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > MaxListExecutionsLimit {
		filter.Limit = MaxListExecutionsLimit
	}
	return k.d.Executions.ListExecutions(ctx, filter)
}

func (k *Kernel) CancelExecution(ctx context.Context, id uuid.UUID) error {
	release, err := k.d.Locker.Acquire(ctx, lockKey(id))
	if err != nil {
		return err
	}
	defer release()

	exec, err := k.d.Executions.GetExecution(ctx, id)
	if err != nil {
		return err
	}
	if exec.Status.IsTerminal() {
		return domain.ErrExecutionTerminal
	}
	now := time.Now().UTC()
	if err := k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		if _, err := tx.Append(ctx, id, domain.EventExecutionCancelled, projector.ExecutionPayload(id, domain.ExecutionCancelled, nil, "client_cancelled")); err != nil {
			return err
		}
		if err := tx.UpdateExecutionStatus(ctx, id, domain.ExecutionCancelled, nil, "client_cancelled"); err != nil {
			return err
		}
		for _, d := range allDispatchesLocked(tx, id) {
			if d.Status != domain.DispatchAcked && d.Status != domain.DispatchExhausted {
				if _, err := tx.Append(ctx, id, domain.EventDispatchExhausted, projector.DispatchPayload(d.ID, id, domain.DispatchExhausted, d.Attempt)); err != nil {
					return err
				}
				if err := tx.Ack(ctx, d.ID, domain.DispatchExhausted, nil); err != nil {
					return err
				}
			}
		}
		for _, a := range allPendingApprovalsLocked(tx, id) {
			errPayload, _ := json.Marshal(map[string]string{"reason": "execution_cancelled"})
			if _, err := tx.AppendBatch(ctx, id, []store.EventRecord{
				{Type: domain.EventApprovalExpired, Payload: projector.ApprovalPayload(a.ID, a.StepID, id, domain.ApprovalExpired, "", "execution_cancelled")},
				{Type: domain.EventStepDenied, Payload: projector.StepPayload(a.StepID, domain.StepKindTool, "", "")},
				{Type: domain.EventStepFailed, Payload: projector.StepErrorPayload(a.StepID, domain.StepKindTool, errPayload)},
			}); err != nil {
				return err
			}
			if step, err := tx.GetStep(ctx, a.StepID); err == nil {
				step.Status = domain.StepFailed
				step.Error = errPayload
				step.CompletedAt = &now
				_ = tx.Upsert(ctx, step)
			}
			a.Status = domain.ApprovalExpired
			a.DecidedAt = &now
			a.Rationale = "execution_cancelled"
			if err := tx.UpdateApproval(ctx, a); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	k.d.Observer.RecordExecutionTerminal(string(domain.ExecutionCancelled))
	return nil
}

func (k *Kernel) GetEvents(ctx context.Context, id uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	return k.d.Events.GetEvents(ctx, id, afterSeq, limit)
}

func lockKey(id uuid.UUID) string {
	return "execution:" + id.String()
}

func (k *Kernel) enqueueDispatch(ctx context.Context, execID uuid.UUID) error {
	return k.d.UnitOfWork.RunInTx(ctx, func(tx store.TxStore) error {
		return k.enqueueDispatchTx(ctx, tx, execID)
	})
}

func (k *Kernel) enqueueDispatchTx(ctx context.Context, tx store.TxStore, execID uuid.UUID) error {
	now := time.Now().UTC()
	d := domain.Dispatch{
		ID:            uuid.Must(uuid.NewV7()),
		ExecutionID:   execID,
		Status:        domain.DispatchPending,
		Attempt:       0,
		MaxAttempts:   k.cfg.DispatchMaxAttempts,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := tx.Enqueue(ctx, d); err != nil {
		return err
	}
	_, err := tx.Append(ctx, execID, domain.EventDispatchSent, projector.DispatchPayload(d.ID, execID, d.Status, d.Attempt))
	return err
}

func allDispatchesLocked(tx store.TxStore, execID uuid.UUID) []domain.Dispatch {
	out, _ := tx.ListDispatchesByExecution(context.Background(), execID)
	return out
}

func allPendingApprovalsLocked(tx store.TxStore, execID uuid.UUID) []domain.Approval {
	var out []domain.Approval
	all, _ := tx.ListPendingApprovals(context.Background())
	for _, a := range all {
		if a.ExecutionID == execID {
			out = append(out, a)
		}
	}
	return out
}
