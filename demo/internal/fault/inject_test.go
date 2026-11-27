package fault

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// mapFlags serves flags from a map. Missing keys read as zero.
type mapFlags struct {
	ints   map[string]int64
	floats map[string]float64
	read   []string
}

func (f *mapFlags) Int(_ context.Context, key string) int64 {
	f.read = append(f.read, key)
	return f.ints[key]
}

func (f *mapFlags) Float(_ context.Context, key string) float64 {
	f.read = append(f.read, key)
	return f.floats[key]
}

func injector(flags *mapFlags, roll float64) *Injector {
	in := NewInjector("orders", flags)
	in.rand = func() float64 { return roll }
	return in
}

var paymentsCall = Point{Kind: Outbound, Dependency: "payments", Operation: "POST", Address: "payments:8080"}

func TestKey(t *testing.T) {
	if got := Key("payments", LatencyMS, "postgres"); got != "payments.latency_ms.postgres" {
		t.Errorf("Key = %q", got)
	}
	if got := Key("catalog", CacheDisabled, ""); got != "catalog.cache_disabled" {
		t.Errorf("Key = %q", got)
	}
}

func TestNoFlagsNoFaults(t *testing.T) {
	flags := &mapFlags{}
	in := injector(flags, 0)
	ctx := context.Background()
	if err := in.Before(ctx, Point{Kind: Inbound, Operation: "POST /carts"}); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if err := in.Before(ctx, paymentsCall); err != nil {
		t.Fatalf("outbound: %v", err)
	}
	if n := in.PoolSize(ctx); n != 0 {
		t.Fatalf("PoolSize = %d", n)
	}
	want := []string{
		"orders.cpu_burn_ms", "orders.error_rate",
		"orders.latency_ms.payments", "orders.error_rate.payments",
		"orders.pool_size",
	}
	if len(flags.read) != len(want) {
		t.Fatalf("read %v, want %v", flags.read, want)
	}
	for i := range want {
		if flags.read[i] != want[i] {
			t.Fatalf("read %v, want %v", flags.read, want)
		}
	}
}

func TestOutboundLatency(t *testing.T) {
	in := injector(&mapFlags{ints: map[string]int64{"orders.latency_ms.payments": 50}}, 0)
	start := time.Now()
	if err := in.Before(context.Background(), paymentsCall); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Fatalf("returned after %v, want at least 50ms", d)
	}
	// Other dependencies are not delayed.
	start = time.Now()
	if err := in.Before(context.Background(), Point{Kind: Outbound, Dependency: "catalog"}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Fatalf("catalog call delayed by %v", d)
	}
}

func TestOutboundLatencyStopsAtDeadline(t *testing.T) {
	in := injector(&mapFlags{ints: map[string]int64{"orders.latency_ms.payments": 5000}}, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := in.Before(ctx, paymentsCall)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v past the deadline", d)
	}
}

func TestOutboundErrorRate(t *testing.T) {
	flags := &mapFlags{floats: map[string]float64{"orders.error_rate.payments": 0.3}}

	if err := injector(flags, 0.5).Before(context.Background(), paymentsCall); err != nil {
		t.Fatalf("roll above the rate failed: %v", err)
	}
	err := injector(flags, 0.1).Before(context.Background(), paymentsCall)
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("err = %v, want connection reset", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || netErr.Timeout() {
		t.Fatalf("err = %#v, want a net.Error that is not a timeout", err)
	}
	if got, want := err.Error(), "read tcp payments:8080: read: connection reset by peer"; got != want {
		t.Fatalf("message %q, want %q", got, want)
	}
}

func TestInboundFaults(t *testing.T) {
	flags := &mapFlags{
		ints:   map[string]int64{"orders.cpu_burn_ms": 30},
		floats: map[string]float64{"orders.error_rate": 1},
	}
	start := time.Now()
	err := injector(flags, 0.99).Before(context.Background(), Point{Kind: Inbound, Operation: "POST /carts"})
	if d := time.Since(start); d < 30*time.Millisecond {
		t.Fatalf("returned after %v, want at least 30ms of CPU burn", d)
	}
	if !errors.Is(err, errHandler) {
		t.Fatalf("err = %v, want the handler error", err)
	}
	// A dependency's error rate does not fail inbound requests.
	flags = &mapFlags{floats: map[string]float64{"orders.error_rate.payments": 1}}
	if err := injector(flags, 0).Before(context.Background(), Point{Kind: Inbound}); err != nil {
		t.Fatalf("inbound failed on a dependency flag: %v", err)
	}
}

func TestPoolSize(t *testing.T) {
	in := injector(&mapFlags{ints: map[string]int64{"orders.pool_size": 2}}, 0)
	if n := in.PoolSize(context.Background()); n != 2 {
		t.Fatalf("PoolSize = %d, want 2", n)
	}
	in = injector(&mapFlags{ints: map[string]int64{"orders.pool_size": -1}}, 0)
	if n := in.PoolSize(context.Background()); n != 0 {
		t.Fatalf("PoolSize = %d for a negative flag, want 0", n)
	}
}
