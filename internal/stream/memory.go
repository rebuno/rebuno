package stream

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type MemoryBus struct {
	mu      sync.Mutex
	deliver func(uuid.UUID, Delta)
}

func NewMemoryBus() *MemoryBus { return &MemoryBus{} }

func (b *MemoryBus) Start(ctx context.Context, deliver func(uuid.UUID, Delta)) error {
	b.mu.Lock()
	b.deliver = deliver
	b.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (b *MemoryBus) Publish(ctx context.Context, execID uuid.UUID, d Delta) error {
	b.mu.Lock()
	deliver := b.deliver
	b.mu.Unlock()
	if deliver != nil { // dropped if Publish races ahead of Start at boot
		deliver(execID, d)
	}
	return nil
}
