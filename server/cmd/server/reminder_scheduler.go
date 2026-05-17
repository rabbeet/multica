// PUL-154 «Wake up in N»: background worker that fires due reminders.
//
// Pattern is the same as autopilot_scheduler.go (60s tick + recovery on
// startup), with two differences:
//   - Two-phase claim via the issue_reminder.firing_at column instead of
//     advancing next_run_at. This lets the FOR UPDATE SKIP LOCKED contract
//     hand each due row to exactly one worker without resetting state on
//     completion.
//   - A 5-minute prune-pass that pre-cancels reminders whose issue has seen
//     activity since the reminder was created. Running the prune both
//     standalone (every 5 min) and inline at the top of each fire tick keeps
//     the claim query trivial.
//
// Comment creation + status flip + history audit go through the shared
// service.CommentService.Create (PR-0) so the same atomic guarantees and
// audit invariants apply as for HTTP-authored comments.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	reminderTickInterval  = 60 * time.Second
	reminderPruneInterval = 5 * time.Minute
)

// runReminderScheduler is the entry point spawned alongside the autopilot
// scheduler at server start. It owns three loops on shared tickers: fire
// (every 60s), prune (every 5min), and one-time recovery on startup.
func runReminderScheduler(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService) {
	// Startup recovery: clear any firing_at left over from a crash. Safe to
	// run unconditionally — newly-claimed reminders will have firing_at older
	// than 5 minutes only if the worker crashed mid-fire.
	if rows, err := queries.ResetStuckClaims(ctx); err != nil {
		slog.Warn("reminder scheduler: startup reset failed", "error", err)
	} else if rows > 0 {
		slog.Info("reminder scheduler: startup reset", "claims_released", rows)
	}

	fireTicker := time.NewTicker(reminderTickInterval)
	pruneTicker := time.NewTicker(reminderPruneInterval)
	defer fireTicker.Stop()
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pruneTicker.C:
			runPruneAndReset(ctx, queries)
		case <-fireTicker.C:
			runFireTick(ctx, queries, bus, commentSvc)
		}
	}
}

// runPruneAndReset is the periodic non-fire housekeeping: cancel reminders
// whose issues saw activity, then release any stuck claims so they retry on
// the next fire tick.
func runPruneAndReset(ctx context.Context, queries *db.Queries) {
	if rows, err := queries.MarkRemindersCancelledByActivity(ctx); err != nil {
		slog.Warn("reminder scheduler: activity-prune failed", "error", err)
	} else if rows > 0 {
		slog.Info("reminder scheduler: activity-prune", "cancelled", rows)
	}
	if rows, err := queries.ResetStuckClaims(ctx); err != nil {
		slog.Warn("reminder scheduler: stuck-claim reset failed", "error", err)
	} else if rows > 0 {
		slog.Info("reminder scheduler: stuck-claim reset", "released", rows)
	}
}

// runFireTick is the per-minute work loop: activity-prune (so we never claim
// a reminder that should have been cancelled), then claim due rows, then
// fire each one. Each step's failure is logged but does not abort the tick;
// transient errors (e.g. PG hiccup on activity-prune) should not freeze the
// fire loop.
func runFireTick(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService) {
	// Inline activity-prune before claiming. Cheap and keeps the claim query
	// from racing with a fresh comment that arrived since the 5-min prune.
	if _, err := queries.MarkRemindersCancelledByActivity(ctx); err != nil {
		slog.Warn("reminder scheduler: inline activity-prune failed", "error", err)
	}

	claimed, err := queries.ClaimDueReminders(ctx)
	if err != nil {
		slog.Warn("reminder scheduler: claim due failed", "error", err)
		return
	}
	if len(claimed) == 0 {
		return
	}
	slog.Info("reminder scheduler: claimed", "count", len(claimed))

	for _, r := range claimed {
		if err := fireOne(ctx, queries, bus, commentSvc, r); err != nil {
			slog.Warn("reminder scheduler: fire failed",
				"reminder_id", util.UUIDToString(r.ID),
				"issue_id", util.UUIDToString(r.IssueID),
				"error", err)
			// Release the claim so the next tick retries.
			if uErr := queries.UnclaimReminder(ctx, r.ID); uErr != nil {
				slog.Warn("reminder scheduler: unclaim after error failed",
					"reminder_id", util.UUIDToString(r.ID), "error", uErr)
			}
		}
	}
}

