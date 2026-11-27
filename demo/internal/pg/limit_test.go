package pg

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterCaps(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1)
	l := newLimiter(func(context.Context) int { return int(limit.Load()) })
	ctx := context.Background()

	release, err := l.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		r, err := l.acquire(ctx)
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("second acquire did not wait for the first slot")
	case <-time.After(30 * time.Millisecond):
	}
	release()
	select {
	case r := <-got:
		r()
	case <-time.After(time.Second):
		t.Fatal("second acquire still waiting after release")
	}

	// No cap: acquires do not wait, but are still counted.
	limit.Store(0)
	r1, _ := l.acquire(ctx)
	r2, _ := l.acquire(ctx)
	if l.inUse != 2 {
		t.Fatalf("inUse = %d, want 2", l.inUse)
	}
	r1()
	r2()
}

func TestLimiterContextEnds(t *testing.T) {
	l := newLimiter(func(context.Context) int { return 1 })
	release, _ := l.acquire(context.Background())
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if l.inUse != 1 {
		t.Fatalf("inUse = %d after a failed acquire, want 1", l.inUse)
	}
}
