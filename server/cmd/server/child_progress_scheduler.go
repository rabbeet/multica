// PUL-164 child-progress fan-out worker.
//
// Drains issue_child_progress_outbox every 5 seconds. For each pending row,
// posts a system-authored comment of type='child_progress' on every ancestor
// in the row's frozen ancestor_chain. Comments are inserted via
// service.CommentService.Create, so the same audit/WS pipeline that fires
// for HTTP-authored comments also fires for fan-out comments (the only
// difference is the suppression flag — fan-out comments must never trigger
// the assigned agent, see handler.shouldEnqueueOnComment).
//
// Failure handling:
//   - Idempotency violation (ErrCommentAlreadyExists) → treat as success.
//     Repeated fires on the same source_history_id land in the same comment
//     row via the (source_history_id, issue_id) WHERE type='child_progress'
//     unique partial index added in migration 078.
//   - Any other CommentService.Create error → bump retry_count, release the
//     claim. The next tick retries until retry_count exceeds 10, at which
//     point the row is parked as 'failed' for operator investigation.
//   - Worker crash mid-tick → ResetStuckChildProgressOutboxClaims releases
//     claims older than 60s on each tick (and once on startup).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	childProgressTickInterval = 5 * time.Second
	childProgressMaxRetries   = 10
)

// systemBotAuthorID is the placeholder author_id used for system-authored
// fan-out comments. NULL UUID is acceptable in the comment table; the
// frontend keys off comment.author_type='system' to render without an
// avatar. If a future migration introduces a real system bot member with
// a fixed UUID, swap this for that UUID — no other call sites need to know.
var systemBotAuthorID = pgtype.UUID{}

// runChildProgressScheduler is the entry point spawned alongside the other
// schedulers at server start. Owns three responsibilities on a shared 5s
// ticker: (1) clear stuck claims from prior crashes, (2) claim due pending
// rows, (3) process each claim by posting comments on its ancestor chain.
func runChildProgressScheduler(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService) {
	// Startup recovery — releases any claims still in 'processing' from a
	// previous crash. Safe to run unconditionally.
	if rows, err := queries.ResetStuckChildProgressOutboxClaims(ctx); err != nil {
		slog.Warn("child_progress scheduler: startup reset failed", "error", err)
	} else if rows > 0 {
		slog.Info("child_progress scheduler: startup reset", "claims_released", rows)
	}

	ticker := time.NewTicker(childProgressTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runChildProgressTick(ctx, queries, bus, commentSvc)
		}
	}
}

// runChildProgressTick: one cycle of stuck-claim recovery + claim + process.
// Each step's failure is logged but does not abort the tick; a transient PG
// hiccup must not freeze the worker permanently.
func runChildProgressTick(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService) {
	if _, err := queries.ResetStuckChildProgressOutboxClaims(ctx); err != nil {
		slog.Warn("child_progress scheduler: stuck-claim reset failed", "error", err)
	}

	claimed, err := queries.ClaimDueChildProgressOutbox(ctx)
	if err != nil {
		slog.Warn("child_progress scheduler: claim failed", "error", err)
		return
	}
	if len(claimed) == 0 {
		return
	}
	slog.Info("child_progress scheduler: claimed", "count", len(claimed))

	for _, row := range claimed {
		if err := processOne(ctx, queries, bus, commentSvc, row); err != nil {
			// Record the error on the row and let it retry, unless we have
			// blown the retry budget.
			outboxID := util.UUIDToString(row.ID)
			errMsg := pgtype.Text{String: err.Error(), Valid: true}
			if int(row.RetryCount)+1 >= childProgressMaxRetries {
				if _, mErr := queries.MarkChildProgressOutboxFailed(ctx, db.MarkChildProgressOutboxFailedParams{
					ID:        row.ID,
					LastError: errMsg,
				}); mErr != nil {
					slog.Warn("child_progress scheduler: mark failed errored",
						"outbox_id", outboxID, "error", mErr)
				}
				slog.Warn("child_progress scheduler: row exceeded retry budget",
					"outbox_id", outboxID, "last_error", err.Error())
			} else {
				if _, bErr := queries.BumpChildProgressOutboxRetry(ctx, db.BumpChildProgressOutboxRetryParams{
					LastError: errMsg,
					ID:        row.ID,
				}); bErr != nil {
					slog.Warn("child_progress scheduler: bump retry errored",
						"outbox_id", outboxID, "error", bErr)
				}
			}
		}
	}
}

