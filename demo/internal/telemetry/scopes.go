package telemetry

import (
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
)

// droppedScopes are instrumentation scopes whose spans are not exported.
//
// The flagd provider evaluates fault flags in-process with flagd's JSON
// evaluator, which starts a span per evaluation ("resolveInt",
// "resolveFloat") and per flag sync ("flagSync") on the global tracer
// provider, with no option to turn that off from the provider. Several of
// them per request, as children of the request's spans, would put fault
// injection into the traces the engine analyzes.
var droppedScopes = map[string]bool{
	"jsonEvaluator": true,
}

// scopeFilter hands out a no-op tracer for the scopes in drop and the
// wrapped provider's tracer for everything else.
type scopeFilter struct {
	embedded.TracerProvider
	tp   trace.TracerProvider
	drop map[string]bool
}

func (f scopeFilter) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	if f.drop[name] {
		return noop.NewTracerProvider().Tracer(name)
	}
	return f.tp.Tracer(name, opts...)
}
