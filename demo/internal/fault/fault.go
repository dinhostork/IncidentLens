// Package fault is where fault injection plugs into the demo services.
//
// Every inbound request and every outbound call (HTTP or database) passes
// through Hook.Before before it runs. The hook can delay the call by blocking
// or fail it by returning an error. The services only ever see the Hook
// interface, so the flag-driven implementation can replace Nop without
// touching handlers.
//
// Injected failures should look like real ones (a timeout, a refused
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
}

// Hook runs before every inbound request and outbound call. A non-nil error
// fails the call as if the dependency (or the handler) had failed.
type Hook interface {
	Before(ctx context.Context, p Point) error
}

// Nop is a Hook that never injects anything.
type Nop struct{}

// Before implements Hook.
func (Nop) Before(context.Context, Point) error { return nil }
