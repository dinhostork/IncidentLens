package fault

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	flagd "github.com/open-feature/go-sdk-contrib/providers/flagd/pkg"
	"github.com/open-feature/go-sdk/openfeature"
)

// ConnectFlagd sets flagd as the OpenFeature provider and returns Flags that
// read from it, plus a function that closes the connection.
//
// The provider runs in-process: it keeps a copy of the flag definitions,
// synced from flagd (FLAGD_HOST, port 8015 unless FLAGD_SYNC_PORT says
// otherwise), and evaluates locally. Changes in flagd arrive over the sync
// stream within a second. If flagd is unreachable at startup every flag reads
// as off until the first sync, and if it goes away later the last synced
// values stay in effect.
//
// The connection is not instrumented and the provider logs to stderr only,
// so nothing about fault injection reaches the service's telemetry.
func ConnectFlagd(ctx context.Context) (Flags, func(context.Context) error, error) {
	// Not slog.Default: that one exports to OpenTelemetry.
	log := logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	p, err := flagd.NewProvider(flagd.WithInProcessResolver(), flagd.WithLogger(log))
	if err != nil {
		return nil, nil, fmt.Errorf("flagd provider: %w", err)
	}
	// Does not wait for the first sync. Until then flags read as off.
	if err := openfeature.SetProviderWithContext(ctx, p); err != nil {
		return nil, nil, fmt.Errorf("flagd provider: %w", err)
	}
	return openFeatureFlags{openfeature.NewDefaultClient()}, openfeature.ShutdownWithContext, nil
}

type openFeatureFlags struct{ c *openfeature.Client }

func (f openFeatureFlags) Int(ctx context.Context, key string) int64 {
	v, _ := f.c.IntValue(ctx, key, 0, openfeature.EvaluationContext{})
	return v
}

func (f openFeatureFlags) Float(ctx context.Context, key string) float64 {
	v, _ := f.c.FloatValue(ctx, key, 0, openfeature.EvaluationContext{})
	return v
}
