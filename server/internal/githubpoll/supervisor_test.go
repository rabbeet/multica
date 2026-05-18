package githubpoll

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWithRecover_HandlesPanic(t *testing.T) {
	// First call panics, second call returns ctx.Err() — supervisor
	// catches the panic, restarts after backoff, then exits on
	// ctx cancellation.
	var calls int32
	var panicsSeen atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunWithRecover(ctx, "test", &panicsSeen, nil, 10*time.Millisecond, func(ctx context.Context) error {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				panic("simulated classifier nil-deref")
			}
			cancel()
			return ctx.Err()
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not return after panic + recovery")
	}

	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("fn calls = %d, want at least 2 (panic + retry)", got)
	}
	if panicsSeen.Load() != 1 {
		t.Errorf("panic counter = %d, want 1", panicsSeen.Load())
	}
}

func TestRunWithRecover_ExitsOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunWithRecover(ctx, "test", nil, nil, 10*time.Millisecond, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not exit when ctx was canceled")
	}
}

func TestRunWithRecover_RestartsOnError(t *testing.T) {
	// fn returns a non-nil error every call until cancel. Supervisor
	// restarts each time. Asserts via a channel signaled inside fn
	// (deterministic) rather than a polling-loop on the counter
	// (flake-prone under CI load).
	var calls atomic.Int64
	reached := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunWithRecover(ctx, "test", nil, nil, time.Millisecond, func(ctx context.Context) error {
			if calls.Add(1) == 3 {
				close(reached)
				cancel()
			}
			return errors.New("transient")
		})
	}()
	select {
	case <-reached:
		// fn reached its third invocation — restart loop is alive.
	case <-time.After(2 * time.Second):
		t.Fatalf("supervisor did not restart fn enough times: calls=%d", calls.Load())
	}
	// Also confirm graceful shutdown.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not exit after cancel")
	}
}

func TestRunWithRecover_DefaultsForNilArgs(t *testing.T) {
	// nil logger + nil counter + zero backoff should still work.
	// Smoke-only: prove the call shape is safe.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		RunWithRecover(ctx, "smoke", nil, nil, 0, func(ctx context.Context) error {
			cancel()
			return ctx.Err()
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor with nil args did not exit")
	}
}
