// Command changes is the change events API. It records deploys, config
// changes, feature flag flips and notes in ClickHouse and lists them by time
// window. See README.md for the endpoints and settings.
//
// It sends no telemetry of its own: its spans would show up as one more
// service in every reconstruction of the demo.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dinhostork/IncidentLens/changes/api"
	"github.com/dinhostork/IncidentLens/changes/clickhouse"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("changes: " + err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	store, err := clickhouse.New(clickhouse.Config{
		URL:      env("CLICKHOUSE_URL", "http://127.0.0.1:8123"),
		User:     env("CLICKHOUSE_USER", "incidentlens"),
		Password: env("CLICKHOUSE_PASSWORD", "incidentlens"),
		Database: env("CLICKHOUSE_DATABASE", "incidentlens"),
	})
	if err != nil {
		return err
	}
	token := os.Getenv("CHANGES_TOKEN")
	addr := env("CHANGES_ADDR", "127.0.0.1:8090")
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.New(store, api.Options{Token: token, Logger: log}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr, "auth", token != "")

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
