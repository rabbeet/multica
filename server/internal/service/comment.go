// Package service — CommentService is the shared comment-creation pipeline
// (transactional INSERT + status-flip + history audit) extracted from
// handler.CreateComment so that both the HTTP handler and non-HTTP callers
// (e.g. PUL-154 reminder scheduler) commit comments through one code path.
//
// HTTP-specific concerns (request parsing, actor resolution from headers,
// attachment linking, response formatting, agent-task triggers) stay in the
// handler. The service owns only what must happen inside the row-locked
// transaction so the audit trail and the DecideFlip invariant cannot diverge
// between callers.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// CommentService owns the transactional create-comment + status-flip pipeline.
// Construct via NewCommentService. Dependencies are intentionally narrow: only
// Queries + TxStarter, because every concern that needs more (events, agent
// tasks, attachments) is handled by the caller post-commit.
type CommentService struct {
	Queries   *db.Queries
	TxStarter TxStarter

	// PUL-479: when telegramOutbox is non-nil, Create INSERTs a
	// telegram_outbox row inside the same tx as the comment INSERT so
	// the outbound mirror is atomic with the comment itself. Wired
	// only when MULTICA_TELEGRAM_ENABLED=true; nil-check makes the
	// feature-flag-off path a compile-time no-op.
	telegramOutbox TelegramOutboxEnqueuer
}

// TelegramOutboxEnqueuer is the tx-scoped surface CommentService needs
// from the outbound bridge. *db.Queries.WithTx(...) provides it in
// production (via InsertTelegramOutboxRow); tests substitute a fake.
// Kept as an interface (not a concrete function) so future kinds
// (status_change / assignee_change) can plug into the same seam.
type TelegramOutboxEnqueuer interface {
	// EnqueueComment writes one 'comment' row to telegram_outbox. Ctx
	// must be the same context the caller's tx is scoped to; qtx must
	// be the *db.Queries bound to that tx (so the INSERT commits with
	// the surrounding tx). authorLabel is the display name that
	// appears in the Telegram header line ("PUL-479 · <label>").
	EnqueueComment(ctx context.Context, qtx *db.Queries, issueID, commentID pgtype.UUID, identifier, authorLabel, content string) error
}

func NewCommentService(q *db.Queries, tx TxStarter) *CommentService {
	return &CommentService{Queries: q, TxStarter: tx}
}

// SetTelegramOutbox enables the outbound mirror. Callers should invoke
// this once at startup after constructing the enqueuer. Passing nil
// disables the mirror (used by tests that want a bare CommentService).
func (s *CommentService) SetTelegramOutbox(enq TelegramOutboxEnqueuer) {
	s.telegramOutbox = enq
}

// CommentCreateParams is the input to CommentService.Create.
//
// IssueID + WorkspaceID identify the issue the comment is attached to. The
// service re-fetches the issue inside the transaction with a row lock, so the
// caller does NOT need to pre-lock. Passing both fields (rather than a
// db.Issue) makes it explicit that the caller's snapshot may be stale.
//
// AuthorType / AuthorID identify who is posting. The caller is responsible
// for any pre-validation (HTTP actor resolution, agent-existence checks).
//
// Content is stored verbatim. Mention expansion is the caller's responsibility
// (the HTTP handler does it via mention.ExpandIssueIdentifiers before calling
// this method; non-HTTP callers like the reminder scheduler typically post
// raw text with no mention expansion).
//
// Type selects the comment type (must be in the comment.type CHECK whitelist).
// ParentID is optional; when set, the service validates it belongs to the
// same issue inside the transaction.
//
// StatusTransition is optional. When non-nil, the service applies the
// transition transactionally with the comment INSERT (UPDATE issue.status +
// InsertStatusHistory). When nil, the service falls back to the
// DecideFlip-driven waiting↔in_progress automatic flip (unless
// SkipDecideFlip is true). At most one of (StatusTransition, DecideFlip)
// drives a transition per call; explicit transitions win.
type CommentCreateParams struct {
	IssueID     pgtype.UUID
	WorkspaceID pgtype.UUID
	AuthorType  string
	AuthorID    pgtype.UUID
	Content     string
	Type        string
	ParentID    pgtype.UUID

	// Meta is a structured payload stored on comment.meta JSONB. Used by
	// non-comment types (currently 'child_progress') to carry data the
	// frontend needs for read-time rendering. nil/empty leaves the column at
	// its default '{}'::jsonb. The discriminator is `meta.kind` (matching
	// comment.type); callers are responsible for serialising a valid payload.
	Meta []byte

	// SourceHistoryID points at the issue_status_history row that produced
	// this comment, when applicable. Currently only set for
	// type='child_progress' fan-outs — feeds the
	// (source_history_id, issue_id) WHERE type='child_progress' unique
	// idempotency index. A duplicate insert returns ErrCommentAlreadyExists.
	SourceHistoryID pgtype.Int8

	SkipDecideFlip   bool
	StatusTransition *StatusTransition
}

