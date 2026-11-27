// Package service is the common entry point of the Go demo services: it sets
// up telemetry and the fault hook, builds the service's handler and serves it
// until the process is told to stop.
//
// The fault hook reads flags named after OTEL_SERVICE_NAME from flagd
// (FLAGD_HOST). See demo/README.md for the flags.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/dinhostork/IncidentLens/demo/internal/fault"
	"github.com/dinhostork/IncidentLens/demo/internal/httpx"
	"github.com/dinhostork/IncidentLens/demo/internal/telemetry"
)

// Setup builds the service. cleanup runs after the server has stopped.
type Setup func(ctx context.Context, hook fault.Hook) (h http.Handler, cleanup func(), err error)

// Main runs the service and exits the process when it stops. It listens on
// $PORT (default 8080).
func Main(setup Setup) {
	if err := run(setup); err != nil {
		os.Exit(1)
	}
}

func run(setup Setup) (err error) {
	ctx := context.Background()
	shutdown, err := telemetry.Setup(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry:", err)
		return err
	}
	defer func() {
		// Flush before exiting, including the log record for a failed start.
		if err != nil {
			slog.Error("exiting", "error", err.Error())
		}
		// See httpx.ShutdownTimeout: both together stay under the 15s
		// stop_grace_period set in compose.
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if serr := shutdown(sctx); serr != nil {
			fmt.Fprintln(os.Stderr, "telemetry shutdown:", serr)
		}
	}()

	name, err := RequireEnv("OTEL_SERVICE_NAME")
	if err != nil {
		return err
	}
	flags, closeFlags, err := fault.ConnectFlagd(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_ = closeFlags(cctx)
	}()
	hook := fault.NewInjector(name, flags)

	h, cleanup, err := setup(ctx, hook)
	if err != nil {
		return err
	}
	defer cleanup()
	return httpx.ListenAndServe(ctx, ":"+Env("PORT", "8080"), h)
}

// Env returns the environment variable key, or def when it is unset or empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// RequireEnv returns the environment variable key or an error if it is unset.
func RequireEnv(key string) (string, error) {
	if v := os.Getenv(key); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is not set", key)
}
