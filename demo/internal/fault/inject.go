package fault

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"syscall"
	"time"
)

// Fault names, the middle part of a flag key. Keys are
// "<service>.<fault>[.<dependency>]", for example "payments.latency_ms.postgres".
const (
	// LatencyMS delays every call to a dependency by this many milliseconds.
	LatencyMS = "latency_ms"
	// ErrorRate fails this fraction (0 to 1) of calls to a dependency, or of
	// the service's own requests when no dependency is given.
	ErrorRate = "error_rate"
	// PoolSize caps the database connections in use at once.
	PoolSize = "pool_size"
	// CPUBurnMS keeps a CPU busy for this many milliseconds on every request.
	CPUBurnMS = "cpu_burn_ms"
	// CacheDisabled turns off catalog's Redis cache. Only catalog reads it.
	CacheDisabled = "cache_disabled"
)

// Key returns the flag key for a fault. dependency is empty for faults that
// apply to the service itself.
func Key(service, fault, dependency string) string {
	k := service + "." + fault
	if dependency != "" {
		k += "." + dependency
	}
	return k
}

// Flags reads fault flags. Implementations return the zero value when a flag
// is off, unknown or cannot be evaluated, so a broken flag source means no
// faults rather than failing requests.
type Flags interface {
	Int(ctx context.Context, key string) int64
	Float(ctx context.Context, key string) float64
}

// Injector is a Hook that applies the faults its flags turn on. Flags are
// read on every call, so a change in flagd applies to the next request.
type Injector struct {
	service string
	flags   Flags
	rand    func() float64
}

// NewInjector returns an Injector for service, which is the first part of
// every flag key it reads.
func NewInjector(service string, flags Flags) *Injector {
	return &Injector{service: service, flags: flags, rand: rand.Float64}
}

// Before implements Hook.
//
// Inbound: burn CPU for cpu_burn_ms, then fail error_rate of requests.
// Outbound: wait latency_ms.<dependency>, then fail error_rate.<dependency> of
// calls. The wait ends early, with the context's error, when the caller gives
// up, which is what a slow dependency looks like to a caller with a timeout.
func (in *Injector) Before(ctx context.Context, p Point) error {
	if p.Kind == Inbound {
		burnCPU(millis(in.flags.Int(ctx, Key(in.service, CPUBurnMS, ""))))
		if in.hit(ctx, Key(in.service, ErrorRate, "")) {
			return errHandler
		}
		return nil
	}
	if d := millis(in.flags.Int(ctx, Key(in.service, LatencyMS, p.Dependency))); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if in.hit(ctx, Key(in.service, ErrorRate, p.Dependency)) {
		return connectionReset(p.Address)
	}
	return nil
}

// PoolSize implements Hook.
func (in *Injector) PoolSize(ctx context.Context) int {
	return int(max(in.flags.Int(ctx, Key(in.service, PoolSize, "")), 0))
}

func (in *Injector) hit(ctx context.Context, key string) bool {
	rate := in.flags.Float(ctx, key)
	return rate > 0 && in.rand() < rate
}

func millis(n int64) time.Duration {
	return time.Duration(max(n, 0)) * time.Millisecond
}

// burnCPU spins for d. It ignores cancellation, as real CPU-bound work would.
func burnCPU(d time.Duration) {
	if d <= 0 {
		return
	}
	for start := time.Now(); time.Since(start) < d; {
	}
}

// errHandler is what an injected handler failure returns: the message of the
// most common Go bug, a nil pointer dereference recovered as an error.
var errHandler = errors.New("runtime error: invalid memory address or nil pointer dereference")

// connectionReset returns the error a Go client gets when the other side
// resets the connection mid-request.
func connectionReset(address string) error {
	var a net.Addr
	if address != "" {
		a = tcpAddr(address)
	}
	return &net.OpError{Op: "read", Net: "tcp", Addr: a, Err: os.NewSyscallError("read", syscall.ECONNRESET)}
}

type tcpAddr string

func (tcpAddr) Network() string  { return "tcp" }
func (a tcpAddr) String() string { return string(a) }