// StatusTransition is an explicit issue status change that the caller wants
// applied atomically with the comment INSERT. RefID feeds the
// issue_status_history.ref_id idempotency key (UNIQUE on source + ref_id),
// so retrying the same logical fire-event does not create duplicate history
// rows.
//
// OnlyIfStatusIn is a guard: if the locked issue's current status is not in
// this list, the transition is silently skipped (comment still lands).
// Callers that always want the transition leave OnlyIfStatusIn nil.
type StatusTransition struct {
	ToStatus       string
	Source         string
	ActorType      string
	ActorID        pgtype.UUID
	RefID          string
	OnlyIfStatusIn []string
}

// CommentCreateResult bundles the inserted comment together with the locked
// issue snapshot the transaction saw. UpdatedIssue.Status reflects any
// applied transition; if no transition fired, it equals the pre-call value
// of the row. AppliedTransition is the from/to pair that was actually
// written to issue_status_history, or nil if no transition occurred. Parent
// (when set) is the resolved parent-comment row, which downstream callers
// need for thread-reply heuristics (handler uses it for
// isReplyToMemberThread; the reminder scheduler ignores it).
type CommentCreateResult struct {
	Comment           db.Comment
	UpdatedIssue      db.Issue
	Parent            *db.Comment
	AppliedTransition *AppliedTransition
}

// AppliedTransition records the resolved from/to pair the service wrote to
// issue_status_history. From is captured from the locked row before the
// UPDATE; To is the requested ToStatus.
type AppliedTransition struct {
	From string
	To   string
}

// Sentinel errors callers may inspect.
var (
	// ErrIssueNotFound is returned when the issue row is missing at row-lock
	// time (covers race with delete or a bad IssueID/WorkspaceID pair).
	ErrIssueNotFound = errors.New("issue not found")
	// ErrInvalidParent is returned when ParentID is set but does not belong
	// to the same issue.
	ErrInvalidParent = errors.New("invalid parent comment")
	// ErrCommentAlreadyExists is returned when CreateComment violates a
	// unique constraint — currently only the (source_history_id, issue_id)
	// idempotency index for type='child_progress'. Callers that expect this
	// (the child_progress fan-out worker, mainly) treat it as success.
	ErrCommentAlreadyExists = errors.New("comment already exists")
)

