// Package httpx has the HTTP plumbing shared by the Go demo services: an
// instrumented server mux, instrumented clients for calling other services,
// and small JSON helpers.
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
)

// Mux is an http.ServeMux whose routes run the fault hook before the handler.
// Wrap it with Handler to get server spans and metrics.
type Mux struct {
	mux  *http.ServeMux
	hook fault.Hook
}

// NewMux returns a Mux that calls hook before every route. /healthz is
// registered already and bypasses the hook.
func NewMux(hook fault.Hook) *Mux {
	m := &Mux{mux: http.NewServeMux(), hook: hook}
	m.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return m
}

// HandleFunc registers h for pattern, a Go 1.22 ServeMux pattern with method
// ("GET /products/{id}").
func (m *Mux) HandleFunc(pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		p := fault.Point{Kind: fault.Inbound, Operation: r.Pattern}
		if err := m.hook.Before(r.Context(), p); err != nil {
			ServerError(w, r, http.StatusInternalServerError, "handler failed", err)
			return
		}
		h(w, r)
	})
}

// Handler wraps the mux with OpenTelemetry server instrumentation. Span names
// follow semantic conventions: "{method} {route}". Health checks are not
// traced.
func (m *Mux) Handler() http.Handler {
	return otelhttp.NewHandler(m.mux, "http.server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return r.Method
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz"
		}),
	)
}

// ShutdownTimeout bounds how long in-flight requests get after SIGTERM.
// Together with the telemetry flush in package service it has to stay under
// the container's stop grace period, or the last spans are lost.
const ShutdownTimeout = 5 * time.Second

// ListenAndServe serves h on addr until SIGINT or SIGTERM, then shuts down
// gracefully.
func ListenAndServe(ctx context.Context, addr string, h http.Handler) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.InfoContext(ctx, "listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