// fireOne does the per-reminder work: verify the creator still exists,
// build the comment body, commit via CommentService.Create with an
// InjectStatusTransition for the optional waiting/backlog → todo flip,
// then mark the reminder fired and publish the realtime event.
func fireOne(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService, r db.IssueReminder) error {
	issue, err := queries.GetIssue(ctx, r.IssueID)
	if err != nil {
		// Should not happen — ON DELETE CASCADE removes reminders with the
		// issue. Treat as transient (release claim, retry).
		return fmt.Errorf("load issue: %w", err)
	}

	if !creatorAlive(ctx, queries, r.CreatedByType, r.CreatedByID) {
		// Creator was removed between scheduling and firing. Cancel the
		// reminder so it never resurrects.
		if cErr := queries.CancelReminderForGoneCreator(ctx, r.ID); cErr != nil {
			return fmt.Errorf("cancel for gone creator: %w", cErr)
		}
		slog.Info("reminder scheduler: creator gone, cancelled",
			"reminder_id", util.UUIDToString(r.ID))
		publishCancelled(bus, issue.WorkspaceID, r, "creator_gone")
		return nil
	}

	content := buildContent(r, issue)

	// Only inject the transition if the issue is actually in waiting or
	// backlog; OnlyIfStatusIn is the StatusTransition guard.
	transition := &service.StatusTransition{
		ToStatus:       "todo",
		Source:         service.SourceHookReminder,
		ActorType:      "system",
		ActorID:        pgtype.UUID{}, // null actor for system
		RefID:          util.UUIDToString(r.ID),
		OnlyIfStatusIn: []string{"waiting", "backlog"},
	}

	result, err := commentSvc.Create(ctx, service.CommentCreateParams{
		IssueID:          r.IssueID,
		WorkspaceID:      r.WorkspaceID,
		AuthorType:       r.CreatedByType,
		AuthorID:         r.CreatedByID,
		Content:          content,
		Type:             service.CommentTypeWakeUp,
		SkipDecideFlip:   true, // type='wake_up' would be filtered out anyway, but explicit is better
		StatusTransition: transition,
	})
	if err != nil {
		return fmt.Errorf("create wake_up comment: %w", err)
	}

	fired, err := queries.MarkReminderFired(ctx, db.MarkReminderFiredParams{
		ID:             r.ID,
		FiredCommentID: pgtype.UUID{Bytes: result.Comment.ID.Bytes, Valid: true},
	})
	if err != nil {
		// pgx.ErrNoRows means another worker already finalized this row
		// (unlikely under SKIP LOCKED but possible during a recovery race).
		// Treat as success — the comment already exists.
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info("reminder scheduler: already finalized",
				"reminder_id", util.UUIDToString(r.ID))
			return nil
		}
		return fmt.Errorf("mark fired: %w", err)
	}

	publishFired(bus, issue.WorkspaceID, fired, result)
	return nil
}

// creatorAlive checks whether the member or agent who scheduled the
// reminder still exists. A deleted creator means the comment cannot be
// authored under a real identity, so the reminder is cancelled instead.
func creatorAlive(ctx context.Context, queries *db.Queries, createdByType string, createdByID pgtype.UUID) bool {
	switch createdByType {
	case "member":
		_, err := queries.GetMember(ctx, createdByID)
		return err == nil
	case "agent":
		_, err := queries.GetAgent(ctx, createdByID)
		return err == nil
	default:
		return false
	}
}

// buildContent assembles the wake_up comment body per plan v2:
//   - prefix '⏰ '
//   - either the user-provided note or the canonical 'Wake-up reminder'
//   - suffix '(was: <prev_status>)' when the issue was waiting or backlog,
//     otherwise no suffix
//
// The prev_status suffix is computed from the issue snapshot loaded above
// (pre-transition). The status flip lands transactionally with this
// comment, so the suffix and the visible status change are consistent.
func buildContent(r db.IssueReminder, issue db.Issue) string {
	body := "Wake-up reminder"
	if r.Note.Valid && r.Note.String != "" {
		body = r.Note.String
	}
	content := "⏰ " + body
	if issue.Status == "waiting" || issue.Status == "backlog" {
		content += " (was: " + issue.Status + ")"
	}
	return content
}

func publishFired(bus *events.Bus, workspaceID pgtype.UUID, r db.IssueReminder, result service.CommentCreateResult) {
	var transition map[string]string
	if result.AppliedTransition != nil {
		transition = map[string]string{
			"from": result.AppliedTransition.From,
			"to":   result.AppliedTransition.To,
		}
	}
	bus.Publish(events.Event{
		Type:        protocol.EventReminderFired,
		WorkspaceID: util.UUIDToString(workspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"reminder_id":       util.UUIDToString(r.ID),
			"issue_id":          util.UUIDToString(r.IssueID),
			"fired_comment_id":  util.UUIDToString(r.FiredCommentID),
			"status_transition": transition,
		},
	})
}

func publishCancelled(bus *events.Bus, workspaceID pgtype.UUID, r db.IssueReminder, reason string) {
	bus.Publish(events.Event{
		Type:        protocol.EventReminderCancelled,
		WorkspaceID: util.UUIDToString(workspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"reminder_id":   util.UUIDToString(r.ID),
			"issue_id":      util.UUIDToString(r.IssueID),
			"cancel_reason": reason,
		},
	})
}
