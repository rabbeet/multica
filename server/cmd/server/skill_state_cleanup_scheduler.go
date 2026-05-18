// PUL-182 TTL cleanup worker for issue_skill_state.
//
// PUL-177 shipped the `issue_skill_state` table with a two-state machine
// (in_progress → done). A skill that started but never reported done —
// claude-code crashed, rate-limit hit, agent worktree force-killed,
// user Ctrl-C'd — leaves a phantom "in_progress" chip in the Inbox
// forever. This scheduler is the cron half of the fix: every 15 minutes
// it runs CleanupStaleIssueSkillStates(ttl) against PG and the matching
// rows are gone.
//
// Threshold is configurable via env (ISSUE_SKILL_STATE_STALE_TTL, default
// 24h). Tick interval is a fixed const — 15 minutes is long enough that
// the log line stays quiet on healthy systems and short enough that the
// worst-case phantom-chip lifetime is bounded.
//
// Failure handling: a transient PG error logs at Warn and the tick is
// abandoned. The next ticker fires 15 minutes later. There is no retry
// queue and no claim/lock state — the work is naturally idempotent
// (rerunning the same DELETE 5 minutes later finds nothing to do).
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// skillStateCleanupTickInterval is the cadence for the TTL sweep.
// 15 minutes is the worst-case extra lifetime of a phantom chip on top
// of the TTL itself (24h default). A row created at T=0 that crashed
// without `done` will be visible until at most T = TTL + 15 min.
const skillStateCleanupTickInterval = 15 * time.Minute

// runSkillStateCleanupScheduler starts the periodic TTL cleanup. Shares
// the shutdown context with the other schedulers wired in main.go so
// SIGTERM cleanly stops it during graceful drain.
//
// Runs one cleanup synchronously at startup so the first sweep is not
// 15 minutes after server boot — if the server just restarted from a
// crash, any rows the crash itself orphaned are picked up immediately.
func runSkillStateCleanupScheduler(ctx context.Context, queries *db.Queries, ttl time.Duration) {
	runSkillStateCleanupTick(ctx, queries, ttl)

	ticker := time.NewTicker(skillStateCleanupTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSkillStateCleanupTick(ctx, queries, ttl)
		}
	}
}

// runSkillStateCleanupTick runs one sweep. Logs at Info only when at
// least one row was deleted (so a healthy idle server is silent at the
// default log level) and emits per-row Debug entries so operators
// debugging "why did this chip disappear?" can grep for the slug.
func runSkillStateCleanupTick(ctx context.Context, queries *db.Queries, ttl time.Duration) {
	deleted, err := queries.CleanupStaleIssueSkillStates(ctx, int64(ttl.Seconds()))
	if err != nil {
		slog.Warn("skill_state cleanup: failed", "error", err, "ttl", ttl.String())
		return
	}
	if len(deleted) == 0 {
		return
	}
	slog.Info("skill_state cleanup: removed stale in_progress rows",
		"deleted", len(deleted),
		"ttl", ttl.String())
	for _, row := range deleted {
		slog.Debug("skill_state cleanup row",
			"issue_id", util.UUIDToString(row.IssueID),
			"skill_slug", row.SkillSlug,
			"started_at", row.StartedAt.Time.UTC().Format(time.RFC3339Nano))
	}
}
