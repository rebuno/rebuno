// Package projector reconstructs ExecutionState by folding events in sequence order.
package projector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

const eventPageSize = 500

type EventHandler func(state *domain.ExecutionState, evt *domain.Event) error

type Projector struct {
	events      store.EventStore
	checkpoints store.CheckpointStore
	logger      *slog.Logger
	handlers    map[domain.EventType]EventHandler
}

func New(events store.EventStore, checkpoints store.CheckpointStore, logger *slog.Logger) *Projector {
	if events == nil {
		panic("projector: EventStore must not be nil")
	}
	if checkpoints == nil {
		panic("projector: CheckpointStore must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &Projector{
		events:      events,
		checkpoints: checkpoints,
		logger:      logger,
		handlers:    make(map[domain.EventType]EventHandler),
	}
	registerExecutionHandlers(p)
	registerStepHandlers(p)
	registerSignalHandlers(p)
	return p
}

func (p *Projector) Register(eventType domain.EventType, handler EventHandler) {
	p.handlers[eventType] = handler
}

func (p *Projector) Project(ctx context.Context, executionID string) (*domain.ExecutionState, error) {
	state, afterSeq, err := p.loadBase(ctx, executionID)
	if err != nil {
		return nil, fmt.Errorf("projector: loading base state for %s: %w", executionID, err)
	}

	if err := p.replay(ctx, executionID, state, afterSeq); err != nil {
		return nil, fmt.Errorf("projector: replaying events for %s: %w", executionID, err)
	}

	return state, nil
}

func (p *Projector) SaveCheckpoint(ctx context.Context, state *domain.ExecutionState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("projector: marshaling checkpoint for %s: %w", state.Execution.ID, err)
	}

	cp := domain.Checkpoint{
		ExecutionID: state.Execution.ID,
		Sequence:    state.LastSequence,
		StateData:   data,
		CreatedAt:   time.Now(),
	}

	if err := p.checkpoints.Save(ctx, cp); err != nil {
		return fmt.Errorf("projector: saving checkpoint for %s at seq %d: %w",
			state.Execution.ID, state.LastSequence, err)
	}

	p.logger.Debug("checkpoint saved",
		slog.String("execution_id", state.Execution.ID),
		slog.Int64("sequence", state.LastSequence),
	)
	return nil
}

func (p *Projector) loadBase(ctx context.Context, executionID string) (*domain.ExecutionState, int64, error) {
	cp, found, err := p.checkpoints.Get(ctx, executionID)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching checkpoint: %w", err)
	}

	if found {
		var state domain.ExecutionState
		if err := json.Unmarshal(cp.StateData, &state); err != nil {
			p.logger.Warn("corrupt checkpoint, replaying from beginning",
				slog.String("execution_id", executionID),
				slog.String("error", err.Error()),
			)
			return newEmptyState(executionID), 0, nil
		}
		p.logger.Debug("resuming from checkpoint",
			slog.String("execution_id", executionID),
			slog.Int64("sequence", cp.Sequence),
		)
		return &state, cp.Sequence, nil
	}

	return newEmptyState(executionID), 0, nil
}

func (p *Projector) replay(ctx context.Context, executionID string, state *domain.ExecutionState, afterSeq int64) error {
	cursor := afterSeq
	for {
		events, err := p.events.GetByExecution(ctx, executionID, cursor, eventPageSize)
		if err != nil {
			return fmt.Errorf("fetching events after seq %d: %w", cursor, err)
		}
		if len(events) == 0 {
			break
		}

		for i := range events {
			evt := &events[i]

			if state.LastSequence > 0 && evt.Sequence != state.LastSequence+1 {
				p.markTainted(state, fmt.Sprintf(
					"sequence gap: expected %d, got %d", state.LastSequence+1, evt.Sequence))
				return nil
			}

			p.applyEvent(state, evt)
			state.LastSequence = evt.Sequence
		}

		cursor = events[len(events)-1].Sequence
		if len(events) < eventPageSize {
			break
		}
	}
	return nil
}

func (p *Projector) applyEvent(state *domain.ExecutionState, evt *domain.Event) {
	handler, ok := p.handlers[evt.Type]
	if !ok {
		p.logger.Warn("unknown event type during projection",
			slog.String("execution_id", state.Execution.ID),
			slog.String("event_type", string(evt.Type)),
			slog.Int64("sequence", evt.Sequence),
		)
		return
	}
	if err := handler(state, evt); err != nil {
		p.markTainted(state, err.Error())
	}
}

func (p *Projector) markTainted(state *domain.ExecutionState, reason string) {
	state.Tainted = true
	state.TaintedReason = reason
	p.logger.Warn("execution tainted",
		slog.String("execution_id", state.Execution.ID),
		slog.String("reason", reason),
	)
}

func ShouldCheckpoint(eventType domain.EventType) bool {
	switch eventType {
	case domain.EventStepCompleted,
		domain.EventExecutionStarted,
		domain.EventExecutionBlocked,
		domain.EventExecutionResumed,
		domain.EventExecutionCompleted,
		domain.EventExecutionFailed,
		domain.EventExecutionCancelled:
		return true
	default:
		return false
	}
}

func newEmptyState(executionID string) *domain.ExecutionState {
	return &domain.ExecutionState{
		Execution: domain.Execution{
			ID: executionID,
		},
		Steps: make(map[string]*domain.Step),
	}
}

func noOp(_ *domain.ExecutionState, _ *domain.Event) error { return nil }
