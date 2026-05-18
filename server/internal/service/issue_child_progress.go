// Package service — PUL-164 child-progress fan-out.
//
// When an issue with parent_issue_id IS NOT NULL changes status, we want
// every ancestor up to a hard depth limit of 5 to see a system-authored
// comment of type='child_progress' so the parent's stakeholders learn
// "the work I delegated reached <new_status>" without polling each child
// manually.
//
// Architecture (decoupled, at-least-once):
//
//	┌────────────────────────────────────────────────────────────────┐
//	│ status mutation paths                                          │
//	│   - handler.UpdateIssue (HTTP PATCH /api/issues/{id}, status)  │
//	│   - service.CommentService.Create (explicit + DecideFlip)      │
//	│   - service.TaskService failed-task reset                      │
//	│ each writes issue_status_history + calls                       │
//	│ service.EnqueueChildProgressFanout (this file)                 │
//	└──────────────────────────┬─────────────────────────────────────┘
//	                           │ INSERT issue_child_progress_outbox
//	                           ▼
//	┌────────────────────────────────────────────────────────────────┐
//	│ cmd/server/child_progress_scheduler.go (5s tick)               │
//	│   ClaimDueChildProgressOutbox → FOR UPDATE SKIP LOCKED         │
//	│   for each row: CommentService.Create per ancestor             │
//	│       type=child_progress, source_history_id=H, meta={...}     │
//	│       ON CONFLICT (source_history_id, issue_id) DO NOTHING     │
//	│       (surfaced as ErrCommentAlreadyExists; worker treats as ok)│
//	│   MarkChildProgressOutboxProcessed when all ancestors succeed  │
//	└────────────────────────────────────────────────────────────────┘
//
// The user-visible status flip happens in the writer's transaction; the
// fan-out is the worker's job and can retry / be replayed without disturbing
// the original mutation. This decoupling matters: if CommentService.Create
// had a transient hiccup, we do not want the user's "mark deployed" click
// to surface a 500.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CommentTypeChildProgress is comment.type for fan-out comments posted by
// the child_progress scheduler on each ancestor of a child issue that
// changed status. The CHECK constraint for this value is added in
// migration 078_child_progress.up.sql.
const CommentTypeChildProgress = "child_progress"

// MetaKindChildProgress is the comment.meta->>'kind' discriminator. The
// frontend uses it to pick the rich-render component
// (apps/web/components/Comment/ChildProgressComment.tsx).
const MetaKindChildProgress = "child_progress"

// SourceManual is the issue_status_history.source value emitted whenever
// status changes via the HTTP issue-update endpoint. It is already in the
// CHECK whitelist (migration 070); PUL-164 uses it for the InsertStatusHistory
// rows the HTTP handler now writes so child_progress fan-out has a real
// history row to point at via source_history_id.
const SourceManual = "manual"

// noisyTransitions are status pairs that should NOT fan out to parent
// because they are conversational noise from DecideFlip — every member
// comment on a waiting issue flips waiting → in_progress and back. The
// parent's stakeholders do not need to see every flip; they care about
// "moved into work", "in review", "deployed", "blocked", "cancelled".
var noisyTransitions = map[[2]string]bool{
	{"waiting", "in_progress"}: true,
	{"in_progress", "waiting"}: true,
}

// ShouldFanOutTransition reports whether a status change should produce a
// child_progress fan-out. Returns false for identity flips (no-op) and the
// noisy DecideFlip pair. Callers MUST additionally check that the issue
// has a parent before enqueueing — this function only filters noise.
func ShouldFanOutTransition(prevStatus, newStatus string) bool {
	if prevStatus == newStatus {
		return false
	}
	return !noisyTransitions[[2]string{prevStatus, newStatus}]
}