// processOne posts a child_progress comment on every ancestor in the row's
// ancestor_chain, then marks the outbox row processed. On any error during
// ancestor INSERT, returns early so the caller bumps retry_count.
//
// Comment author_type is 'system' with a NULL author_id. Frontend
// distinguishes system-authored fan-out comments from real members/agents
// by switching on comment.type, not author_id.
func processOne(ctx context.Context, queries *db.Queries, bus *events.Bus, commentSvc *service.CommentService, row db.IssueChildProgressOutbox) error {
	child, err := queries.GetIssue(ctx, row.ChildIssueID)
	if err != nil {
		return fmt.Errorf("load child issue: %w", err)
	}

	// Build the identifier 'PUL-162'-style. Best-effort: if workspace lookup
	// fails, just use the number with no prefix in the meta.
	identifier := childIdentifier(ctx, queries, child)

	body := service.FormatFallbackBody(identifier, child.Title, row.NewStatus)
	meta := service.ChildProgressMeta{
		ChildIssueID:    util.UUIDToString(row.ChildIssueID),
		ChildIdentifier: identifier,
		ChildTitle:      child.Title,
		PrevStatus:      row.PrevStatus,
		NewStatus:       row.NewStatus,
		ActorID:         util.UUIDToString(row.ActorID),
		ActorType:       row.ActorType.String,
		SourceHistoryID: row.SourceHistoryID,
	}
	metaJSON, err := service.MarshalChildProgressMeta(meta)
	if err != nil {
		return fmt.Errorf("marshal child_progress meta: %w", err)
	}

	for _, ancestorID := range row.AncestorChain {
		ancestor, gErr := queries.GetIssue(ctx, ancestorID)
		if gErr != nil {
			return fmt.Errorf("load ancestor %s: %w", util.UUIDToString(ancestorID), gErr)
		}

		result, cErr := commentSvc.Create(ctx, service.CommentCreateParams{
			IssueID:         ancestorID,
			WorkspaceID:     ancestor.WorkspaceID,
			AuthorType:      "system",
			AuthorID:        systemBotAuthorID,
			Content:         body,
			Type:            service.CommentTypeChildProgress,
			Meta:            metaJSON,
			SourceHistoryID: pgtype.Int8{Int64: row.SourceHistoryID, Valid: true},
			SkipDecideFlip:  true, // type='child_progress' would be filtered anyway, but explicit is better
		})
		switch {
		case cErr == nil:
			publishChildProgress(bus, ancestor.WorkspaceID, result, row)
		case errors.Is(cErr, service.ErrCommentAlreadyExists):
			// Duplicate fire (retry / replay). Already-existing comment is
			// fine — no event publish (it would be a duplicate).
			slog.Debug("child_progress scheduler: ancestor already has fan-out comment",
				"outbox_id", util.UUIDToString(row.ID),
				"ancestor_id", util.UUIDToString(ancestorID))
		default:
			return fmt.Errorf("create child_progress comment on ancestor %s: %w",
				util.UUIDToString(ancestorID), cErr)
		}
	}

	if _, err := queries.MarkChildProgressOutboxProcessed(ctx, row.ID); err != nil {
		// At this point all comments are inserted; failing to mark processed
		// means we will re-process on the next tick. ErrCommentAlreadyExists
		// will short-circuit each ancestor cleanly, so this is safe — just
		// noisy.
		return fmt.Errorf("mark processed: %w", err)
	}
	return nil
}

