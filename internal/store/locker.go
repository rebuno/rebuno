package store

import "context"

type Locker interface {
	Acquire(ctx context.Context, key string) (release func(), err error)
}
