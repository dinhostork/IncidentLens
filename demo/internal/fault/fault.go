// Package fault is where fault injection plugs into the demo services.
//
// Every inbound request and every outbound call (HTTP or database) passes
// through Hook.Before before it runs. The hook can delay the call by blocking
// or fail it by returning an error. The services only ever see the Hook
// interface. Injector is the implementation driven by flags in flagd, Nop
// the one that does nothing.
//
// Injected failures should look like real ones (a timeout, a reset
// connection, a 500), so the engine cannot tell them apart by error type or
// message.
package fault

import "context"

// Kind tells inbound requests apart from outbound calls.
type Kind int

const (
	// Inbound is a request handled by the service itself.
	Inbound Kind = iota
	// Outbound is a call the service makes to a dependency.
	Outbound
)

func (k Kind) String() string {
	if k == Inbound {
		return "inbound"
	}
	return "outbound"
}

// Point identifies where in a service a hook runs.
type Point struct {
	Kind Kind
	// Dependency is the name of the called dependency ("payments", "postgres")
	// for outbound calls, empty for inbound requests.
	Dependency string
	// Operation is the HTTP route or the database operation, for context.
	Operation string
	// Address is the dependency's host:port for outbound calls. Injected
	// errors use it so they read like real network errors.
	Address string
}

// Hook decides which faults apply to a service.
type Hook interface {
	// Before runs before every inbound request and outbound call. A non-nil
	// error fails the call as if the dependency (or the handler) had failed.
	Before(ctx context.Context, p Point) error
	// PoolSize returns how many database connections the service may use at
	// once, or 0 to use the pool as configured.
	PoolSize(ctx context.Context) int
}

// Nop is a Hook that never injects anything.
type Nop struct{}

// Before implements Hook.
func (Nop) Before(context.Context, Point) error { return nil }

// PoolSize implements Hook.
func (Nop) PoolSize(context.Context) int { return 0 }
