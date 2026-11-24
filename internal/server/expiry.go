package server

import (
	"context"
	"log/slog"
	"time"
)

// ExpiryInterval is how often the API sweeps expired labs (design 3.8).
const ExpiryInterval = 10 * time.Minute

// Expirer runs one expiry sweep and returns how many labs it expired.
type Expirer interface {
	ExpireLabs(ctx context.Context) (int, error)
}

// RunExpiry sweeps once at startup, then every interval until ctx ends. A failed sweep is logged
// and retried next tick.
func RunExpiry(ctx context.Context, e Expirer, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := e.ExpireLabs(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			slog.Error("expiry sweep failed", "error", err)
		case n > 0:
			slog.Info("expired stale labs", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
