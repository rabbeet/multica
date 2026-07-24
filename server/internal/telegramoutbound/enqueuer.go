package telegramoutbound

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CommentEnqueuer is the concrete TelegramOutboxEnqueuer used by
// service.CommentService in production. It writes one 'comment' row
// into telegram_outbox using the caller-supplied qtx, which must be
// the *db.Queries bound to the caller's open transaction — so the
// outbox INSERT commits atomically with the comment INSERT.
//
// Constructing CommentEnqueuer with authorLabelFn=nil defaults to a
// bare "author_type" label ("member" / "agent" / "system"). Callers
// that want a friendlier display name (e.g. "Vadim" instead of
// "member") pass a resolver that looks up the user/agent by id.
type CommentEnqueuer struct {
	authorLabelFn AuthorLabelFn
}

// AuthorLabelFn resolves (author_type, issue_id) into a human
// display label. Called synchronously from CommentService.Create
// inside the tx — must be fast (<10ms). Returning "" means "use the
// author_type as-is". issueID rather than authorID is passed because
// v1 attributes single-user mode to a fixed default author per
// workspace, so the label lookup uses issue → workspace → default.
type AuthorLabelFn func(ctx context.Context, authorType string, issueID pgtype.UUID) string

// NewCommentEnqueuer constructs the enqueuer. labelFn may be nil.
func NewCommentEnqueuer(labelFn AuthorLabelFn) *CommentEnqueuer {
	return &CommentEnqueuer{authorLabelFn: labelFn}
}

// EnqueueComment implements service.TelegramOutboxEnqueuer. Fields
// the scheduler needs later are persisted in the payload; author
// display resolution happens here (once, at enqueue time) so the
// scheduler does not need to re-load users when it drains. The
// authorTypeSeed parameter carries the raw author_type ("member",
// "agent", "system") from the CommentService caller — the label
// resolver may replace it with a friendly display name using the
// enqueuer's AuthorLabelFn (v1 leaves the resolver nil, which keeps
// the raw seed in the payload).
func (e *CommentEnqueuer) EnqueueComment(
	ctx context.Context, qtx *db.Queries,
	issueID, commentID pgtype.UUID,
	identifier, authorTypeSeed, content string,
) error {
	label := authorTypeSeed
	if e.authorLabelFn != nil {
		if l := e.authorLabelFn(ctx, authorTypeSeed, issueID); l != "" {
			label = l
		}
	}
	payload, err := json.Marshal(outboxPayload{
		Content:     content,
		AuthorLabel: label,
		Identifier:  identifier,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	_, err = qtx.InsertTelegramOutboxRow(ctx, db.InsertTelegramOutboxRowParams{
		Kind:      "comment",
		IssueID:   issueID,
		CommentID: commentID,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("insert outbox row: %w", err)
	}
	return nil
}