// Create executes the transactional core: row-lock the issue, INSERT the
// comment, optionally apply a status transition (explicit or
// DecideFlip-derived), commit. The returned CommentCreateResult carries the
// post-commit snapshot the caller needs for WS publish, agent triggers, and
// any response formatting.
//
// Callers must NOT begin their own transaction around Create — the service
// owns the entire tx lifecycle to keep the row lock and the audit insert
// atomic. Callers do post-commit work (attachments, WS events, agent
// triggers, response writing) outside the transaction, after Create returns.
func (s *CommentService) Create(ctx context.Context, p CommentCreateParams) (CommentCreateResult, error) {
	var empty CommentCreateResult

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return empty, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.Queries.WithTx(tx)

	lockedIssue, err := qtx.GetIssueForUpdate(ctx, db.GetIssueForUpdateParams{
		ID:          p.IssueID,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return empty, ErrIssueNotFound
		}
		return empty, fmt.Errorf("lock issue: %w", err)
	}

	var parent *db.Comment
	if p.ParentID.Valid {
		row, err := qtx.GetComment(ctx, p.ParentID)
		if err != nil || row.IssueID.Bytes != lockedIssue.ID.Bytes {
			return empty, ErrInvalidParent
		}
		parent = &row
	}

	comment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		IssueID:         lockedIssue.ID,
		WorkspaceID:     lockedIssue.WorkspaceID,
		AuthorType:      p.AuthorType,
		AuthorID:        p.AuthorID,
		Content:         p.Content,
		Type:            p.Type,
		ParentID:        p.ParentID,
		Meta:            p.Meta,
		SourceHistoryID: p.SourceHistoryID,
	})
	if err != nil {
		// PUL-164: the (source_history_id, issue_id) WHERE type='child_progress'
		// unique partial index is the idempotency contract for the fan-out
		// worker. Surface it as ErrCommentAlreadyExists so the worker can
		// treat duplicate fires as success without parsing pg error codes.
		if isUniqueViolation(err) {
			return empty, ErrCommentAlreadyExists
		}
		return empty, fmt.Errorf("insert comment: %w", err)
	}

	// PUL-479 outbound Telegram mirror. Only user-visible comment
	// types get mirrored; system / status_change / progress_update /
	// wake_up / child_progress are internal signalling and stay
	// silent (mirror'ing them would spam the topic). If enqueue
	// fails, roll the whole tx back — we prefer a visible 5xx over
	// a comment that lands in multica without its Telegram twin.
	//
	// We pass identifier="" here (the topic name already includes
	// "PUL-N · title", so per-message headers stay short). The scheduler
	// resolves the author display label from author_type at send time.
	if s.telegramOutbox != nil && p.Type == "comment" {
		if enqErr := s.telegramOutbox.EnqueueComment(
			ctx, qtx, lockedIssue.ID, comment.ID,
			"", p.AuthorType, p.Content,
		); enqErr != nil {
			return empty, fmt.Errorf("enqueue telegram outbox: %w", enqErr)
		}
	}

	updatedIssue := lockedIssue
	var applied *AppliedTransition

	switch {
	case p.StatusTransition != nil:
		if statusMatches(lockedIssue.Status, p.StatusTransition.OnlyIfStatusIn) {
			t := p.StatusTransition
			ui, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID:     lockedIssue.ID,
				Status: t.ToStatus,
			})
			if err != nil {
				return empty, fmt.Errorf("apply status transition: %w", err)
			}

			history, histErr := qtx.InsertStatusHistory(ctx, db.InsertStatusHistoryParams{
				IssueID:    lockedIssue.ID,
				FromStatus: pgtype.Text{String: lockedIssue.Status, Valid: true},
				ToStatus:   t.ToStatus,
				Source:     t.Source,
				ActorID:    t.ActorID,
				ActorType:  pgtype.Text{String: t.ActorType, Valid: t.ActorType != ""},
				RefID:      pgtype.Text{String: t.RefID, Valid: t.RefID != ""},
			})
			// UNIQUE (source, ref_id) is the dedup contract. A duplicate fire on
			// the same RefID is treated as an idempotent no-op so retries (worker
			// crash mid-fire, hook replay) do not break the transaction.
			switch {
			case histErr == nil:
				// Fresh history row → eligible for child_progress fan-out.
				// PUL-164: enqueue the fan-out row inside this tx so the
				// outbox INSERT commits atomically with the status flip.
				if lockedIssue.ParentIssueID.Valid {
					if _, _, fanErr := EnqueueChildProgressFanout(ctx, qtx, EnqueueChildProgressParams{
						WorkspaceID:     lockedIssue.WorkspaceID,
						SourceHistoryID: history.ID,
						ChildIssueID:    lockedIssue.ID,
						PrevStatus:      lockedIssue.Status,
						NewStatus:       t.ToStatus,
						ActorID:         t.ActorID,
						ActorType:       pgtype.Text{String: t.ActorType, Valid: t.ActorType != ""},
					}); fanErr != nil {
						return empty, fmt.Errorf("enqueue child_progress fanout (explicit): %w", fanErr)
					}
				}
			case isUniqueViolation(histErr):
				// Idempotent replay: history row already exists, do NOT
				// enqueue a duplicate fan-out (the original write already
				// did so, and our outbox uniqueness keys on history.id).
			default:
				return empty, fmt.Errorf("insert status history: %w", histErr)
			}

			updatedIssue = ui
			applied = &AppliedTransition{From: lockedIssue.Status, To: t.ToStatus}
		}

	case !p.SkipDecideFlip:
		// DecideFlip is the legacy member<->agent waiting↔in_progress auto-flip
		// (see issue_status_flip.go). It already filters on comment.Type='comment',
		// so non-comment types (system, status_change, progress_update, wake_up)
		// transparently skip without an explicit SkipDecideFlip flag — but the
		// flag lets callers be explicit when they're sure they don't want it.
		if transition := DecideFlip(comment, lockedIssue); transition != nil {
			ui, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID:     lockedIssue.ID,
				Status: transition.ToStatus,
			})
			if err != nil {
				return empty, fmt.Errorf("decideflip status update: %w", err)
			}

			history, histErr := qtx.InsertStatusHistory(ctx, db.InsertStatusHistoryParams{
				IssueID:    lockedIssue.ID,
				FromStatus: pgtype.Text{String: transition.FromStatus, Valid: true},
				ToStatus:   transition.ToStatus,
				Source:     SourceHookComment,
				ActorID:    comment.AuthorID,
				ActorType:  pgtype.Text{String: comment.AuthorType, Valid: true},
				RefID:      pgtype.Text{String: util.UUIDToString(comment.ID), Valid: true},
			})
			switch {
			case histErr == nil:
				// PUL-164: child_progress fan-out. Note: DecideFlip only
				// fires the waiting↔in_progress pair, which is precisely
				// the noisyTransitions set ShouldFanOutTransition filters
				// out — so in practice this enqueue call is always a no-op
				// today. Still call it to be future-proof: any future
				// DecideFlip rule emitting a different pair will fan out
				// without code changes here.
				if lockedIssue.ParentIssueID.Valid {
					if _, _, fanErr := EnqueueChildProgressFanout(ctx, qtx, EnqueueChildProgressParams{
						WorkspaceID:     lockedIssue.WorkspaceID,
						SourceHistoryID: history.ID,
						ChildIssueID:    lockedIssue.ID,
						PrevStatus:      transition.FromStatus,
						NewStatus:       transition.ToStatus,
						ActorID:         comment.AuthorID,
						ActorType:       pgtype.Text{String: comment.AuthorType, Valid: true},
					}); fanErr != nil {
						return empty, fmt.Errorf("enqueue child_progress fanout (decideflip): %w", fanErr)
					}
				}
			case isUniqueViolation(histErr):
				// Idempotent replay — see explicit-transition branch above.
			default:
				return empty, fmt.Errorf("decideflip history insert: %w", histErr)
			}

			updatedIssue = ui
			applied = &AppliedTransition{From: transition.FromStatus, To: transition.ToStatus}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return empty, fmt.Errorf("commit tx: %w", err)
	}

	return CommentCreateResult{
		Comment:           comment,
		UpdatedIssue:      updatedIssue,
		Parent:            parent,
		AppliedTransition: applied,
	}, nil
}

// statusMatches reports whether current is in allowed. nil/empty allowed
// means no guard (transition always proceeds).
func statusMatches(current string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, s := range allowed {
		if s == current {
			return true
		}
	}
	return false
}

// isUniqueViolation matches the helper in handler/handler.go. We duplicate it
// here rather than reach across packages because the service layer must not
// depend on the handler layer (one-way arrow).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