// childIdentifier formats the canonical 'PUL-N' style identifier for a
// child issue. Falls back to the bare number when the workspace prefix
// cannot be loaded — the meta is best-effort.
func childIdentifier(ctx context.Context, queries *db.Queries, issue db.Issue) string {
	ws, err := queries.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil || ws.IssuePrefix == "" {
		return fmt.Sprintf("#%d", issue.Number)
	}
	return fmt.Sprintf("%s-%d", ws.IssuePrefix, issue.Number)
}

// publishChildProgress emits two realtime events for a freshly-inserted
// fan-out comment:
//
//  1. comment:created — same shape as the event the HTTP comment-create
//     handler emits, so the frontend's existing handler invalidates the
//     issue's comment list and the fan-out card appears without any new
//     client code. The "comment" object mirrors handler.CommentResponse;
//     meta is exposed as the raw JSONB so clients can deserialise it as a
//     structured ChildProgressMeta (matches the HTTP path's shape).
//  2. child_progress — a more specific event for clients that want to
//     filter on child-driven activity (e.g. an aggregate "child activity"
//     digest view). Optional; no current consumer.
func publishChildProgress(bus *events.Bus, workspaceID pgtype.UUID, result service.CommentCreateResult, row db.IssueChildProgressOutbox) {
	wsID := util.UUIDToString(workspaceID)

	// Build the comment payload to match handler.commentToResponse exactly:
	// same fields, same shapes, same JSON encoding (meta as raw bytes,
	// parent_id as nullable *string, etc.). This keeps the frontend
	// handler off two divergent shapes.
	var metaPayload any
	if len(result.Comment.Meta) > 0 && string(result.Comment.Meta) != "{}" {
		// Pass the raw JSON; the bus marshals the envelope and embedded
		// json.RawMessage values are emitted verbatim.
		metaPayload = json.RawMessage(result.Comment.Meta)
	}
	var sourceHistoryID *int64
	if result.Comment.SourceHistoryID.Valid {
		v := result.Comment.SourceHistoryID.Int64
		sourceHistoryID = &v
	}
	commentPayload := map[string]any{
		"id":                util.UUIDToString(result.Comment.ID),
		"issue_id":          util.UUIDToString(result.Comment.IssueID),
		"author_type":       result.Comment.AuthorType,
		"author_id":         util.UUIDToString(result.Comment.AuthorID),
		"content":           result.Comment.Content,
		"type":              result.Comment.Type,
		"parent_id":         nilIfInvalidUUID(result.Comment.ParentID),
		"created_at":        result.Comment.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		"updated_at":        result.Comment.UpdatedAt.Time.UTC().Format(time.RFC3339Nano),
		"reactions":         []any{},
		"attachments":       []any{},
		"meta":              metaPayload,
		"source_history_id": sourceHistoryID,
	}

	// (1) comment:created — same shape as HTTP-authored comments.
	bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: wsID,
		ActorType:   "system",
		Payload: map[string]any{
			"comment":             commentPayload,
			"issue_title":         "",
			"issue_assignee_type": nil,
			"issue_assignee_id":   nil,
			"issue_status":        result.UpdatedIssue.Status,
		},
	})

	// (2) child_progress — more specific signal for filtering consumers.
	bus.Publish(events.Event{
		Type:        protocol.EventChildProgress,
		WorkspaceID: wsID,
		ActorType:   "system",
		Payload: map[string]any{
			"comment_id":        util.UUIDToString(result.Comment.ID),
			"ancestor_issue_id": util.UUIDToString(result.Comment.IssueID),
			"child_issue_id":    util.UUIDToString(row.ChildIssueID),
			"source_history_id": row.SourceHistoryID,
			"prev_status":       row.PrevStatus,
			"new_status":        row.NewStatus,
		},
	})
}

// nilIfInvalidUUID returns nil for an unset pgtype.UUID, or its string form.
// Matches handler.uuidToPtr — duplicated here because the comment payload
// must look exactly like CommentResponse (parent_id is JSON null when unset,
// not the empty string).
func nilIfInvalidUUID(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := util.UUIDToString(u)
	return &s
}
