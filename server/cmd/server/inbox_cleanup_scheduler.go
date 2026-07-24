// PUL-445 TTL cleanup for read inbox_item rows.
//
// The inbox handler ships paginated (limit 50 default) — but if the
// tail keeps growing indefinitely, each user's absolute inbox row
// count still climbs. Workspace f00bf003 grew to 2553 non-archived
// items over 2 months, of which 2022 were already read; the read
// ones contribute nothing to the "what needs my attention" workflow
// and only inflate the total-rows-scanned during the WS invalidate
// pass. This scheduler archives them once their `read=true` state
// has aged past the configurable TTL (default 14 days).
//
// Mirrors the shape of skill_state_cleanup_scheduler.go (PUL-182):
// one immediate sweep at startup, then a fixed ticker. The
// underlying UPDATE is chunked (LIMIT 5000 in the SQL) so a fresh
// service on a backlogged table still finishes each tick in short
// bounded work. On a healthy steady-state system there is nothing
// to archive and the log stays quiet.
//
// Failure handling: transient PG errors log at Warn and the tick
// is abandoned; the next ticker fires 1h later. The work is
// naturally idempotent — the sweep only touches rows still meeting
// the predicate at execution time.
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboxCleanupTickInterval is the cadence for the retention sweep.
// One hour is coarse enough that the log stays silent on a healthy
// server (nothing to archive most ticks) and fine enough that the
// worst-case retention overshoot on top of the TTL itself is
// bounded to +1h.
const inboxCleanupTickInterval = time.Hour

// inboxCleanupMinTTL floors the configured TTL. envDuration will
// accept "500ms" or "0s", and int64(ttl.Seconds()) then truncates
// to 0 — which would match "created_at < now() - 0 seconds", i.e.
// archive every read row on every tick. That is a retention
// amplifier, not a cleaner. One minute is the smallest TTL where
// the SQL math still produces meaningful separation between rows.
const inboxCleanupMinTTL = time.Minute

// runInboxCleanupScheduler starts the periodic retention sweep for
// inbox_item.archived=false AND read=true rows older than ttl.
// Shares the shutdown context with the other schedulers wired in
// main.go so SIGTERM stops it during graceful drain.
//
// Runs one sweep synchronously at startup so the first tick is not
// 1h after boot — if the server just restarted from a crash, any
// backlogged rows are picked up immediately.
func runInboxCleanupScheduler(ctx context.Context, queries *db.Queries, ttl time.Duration) {
	if ttl < inboxCleanupMinTTL {
		slog.Warn("inbox cleanup: TTL below 1m, clamping to default",
			"requested_ttl", ttl.String(),
			"applied_ttl", (14 * 24 * time.Hour).String())
		ttl = 14 * 24 * time.Hour
	}

	runInboxCleanupTick(ctx, queries, ttl)

	ticker := time.NewTicker(inboxCleanupTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runInboxCleanupTick(ctx, queries, ttl)
		}
	}
}

// runInboxCleanupTick runs one sweep. Logs at Info only when at
// least one row was archived; healthy idle servers stay silent at
// the default log level.
//
// A cancelled context returns silently — context.Canceled during a
// graceful shutdown is expected, not a failure mode worth Warn-log.
func runInboxCleanupTick(ctx context.Context, queries *db.Queries, ttl time.Duration) {
	affected, err := queries.ArchiveOldReadInbox(ctx, int64(ttl.Seconds()))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		slog.Warn("inbox cleanup: failed", "error", err, "ttl", ttl.String())
		return
	}
	if affected == 0 {
		return
	}
	slog.Info("inbox cleanup: archived old read items",
		"archived", affected,
		"ttl", ttl.String())
}