// ChildProgressMeta is the JSON payload stored on comment.meta for
// type='child_progress' rows. The frontend reads this off the comment
// payload to render the fan-out card.
//
// All fields except Kind are best-effort; the frontend MUST tolerate
// missing actor info and a missing child_identifier (a fresh ticket may
// not have a stable prefix yet) by falling back to a plain link.
type ChildProgressMeta struct {
	Kind            string `json:"kind"` // always "child_progress"
	ChildIssueID    string `json:"child_issue_id"`
	ChildIdentifier string `json:"child_identifier,omitempty"`
	ChildTitle      string `json:"child_title"`
	PrevStatus      string `json:"prev_status"`
	NewStatus       string `json:"new_status"`
	ActorID         string `json:"actor_id,omitempty"`
	ActorType       string `json:"actor_type,omitempty"`
	SourceHistoryID int64  `json:"source_history_id"`
}

// MarshalChildProgressMeta serialises a ChildProgressMeta for storage in
// comment.meta. Always sets Kind=MetaKindChildProgress to keep callers
// from forgetting the discriminator.
func MarshalChildProgressMeta(m ChildProgressMeta) ([]byte, error) {
	m.Kind = MetaKindChildProgress
	return json.Marshal(m)
}

// FormatFallbackBody builds the plain-text content stored on
// comment.content for type='child_progress' rows. It is the
// no-JavaScript fallback (REST clients, search indexes, email/Telegram
// bridges when added). The frontend renders a richer card from meta;
// this string only needs to be readable on its own.
func FormatFallbackBody(childIdentifier, childTitle, newStatus string) string {
	if childIdentifier == "" {
		return fmt.Sprintf("🔁 «%s» → %s", childTitle, newStatus)
	}
	return fmt.Sprintf("🔁 [%s] «%s» → %s", childIdentifier, childTitle, newStatus)
}

// EnqueueChildProgressFanout is the central hook called by every status
// mutation path. It does the noise filter + parent check + DB INSERT in
// one shot.
//
// `q` may be either the global *db.Queries or a transactional copy via
// Queries.WithTx — callers that already hold a tx should pass the
// transactional view so the outbox row commits atomically with the
// underlying status change.
//
// Returns (nil, false, nil) for the no-op cases (orphan child, identity
// flip, noisy pair). Returns (row, true, nil) on a successful INSERT. On
// real DB error, returns (nil, false, err).
//
// actor.Valid==false (NULL) is accepted — system-driven flips (task
// scheduler, autopilot) have no human actor.
func EnqueueChildProgressFanout(
	ctx context.Context,
	q *db.Queries,
	params EnqueueChildProgressParams,
) (*db.IssueChildProgressOutbox, bool, error) {
	if !ShouldFanOutTransition(params.PrevStatus, params.NewStatus) {
		return nil, false, nil
	}

	row, err := q.EnqueueChildProgressFanout(ctx, db.EnqueueChildProgressFanoutParams{
		WorkspaceID:     params.WorkspaceID,
		SourceHistoryID: params.SourceHistoryID,
		ChildIssueID:    params.ChildIssueID,
		PrevStatus:      params.PrevStatus,
		NewStatus:       params.NewStatus,
		ActorID:         params.ActorID,
		ActorType:       params.ActorType,
	})
	if err != nil {
		// Orphan child (no parent / no ancestors): the SELECT FROM chain
		// WHERE ids IS NOT NULL clause yields zero rows, INSERT skips, sqlc
		// :one returns ErrNoRows. Treat as a clean no-op.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("enqueue child_progress fanout: %w", err)
	}
	return &row, true, nil
}

// EnqueueChildProgressParams is the input to EnqueueChildProgressFanout.
// Mirrors the underlying sqlc query params with friendlier naming and
// validates implicitly via the SQL CHECK constraints (workspace_id NOT
// NULL, source_history_id NOT NULL, ancestor_chain non-empty + ≤5).
type EnqueueChildProgressParams struct {
	WorkspaceID     pgtype.UUID
	SourceHistoryID int64
	ChildIssueID    pgtype.UUID
	PrevStatus      string
	NewStatus       string
	ActorID         pgtype.UUID
	ActorType       pgtype.Text
}
