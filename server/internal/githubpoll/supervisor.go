package githubpoll

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// RunWithRecover keeps fn alive across panics. The poller's hot path
// classifies untrusted JSON from GitHub — a malformed payload that
// trips a nil-deref or out-of-range slice index would otherwise take
// down the whole multica-backend process (no recover() in
// cmd/server/main.go's goroutines). The supervisor catches the
// panic, logs the stack with structured fields, increments a panic
// counter that observability can alert on, then sleeps backoff and
// restarts fn.
//
// fn returns when ctx is canceled. On graceful exit (ctx.Err() != nil)
// the supervisor returns without restart. Any other return from fn is
// logged + treated like a panic for restart purposes.
//
// Designed to be wrapped by a single goroutine from cmd/server/main.go:
//
//	go RunWithRecover(ctx, "github_poller", panicCount, slog.Default(), func(ctx context.Context) error {
//	    return poller.Run(ctx)
//	})
//
// panicCounter may be nil — used when production wires Prometheus
// labels or local-only telemetry. The supervisor itself doesn't care.
func RunWithRecover(
	ctx context.Context,
	name string,
	panicCounter *atomic.Int64,
	logger *slog.Logger,
	backoff time.Duration,
	fn func(context.Context) error,
) {
	if logger == nil {
		logger = slog.Default()
	}
	if backoff <= 0 {
		backoff = 5 * time.Second
	}
	for ctx.Err() == nil {
		runOnce(ctx, name, panicCounter, logger, fn)
		// Graceful shutdown: if the run returned because ctx was
		// canceled, exit without sleeping.
		if ctx.Err() != nil {
			return
		}
		// Backoff before restart. Bounded by ctx.Done so a slow
		// shutdown still ends in <=backoff.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// runOnce executes fn under defer-recover. Pulled out so the panic
// handler runs at function-scope, not loop-scope (a panic during
// the loop's iteration check would otherwise crash unrecovered).
func runOnce(
	ctx context.Context,
	name string,
	panicCounter *atomic.Int64,
	logger *slog.Logger,
	fn func(context.Context) error,
) {
	defer func() {
		if r := recover(); r != nil {
			if panicCounter != nil {
				panicCounter.Add(1)
			}
			logger.Error("githubpoll.supervisor.panic",
				"name", name,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}
	}()
	if err := fn(ctx); err != nil && ctx.Err() == nil {
		// Non-context-cancellation exit. Log so the operator sees
		// why the supervisor is restarting fn. The restart itself
		// happens up in the loop after the backoff.
		logger.Error("githubpoll.supervisor.exit",
			"name", name,
			"error", err,
		)
	}
}
