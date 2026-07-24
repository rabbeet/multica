package telegramoutbound

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// TestLimiter_PerChatBucket_BlocksSecondCallInSameChat: with per-chat
// budget of 1 token per 50ms, back-to-back calls in the same chat MUST
// take ≥50ms wall-clock; a second chat MUST proceed immediately.
func TestLimiter_PerChatBucket_BlocksSecondCallInSameChat(t *testing.T) {
	l := newLimiterWithRates(rate.Inf, 100, rate.Every(50*time.Millisecond), 1)
	ctx := context.Background()

	// First call consumes the initial burst — should be instant.
	start := time.Now()
	if err := l.Wait(ctx, 42); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("first call should be instant, took %v", d)
	}

	// Second call in the SAME chat must wait ≥50ms.
	start = time.Now()
	if err := l.Wait(ctx, 42); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("second call in same chat should wait ~50ms, took %v", d)
	}

	// A call in a DIFFERENT chat should not be blocked by the
	// per-chat backlog on chat 42.
	start = time.Now()
	if err := l.Wait(ctx, 99); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if d := time.Since(start); d > 20*time.Millisecond {
		t.Errorf("different chat should not be blocked by chat 42, took %v", d)
	}
}

// TestLimiter_Global_BlocksAcrossChats: global budget is a hard cap
// on aggregate throughput. Set per-chat to Inf so it never blocks;
// global to 1/20ms means 3 sends take ≥40ms in aggregate.
func TestLimiter_Global_BlocksAcrossChats(t *testing.T) {
	l := newLimiterWithRates(rate.Every(20*time.Millisecond), 1, rate.Inf, 100)
	ctx := context.Background()
	start := time.Now()
	for _, chat := range []int64{1, 2, 3} {
		if err := l.Wait(ctx, chat); err != nil {
			t.Fatalf("Wait chat=%d: %v", chat, err)
		}
	}
	if d := time.Since(start); d < 30*time.Millisecond {
		t.Errorf("global should cap aggregate; 3 calls took %v (want ≥30ms)", d)
	}
}

// TestLimiter_ContextCancel: a Wait that would sleep must return
// ctx.Err() when the deadline expires. Otherwise the scheduler
// cannot be cleanly stopped on shutdown.
func TestLimiter_ContextCancel(t *testing.T) {
	l := newLimiterWithRates(rate.Every(time.Second), 1, rate.Inf, 1)
	ctx := context.Background()
	// Burn the initial global token.
	if err := l.Wait(ctx, 1); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	// Now this Wait wants to sleep ~1s; cancel after 30ms.
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := l.Wait(cctx, 1)
	if err == nil {
		t.Fatalf("expected ctx-related error, got nil")
	}
}

// TestLimiter_ConcurrentSafety: parallel Wait on different chats
// should not race the perChat map. Any race here would show up in
// `go test -race`.
func TestLimiter_ConcurrentSafety(t *testing.T) {
	l := NewLimiter()
	var wg sync.WaitGroup
	for i := int64(0); i < 20; i++ {
		wg.Add(1)
		go func(chat int64) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = l.Wait(ctx, chat)
		}(i)
	}
	wg.Wait()
}
