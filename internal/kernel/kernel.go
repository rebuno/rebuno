package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/observe"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/projector"
	"github.com/rebuno/rebuno/internal/store"
	memqueue "github.com/rebuno/rebuno/internal/store/memory"
)

type KernelConfig struct {
	ExecutionTimeout time.Duration
	StepTimeout      time.Duration
	AgentTimeout     time.Duration
	RetryBaseDelay   time.Duration
	RetryMaxDelay    time.Duration
}

func DefaultKernelConfig() KernelConfig {
	return KernelConfig{
		ExecutionTimeout: time.Hour,
		StepTimeout:      5 * time.Minute,
		AgentTimeout:     30 * time.Second,
		RetryBaseDelay:   1 * time.Second,
		RetryMaxDelay:    30 * time.Second,
	}
}

type Kernel struct {
	events      store.EventStore
	checkpoints store.CheckpointStore
	agentHub    store.AgentHub
	runnerHub   store.RunnerHub
	signals     store.SignalStore
	sessions    store.SessionStore
	runners     store.RunnerStore
	locker      store.Locker
	projector   *projector.Projector
	policy      policy.Engine
	config      KernelConfig
	logger      *slog.Logger
	metrics     *observe.Metrics
	watcher     *executionWatcher
	jobQueue    store.JobQueue

	retryWg   sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
}

type Deps struct {
	Events      store.EventStore
	Checkpoints store.CheckpointStore
	AgentHub    store.AgentHub
	RunnerHub   store.RunnerHub
	Signals     store.SignalStore
	Sessions    store.SessionStore
	Runners     store.RunnerStore
	Locker      store.Locker
	Policy      policy.Engine
	Config      KernelConfig
	Logger      *slog.Logger
	Metrics     *observe.Metrics
	JobQueue    store.JobQueue
}

func NewKernel(d Deps) *Kernel {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cfg := d.Config
	defaults := DefaultKernelConfig()
	if cfg.ExecutionTimeout == 0 {
		cfg.ExecutionTimeout = defaults.ExecutionTimeout
	}
	if cfg.StepTimeout == 0 {
		cfg.StepTimeout = defaults.StepTimeout
	}
	if cfg.AgentTimeout == 0 {
		cfg.AgentTimeout = defaults.AgentTimeout
	}
	if cfg.RetryBaseDelay == 0 {
		cfg.RetryBaseDelay = defaults.RetryBaseDelay
	}
	if cfg.RetryMaxDelay == 0 {
		cfg.RetryMaxDelay = defaults.RetryMaxDelay
	}

	proj := projector.New(d.Events, d.Checkpoints, logger)

	jobQueue := d.JobQueue
	if jobQueue == nil {
		jobQueue = memqueue.NewJobQueue()
	}

	return &Kernel{
		events:      d.Events,
		checkpoints: d.Checkpoints,
		agentHub:    d.AgentHub,
		runnerHub:   d.RunnerHub,
		signals:     d.Signals,
		sessions:    d.Sessions,
		runners:     d.Runners,
		locker:      d.Locker,
		projector:   proj,
		policy:      d.Policy,
		config:      cfg,
		logger:      logger,
		metrics:     d.Metrics,
		watcher:     newExecutionWatcher(),
		jobQueue:    jobQueue,
		done:        make(chan struct{}),
	}
}

func (k *Kernel) Close() {
	k.closeOnce.Do(func() {
		close(k.done)
	})
}

func (k *Kernel) Shutdown() {
	k.Close()
	k.retryWg.Wait()
}

func (k *Kernel) StepTimeout() time.Duration {
	return k.config.StepTimeout
}

func (k *Kernel) AgentTimeout() time.Duration {
	return k.config.AgentTimeout
}

func (k *Kernel) Projector() *projector.Projector { return k.projector }

// WatchExecution registers for notifications when new events are emitted for the given execution.
// Returns a notification channel and a cleanup function.
func (k *Kernel) WatchExecution(executionID string) (<-chan struct{}, func()) {
	return k.watcher.Watch(executionID)
}

func (k *Kernel) EmitEvent(
	ctx context.Context,
	executionID string,
	stepID string,
	eventType domain.EventType,
	payload any,
	causationID uuid.UUID,
	correlationID uuid.UUID,
) (domain.Event, error) {
	eventID := uuid.Must(uuid.NewV7())

	if correlationID == uuid.Nil {
		correlationID = eventID
	}
	if causationID == uuid.Nil {
		causationID = eventID
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("marshaling event payload: %w", err)
	}

	evt := domain.Event{
		ID:            eventID,
		ExecutionID:   executionID,
		StepID:        stepID,
		Type:          eventType,
		SchemaVersion: 1,
		Timestamp:     time.Now(),
		Payload:       raw,
		CausationID:   causationID,
		CorrelationID: correlationID,
	}

	if err := k.events.Append(ctx, evt); err != nil {
		return domain.Event{}, fmt.Errorf("appending event %s: %w", eventType, err)
	}

	k.watcher.Notify(executionID)

	k.logger.Debug("event emitted",
		slog.String("event_id", eventID.String()),
		slog.String("execution_id", executionID),
		slog.String("type", string(eventType)),
	)

	return evt, nil
}

func (k *Kernel) EmitEvents(
	ctx context.Context,
	executionID string,
	correlationID uuid.UUID,
	entries []eventEntry,
) ([]domain.Event, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	if correlationID == uuid.Nil {
		correlationID = uuid.Must(uuid.NewV7())
	}

	events := make([]domain.Event, 0, len(entries))
	prevID := uuid.Nil
	now := time.Now()

	for _, e := range entries {
		eventID := uuid.Must(uuid.NewV7())
		causation := prevID
		if causation == uuid.Nil {
			causation = eventID
		}

		raw, err := json.Marshal(e.payload)
		if err != nil {
			return nil, fmt.Errorf("marshaling event payload: %w", err)
		}

		evt := domain.Event{
			ID:            eventID,
			ExecutionID:   executionID,
			StepID:        e.stepID,
			Type:          e.eventType,
			SchemaVersion: 1,
			Timestamp:     now,
			Payload:       raw,
			CausationID:   causation,
			CorrelationID: correlationID,
		}
		events = append(events, evt)
		prevID = eventID
	}

	if err := k.events.AppendBatch(ctx, events); err != nil {
		return nil, fmt.Errorf("appending event batch: %w", err)
	}

	k.watcher.Notify(executionID)

	for _, evt := range events {
		k.logger.Debug("event emitted (batch)",
			slog.String("event_id", evt.ID.String()),
			slog.String("execution_id", executionID),
			slog.String("type", string(evt.Type)),
		)
	}

	return events, nil
}

type eventEntry struct {
	stepID    string
	eventType domain.EventType
	payload   any
}

func (k *Kernel) maybeCheckpoint(ctx context.Context, state *domain.ExecutionState, eventType domain.EventType) {
	if !projector.ShouldCheckpoint(eventType) {
		return
	}
	if err := k.projector.SaveCheckpoint(ctx, state); err != nil {
		k.logger.Warn("failed to save checkpoint",
			slog.String("execution_id", state.Execution.ID),
			slog.String("error", err.Error()),
		)
	}
}
