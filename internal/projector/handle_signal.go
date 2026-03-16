package projector

import (
	"encoding/json"
	"fmt"

	"github.com/rebuno/rebuno/internal/domain"
)

func registerSignalHandlers(p *Projector) {
	p.Register(domain.EventSignalReceived, applySignalReceived)
}

func applySignalReceived(state *domain.ExecutionState, evt *domain.Event) error {
	var payload domain.SignalReceivedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal signal.received payload: %w", err)
	}

	sig := domain.Signal{
		ID:          evt.ID.String(),
		ExecutionID: evt.ExecutionID,
		SignalType:  payload.SignalType,
		Payload:     payload.Payload,
		CreatedAt:   evt.Timestamp,
	}
	state.PendingSignals = append(state.PendingSignals, sig)
	return nil
}
