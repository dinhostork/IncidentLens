package pg

import (
	"context"
	"sync"
)

// limiter caps how many connections are in use at once. The cap is read on
// every acquire, so it can change at runtime. A cap of 0 or less means no cap
// beyond the pool's own size.
type limiter struct {
	limit func(context.Context) int

	mu    sync.Mutex
	inUse int
	// freed is closed, and replaced, every time a slot is released.
	freed chan struct{}
}

func newLimiter(limit func(context.Context) int) *limiter {
	return &limiter{limit: limit, freed: make(chan struct{})}
}

// acquire takes a slot, waiting until one is free or ctx is done. The
// returned function gives the slot back and must be called exactly once.
//
// A caller only waits while inUse is at or above a cap of at least 1, so a
// release will come and wake it. A raised cap takes effect on that wakeup.
func (l *limiter) acquire(ctx context.Context) (func(), error) {
	for {
		n := l.limit(ctx)
		l.mu.Lock()
		if n <= 0 || l.inUse < n {
			l.inUse++
			l.mu.Unlock()
			return l.release, nil
		}
		freed := l.freed
		l.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			// The same error a caller gets from pgxpool when its context
			// ends while it waits for a connection.
			return nil, ctx.Err()
		}
	}
}

func (l *limiter) release() {
	l.mu.Lock()
	l.inUse--
	close(l.freed)
	l.freed = make(chan struct{})
	l.mu.Unlock()
}
