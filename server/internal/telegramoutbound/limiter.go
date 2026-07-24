package telegramoutbound

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter is a two-tier token bucket implementing the Bot API limits:
//
//   - global: ~30 messages/sec across the bot. burst=30 lets a short
//     scheduler tick catch up after being paused (e.g. after DB
//     hiccup); the average enforcement is 1050ms between messages
//     divided across the burst → ≈28.5 mps under sustained load,
//     comfortably below the 30 mps ceiling.
//   - per-chat: ~1 message/sec/group. Bot API's group rate-limit
//     is empirically ~1s between messages; we allow the burst=1 so
//     a lone message goes through instantly, then wait.
//
// Both tiers use time/rate.Wait, which respects ctx cancellation and
// returns immediately when a token is available — meaning the scheduler
// tick does not sleep when the bridge is idle.
type Limiter struct {
	global      *rate.Limiter
	perChatMu   sync.Mutex
	perChat     map[int64]*rate.Limiter
	perChatRate rate.Limit
	perChatBurst int
}

// NewLimiter constructs a Limiter with the production defaults.
// Callers pass overrides only from tests (a much higher rate makes
// the limiter-tests fast without simulating a real second-scale wait).
func NewLimiter() *Limiter {
	return newLimiterWithRates(rate.Every(35*time.Millisecond), 30, rate.Every(1050*time.Millisecond), 1)
}

func newLimiterWithRates(globalRate rate.Limit, globalBurst int, perChatRate rate.Limit, perChatBurst int) *Limiter {
	return &Limiter{
		global:       rate.NewLimiter(globalRate, globalBurst),
		perChat:      make(map[int64]*rate.Limiter),
		perChatRate:  perChatRate,
		perChatBurst: perChatBurst,
	}
}

// Wait blocks until both the global and the per-chat bucket have a
// token available, or ctx expires. On success returns nil; on ctx
// cancellation returns ctx.Err().
func (l *Limiter) Wait(ctx context.Context, chatID int64) error {
	if err := l.global.Wait(ctx); err != nil {
		return err
	}
	return l.forChat(chatID).Wait(ctx)
}

func (l *Limiter) forChat(chatID int64) *rate.Limiter {
	l.perChatMu.Lock()
	defer l.perChatMu.Unlock()
	if lim, ok := l.perChat[chatID]; ok {
		return lim
	}
	lim := rate.NewLimiter(l.perChatRate, l.perChatBurst)
	l.perChat[chatID] = lim
	return lim
}
