// Package telemetry sets up OpenTelemetry traces, metrics and logs for a demo
// service, following docs/telemetry-conventions.md.
//
// Exporters are configured from the standard OTEL_EXPORTER_OTLP_* variables.
// The resource takes OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES, and
// service.version from SERVICE_VERSION.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Setup installs global tracer, meter and logger providers, the W3C trace
// context propagator, and a default slog logger that writes to stderr and to
// OpenTelemetry. The returned function flushes and shuts everything down.
func Setup(ctx context.Context) (shutdown func(context.Context) error, err error) {
	res, err := newResource(ctx)
	if err != nil {
		return nil, err
	}

	var shutdowns []func(context.Context) error
	shutdown = func(ctx context.Context) error {
		var errs []error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			errs = append(errs, shutdowns[i](ctx))
		}
		return errors.Join(errs...)
	}
	fail := func(err error) (func(context.Context) error, error) {
		return nil, errors.Join(err, shutdown(ctx))
	}

	spanExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("trace exporter: %w", err))
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(spanExp))
	shutdowns = append(shutdowns, tp.Shutdown)
	otel.SetTracerProvider(scopeFilter{tp: tp, drop: droppedScopes})
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("metric exporter: %w", err))
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)
	shutdowns = append(shutdowns, mp.Shutdown)
	otel.SetMeterProvider(mp)

	logExp, err := otlploggrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("log exporter: %w", err))
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
	)
	shutdowns = append(shutdowns, lp.Shutdown)
	otel.SetLoggerProvider(lp)

	name := serviceName(res)
	slog.SetDefault(slog.New(fanout{
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
		otelslog.NewHandler(name, otelslog.WithLoggerProvider(lp)),
	}))

	return shutdown, nil
}

// newResource builds the SDK resource. Later sources win, so SERVICE_VERSION
// overrides a service.version set in OTEL_RESOURCE_ATTRIBUTES.
func newResource(ctx context.Context) (*resource.Resource, error) {
	opts := []resource.Option{
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithFromEnv(),
		resource.WithAttributes(semconv.ServiceInstanceID(uuid.NewString())),
	}
	if v := os.Getenv("SERVICE_VERSION"); v != "" {
		opts = append(opts, resource.WithAttributes(semconv.ServiceVersion(v)))
	}
	res, err := resource.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}
	return res, nil
}

func serviceName(res *resource.Resource) string {
	if v, ok := res.Set().Value(semconv.ServiceNameKey); ok {
		return v.AsString()
	}
	return "unknown_service"
}
