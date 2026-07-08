package memstore

import (
	"context"
)

func (s *Store) Acquire(ctx context.Context, key string) (func(), error) {
	s.lockMtx.Lock()
	ch, ok := s.lockers[key]
	if !ok {
		ch = make(chan struct{}, 1)
		s.lockers[key] = ch
	}
	s.lockMtx.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Store) TryAcquire(ctx context.Context, key string) (func(), error) {
	return s.Acquire(ctx, key)
}
