// PUL-479 outbound Telegram bridge wiring.
//
// Reads env, constructs Client + Scheduler + CommentEnqueuer, wires
// them into the shared *service.CommentService, and starts the
// scheduler goroutine. Fully gated by MULTICA_TELEGRAM_ENABLED —
// hosts that haven't opted in incur zero runtime cost (no
// goroutines, no outbox INSERTs from CommentService).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/telegramoutbound"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// wireTelegramOutbound reads env, constructs the outbound bridge if
// enabled, plugs the enqueuer into CommentService, and returns the
// scheduler (or nil when the feature flag is off).
//
// Errors are logged (not returned) — a misconfigured Telegram bridge
// must not prevent the backend from booting; operators fix envvars
// and restart. Same posture as runGithubPoller.
func wireTelegramOutbound(ctx context.Context, queries *db.Queries, commentSvc *service.CommentService) *telegramoutbound.Scheduler {
	env, err := telegramoutbound.FromEnv(os.Getenv)
	if err != nil {
		slog.Error("telegramoutbound: bad configuration; bridge disabled", "error", err)
		return nil
	}
	env.LogSummary(slog.Default())
	if !env.Enabled {
		return nil
	}

	client, err := telegramoutbound.NewClient(env.APIBaseURL, env.BotToken, nil)
	if err != nil {
		slog.Error("telegramoutbound: NewClient failed; bridge disabled", "error", err)
		return nil
	}

	sched, err := telegramoutbound.NewScheduler(telegramoutbound.Config{
		Queries: queries,
		Client:  client,
		Limiter: telegramoutbound.NewLimiter(),
		ChatID:  env.ChatID,
		// v1: alerts go to slog only. A follow-up will wire this to
		// notify.Bridge for cascade-style operator paging (see
		// PUL-479 plan §Failure handling — "critical gap" note).
		Alert: nil,
	}, slog.Default())
	if err != nil {
		slog.Error("telegramoutbound: NewScheduler failed; bridge disabled", "error", err)
		return nil
	}

	// Plug the CommentService seam. v1 leaves AuthorLabelFn nil, so
	// the header line uses the raw author_type ("member", "agent").
	// PR2 will introduce a resolver that swaps this for the actual
	// user display name.
	commentSvc.SetTelegramOutbox(telegramoutbound.NewCommentEnqueuer(nil))

	return sched
}

// runTelegramOutboundScheduler is the goroutine entrypoint spawned
// alongside runChildProgressScheduler / runReminderScheduler /
// runGithubPoller. Returns immediately when sched is nil.
func runTelegramOutboundScheduler(ctx context.Context, sched *telegramoutbound.Scheduler) {
	if sched == nil {
		return
	}
	sched.Run(ctx)
}
