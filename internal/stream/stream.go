package stream

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

const subBuffer = 64

type Delta struct {
	StepID string `json:"step_id"`
	Seq    int64  `json:"seq"`
	Data   string `json:"data"`
}

type Bus interface {
	Publish(ctx context.Context, execID uuid.UUID, d Delta) error
	Start(ctx context.Context, deliver func(execID uuid.UUID, d Delta)) error
}

type Hub struct {
	bus  Bus
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan Delta]struct{}
}

func NewHub(bus Bus) *Hub {
	return &Hub{bus: bus, subs: make(map[uuid.UUID]map[chan Delta]struct{})}
}

func (h *Hub) Start(ctx context.Context) error {
	return h.bus.Start(ctx, h.deliver)
}

func (h *Hub) Publish(ctx context.Context, execID uuid.UUID, d Delta) error {
	return h.bus.Publish(ctx, execID, d)
}

func (h *Hub) Subscribe(execID uuid.UUID) (<-chan Delta, func()) {
	ch := make(chan Delta, subBuffer)
	h.mu.Lock()
	m := h.subs[execID]
	if m == nil {
		m = make(map[chan Delta]struct{})
		h.subs[execID] = m
	}
	m[ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if m := h.subs[execID]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(h.subs, execID)
			}
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

// deliver fans a delta out to every local subscriber of execID with a
// non-blocking send: a full subscriber drops the delta.
func (h *Hub) deliver(execID uuid.UUID, d Delta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[execID] {
		select {
		case ch <- d:
		default:
		}
	}
}
