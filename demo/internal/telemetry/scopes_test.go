package telemetry

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestScopeFilter(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := scopeFilter{tp: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)), drop: droppedScopes}

	ctx, parent := tp.Tracer("app").Start(context.Background(), "GET /x")
	_, child := tp.Tracer("jsonEvaluator").Start(ctx, "resolveInt")
	child.End()
	parent.End()

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /x" {
		names := make([]string, len(spans))
		for i, s := range spans {
			names[i] = s.Name()
		}
		t.Fatalf("exported %v, want only GET /x", names)
	}
}
