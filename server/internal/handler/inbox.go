package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// PUL-445 pagination bounds for GET /api/inbox. The default is small
// enough that first-paint fits comfortably in a couple of hundred KB
// even with the 5-CTE JOIN payload per row; the cap prevents a
// single caller from re-creating the pre-pagination behavior by
// asking for the whole tail. Keyset (?before=<created_at>) walks
// past the default without re-scanning the head.
const (
	inboxDefaultLimit = 50
	inboxMaxLimit     = 200
)

type InboxItemResponse struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	RecipientType string          `json:"recipient_type"`
	RecipientID   string          `json:"recipient_id"`
	Type          string          `json:"type"`
	Severity      string          `json:"severity"`
	IssueID       *string         `json:"issue_id"`
	Title         string          `json:"title"`
	Body          *string         `json:"body"`
	Read          bool            `json:"read"`
	Archived      bool            `json:"archived"`
	CreatedAt     string          `json:"created_at"`
	IssueStatus   *string         `json:"issue_status"`
	ActorType     *string         `json:"actor_type"`
	ActorID       *string         `json:"actor_id"`
	Details       json.RawMessage `json:"details"`

	// PUL-177 phase + last-applied-skill chips for the Inbox row.
	// Phase is always present (derived from issue.status with a
	// default fallback); LatestSkill is null for tickets that have
	// never had a skill applied.
	Phase       string              `json:"phase"`
	LatestSkill *SkillStateResponse `json:"latest_skill"`

	// PUL-180 ownership chip — the third Inbox slot answers "who's
	// holding the ball right now". Derived server-side from
	// agent_task_queue, issue.status, and the most recent comment +
	// status_history rows; see deriveOwnership.
	//
	// Ownership is null when the chip is hidden — phase=done /
	// phase=cancelled (closed ticket, nothing to act on) and when the
	// inbox row targets an agent recipient (agent-to-agent inbox; the
	// chip is a human-affordance only). On a live ticket for a
	// member recipient ownership is always one of "me" / "agent" /
	// "waiting".
	//
	// OwnershipMeta is paired with Ownership and carries the
	// tooltip-source signals (since-timestamp, optional agent name,
	// optional waiting-reason). It is null whenever Ownership is null.
	// JSON tags use explicit-null (no omitempty) so old clients see
	// `ownership: null` rather than a missing key (T4 contract).
	Ownership     *string                `json:"ownership"`
	OwnershipMeta *OwnershipMetaResponse `json:"ownership_meta"`
}

// OwnershipMetaResponse carries the tooltip signals for the Inbox
// ownership chip. All three fields are optional with explicit-null
// JSON encoding so the TypeScript view receives a consistent shape:
//   - Since      — ISO-8601 timestamp the chip's "N мин назад" tooltip
//                  suffix counts from. Source varies by ownership:
//                    me      → max(last status_history, last comment)
//                    agent   → started_at OR dispatched_at fallback
//                    waiting → issue.updated_at
//                  Nullable when none of the underlying timestamps
//                  exist (brand-new issue, no agent_task_queue rows).
//   - AgentName  — set only for ownership="agent" so the tooltip can
//                  read "agent-1 работает, 2 мин назад". Comes from
//                  agent.name (NOT NULL in schema) via the
//                  active_tasks CTE JOIN.
//   - Reason     — set only for ownership="waiting": "review" when
//                  phase=review (status=waiting), "approval" when
//                  phase=blocked. The frontend localizes via
//                  common.ownership.tooltip.waiting_{review,approval}.
type OwnershipMetaResponse struct {
	Since     *string `json:"since"`
	AgentName *string `json:"agent_name"`
	Reason    *string `json:"reason"`
}

// activeTaskRow is the value-typed projection of the active_tasks
// CTE row that deriveOwnership consumes. Defined separately from
// db.ListInboxItemsRow so the pure function stays testable without
// dragging in pgtype.* shells.
type activeTaskRow struct {
	AgentName    string
	DispatchedAt *time.Time
	StartedAt    *time.Time
}

// lastActivity bundles the two MAX(created_at) signals from the
// last_status + last_comment CTEs into one carrier so deriveOwnership
// stays single-purpose for the me-since calculation.
type lastActivity struct {
	StatusAt  *time.Time
	CommentAt *time.Time
}

// deriveOwnership computes the Inbox ownership chip value + meta from
// pure inputs. It is the only place in the codebase that encodes the
// three precedence rules:
//
//  1. recipient is an agent → chip hidden (agent-to-agent inbox; chip
//     is a human-affordance only). Returns "" / nil so the JSON
//     marshals as `ownership: null`.
//  2. phase is closed (done / cancelled) → chip hidden, same as (1).
//  3. an active agent_task_queue row exists for the issue → "agent"
//     wins over status=waiting/blocked, per /office-hours Q1: a
//     ticket that is "waiting on review" but happens to have a
//     re-run agent task in flight is more accurately characterized
//     as "agent working" right now.
//  4. otherwise the phase determines waiting/approval semantics
//     (PhaseReview → "waiting"/reason="review";
//      PhaseBlocked → "waiting"/reason="approval"); everything else
//     falls through to the "me" default.
//
// Signature note (per /plan-eng-review A1): we only take `phase`, not
// raw issueStatus — derivePhaseFromStatus is the single source of
// truth that already collapses status → phase upstream. Passing both
// would invite drift.
func deriveOwnership(
	recipientType string,
	phase string,
	activeTask *activeTaskRow,
	last lastActivity,
	issueUpdatedAt *time.Time,
) (string, *OwnershipMetaResponse) {
	if recipientType == "agent" {
		return "", nil
	}
	if phase == "done" || phase == "cancelled" {
		return "", nil
	}
	if activeTask != nil {
		since := pickTaskTime(activeTask)
		return "agent", &OwnershipMetaResponse{
			AgentName: stringPtrIfNonEmpty(activeTask.AgentName),
			Since:     timePtrToISO(since),
		}
	}
	switch phase {
	case "review":
		return "waiting", &OwnershipMetaResponse{
			Reason: stringPtr("review"),
			Since:  timePtrToISO(issueUpdatedAt),
		}
	case "blocked":
		return "waiting", &OwnershipMetaResponse{
			Reason: stringPtr("approval"),
			Since:  timePtrToISO(issueUpdatedAt),
		}
	}
	return "me", &OwnershipMetaResponse{
		Since: timePtrToISO(pickMeTime(last)),
	}
}

// pickTaskTime returns the running-task timestamp to surface in the
// tooltip — prefer started_at (the moment a worker actually picked up
// the row), fall back to dispatched_at (queued/dispatched window
// before a worker grabs it). Both may be nil for a freshly-queued
// row, in which case the meta.since stays nil and the tooltip omits
// the "N мин назад" suffix.
func pickTaskTime(t *activeTaskRow) *time.Time {
	if t.StartedAt != nil {
		return t.StartedAt
	}
	return t.DispatchedAt
}

// pickMeTime picks the more-recent of the two "ход за мной" signals.
// Either side may be nil for very fresh issues (no comments yet, no
// status transitions yet) — the tooltip degrades to no suffix.
func pickMeTime(la lastActivity) *time.Time {
	switch {
	case la.StatusAt != nil && la.CommentAt != nil:
		if la.StatusAt.After(*la.CommentAt) {
			return la.StatusAt
		}
		return la.CommentAt
	case la.StatusAt != nil:
		return la.StatusAt
	case la.CommentAt != nil:
		return la.CommentAt
	}
	return nil
}

func stringPtr(s string) *string { return &s }

func stringPtrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtrToISO(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func pgTimestampToPtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

// derivePhaseFromStatus maps issue.status to the coarser
// workflow-phase concept the Inbox PhaseChip renders. PUL-177
// /office-hours Q4 reframe — the user's real question is "should I
// open this ticket now", not "what skill stage". Mapping is
// intentionally lossy: planning/coding/review/done aggregate the
// finer-grained multica statuses into the 5 chips a Vadim-on-phone
// can scan in <1 second.
//
// Empty input (NULL issue.status, which happens for unlinked inbox
// items) and unknown values both fall back to "backlog" — those
// rows aren't broken, they're just not yet workflowed.
func derivePhaseFromStatus(status string) string {
	switch status {
	case "backlog", "todo":
		return "backlog"
	case "planning":
		return "planning"
	case "in_progress", "developing":
		return "coding"
	case "waiting":
		return "review"
	case "deployed":
		return "done"
	case "blocked":
		return "blocked"
	case "cancelled":
		return "cancelled"
	default:
		return "backlog"
	}
}

func inboxToResponse(i db.InboxItem) InboxItemResponse {
	// Single-row variant (used by MarkRead, Archive, etc. event
	// publishes). The db.InboxItem model doesn't carry the issue
	// status join or the latest-skill subquery, so Phase defaults
	// to "backlog" — callers can enrichInboxResponse() if they
	// need a derived phase, but at this point the row is generally
	// being shipped as an event payload and consumers re-fetch from
	// ListInbox anyway.
	return InboxItemResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		RecipientType: i.RecipientType,
		RecipientID:   uuidToString(i.RecipientID),
		Type:          i.Type,
		Severity:      i.Severity,
		IssueID:       uuidToPtr(i.IssueID),
		Title:         i.Title,
		Body:          textToPtr(i.Body),
		Read:          i.Read,
		Archived:      i.Archived,
		CreatedAt:     timestampToString(i.CreatedAt),
		ActorType:     textToPtr(i.ActorType),
		ActorID:       uuidToPtr(i.ActorID),
		Details:       json.RawMessage(i.Details),
		Phase:         derivePhaseFromStatus(""),
	}
}

func inboxRowToResponse(r db.ListInboxItemsRow) InboxItemResponse {
	// Phase derivation reads issue.status from the join. Empty when
	// the issue row is missing (which shouldn't happen for a healthy
	// inbox_item, but the join is LEFT so we handle the NULL).
	var statusForPhase string
	if r.IssueStatus.Valid {
		statusForPhase = r.IssueStatus.String
	}
	phase := derivePhaseFromStatus(statusForPhase)

	var latest *SkillStateResponse
	if r.LatestSkillSlug.Valid {
		ls := SkillStateResponse{
			Skill:       r.LatestSkillSlug.String,
			Status:      r.LatestSkillStatus.String,
			StartedAt:   timestampToString(r.LatestSkillStartedAt),
			CompletedAt: timestampToPtr(r.LatestSkillCompletedAt),
			UpdatedAt:   timestampToString(r.LatestSkillUpdatedAt),
		}
		latest = &ls
	}

	// PUL-180: ownership chip. The active_tasks CTE only emits a row
	// when a queued/dispatched/running task exists for the issue, so
	// ActiveTaskAgentID.Valid is the membership signal. We materialize
	// the projection only when present — deriveOwnership treats nil
	// active task as "no agent currently holds the ball" and falls
	// through to the phase/me path.
	var activeTask *activeTaskRow
	if r.ActiveTaskAgentID.Valid {
		activeTask = &activeTaskRow{
			AgentName:    r.ActiveTaskAgentName.String,
			DispatchedAt: pgTimestampToPtr(r.ActiveTaskDispatchedAt),
			StartedAt:    pgTimestampToPtr(r.ActiveTaskStartedAt),
		}
	}
	last := lastActivity{
		StatusAt:  pgTimestampToPtr(r.LastStatusAt),
		CommentAt: pgTimestampToPtr(r.LastCommentAt),
	}
	ownership, ownershipMeta := deriveOwnership(
		r.RecipientType,
		phase,
		activeTask,
		last,
		pgTimestampToPtr(r.IssueUpdatedAt),
	)

	return InboxItemResponse{
		ID:            uuidToString(r.ID),
		WorkspaceID:   uuidToString(r.WorkspaceID),
		RecipientType: r.RecipientType,
		RecipientID:   uuidToString(r.RecipientID),
		Type:          r.Type,
		Severity:      r.Severity,
		IssueID:       uuidToPtr(r.IssueID),
		Title:         r.Title,
		Body:          textToPtr(r.Body),
		Read:          r.Read,
		Archived:      r.Archived,
		CreatedAt:     timestampToString(r.CreatedAt),
		IssueStatus:   textToPtr(r.IssueStatus),
		ActorType:     textToPtr(r.ActorType),
		ActorID:       uuidToPtr(r.ActorID),
		Details:       json.RawMessage(r.Details),
		Phase:         phase,
		LatestSkill:   latest,
		Ownership:     ownershipPtrOrNil(ownership),
		OwnershipMeta: ownershipMeta,
	}
}

// ownershipPtrOrNil maps the deriveOwnership empty-string sentinel
// (used to signal "chip hidden") onto a JSON null so the wire shape
// stays `ownership: null` rather than `ownership: ""`. The non-empty
// branch wraps the slug in a pointer so the field can be omitted-via-
// nil from per-row construction sites that don't compute ownership
// (inboxToResponse, used for single-row event publishes).
func ownershipPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (h *Handler) enrichInboxResponse(ctx context.Context, resp InboxItemResponse, issueID pgtype.UUID) InboxItemResponse {
	if !issueID.Valid {
		return resp
	}
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err == nil {
		s := issue.Status
		resp.IssueStatus = &s
		// PUL-177: keep Phase in sync with IssueStatus when the
		// enrich path fires. ListInbox already derives this from
		// the JOIN result, but the event-publish path uses this
		// helper and would otherwise emit Phase="backlog" forever.
		resp.Phase = derivePhaseFromStatus(s)
	}
	return resp
}

func (h *Handler) ListInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	q := r.URL.Query()
	limit := int32(inboxDefaultLimit)
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = int32(v)
		}
	}
	if limit > inboxMaxLimit {
		limit = inboxMaxLimit
	}

	var before pgtype.Timestamptz
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid before parameter; expected RFC3339 format")
			return
		}
		before = pgtype.Timestamptz{Time: t, Valid: true}
	}

	items, err := h.Queries.ListInboxItems(r.Context(), db.ListInboxItemsParams{
		WorkspaceID:   wsUUID,
		RecipientType: "member",
		RecipientID:   parseUUID(userID),
		Before:        before,
		Lim:           limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list inbox")
		return
	}

	resp := make([]InboxItemResponse, len(items))
	for i, item := range items {
		resp[i] = inboxRowToResponse(item)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) MarkInboxRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	item, err := h.Queries.MarkInboxRead(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}

	userID := requestUserID(r)
	workspaceID := uuidToString(item.WorkspaceID)
	h.publish(protocol.EventInboxRead, workspaceID, "member", userID, map[string]any{
		"item_id":      uuidToString(item.ID),
		"recipient_id": uuidToString(item.RecipientID),
	})

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ArchiveInboxItem(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	item, err := h.Queries.ArchiveInboxItem(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive")
		return
	}

	// Archive all sibling inbox items for the same issue (issue-level archive)
	if item.IssueID.Valid {
		h.Queries.ArchiveInboxByIssue(r.Context(), db.ArchiveInboxByIssueParams{
			WorkspaceID:   item.WorkspaceID,
			RecipientType: item.RecipientType,
			RecipientID:   item.RecipientID,
			IssueID:       item.IssueID,
		})
	}

	userID := requestUserID(r)
	workspaceID := uuidToString(item.WorkspaceID)
	h.publish(protocol.EventInboxArchived, workspaceID, "member", userID, map[string]any{
		"item_id":      uuidToString(item.ID),
		"issue_id":     uuidToPtr(item.IssueID),
		"recipient_id": uuidToString(item.RecipientID),
	})

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CountUnreadInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.CountUnreadInbox(r.Context(), db.CountUnreadInboxParams{
		WorkspaceID:   wsUUID,
		RecipientType: "member",
		RecipientID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count unread inbox")
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

func (h *Handler) MarkAllInboxRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.MarkAllInboxRead(r.Context(), db.MarkAllInboxReadParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark all inbox read")
		return
	}

	slog.Info("inbox: mark all read", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	h.publish(protocol.EventInboxBatchRead, workspaceID, "member", userID, map[string]any{
		"recipient_id": userID,
		"count":        count,
	})

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (h *Handler) ArchiveAllInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.ArchiveAllInbox(r.Context(), db.ArchiveAllInboxParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive all inbox")
		return
	}

	slog.Info("inbox: archive all", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	h.publish(protocol.EventInboxBatchArchived, workspaceID, "member", userID, map[string]any{
		"recipient_id": userID,
		"count":        count,
	})

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (h *Handler) ArchiveAllReadInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.ArchiveAllReadInbox(r.Context(), db.ArchiveAllReadInboxParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive all read inbox")
		return
	}

	slog.Info("inbox: archive all read", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	h.publish(protocol.EventInboxBatchArchived, workspaceID, "member", userID, map[string]any{
		"recipient_id": userID,
		"count":        count,
	})

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (h *Handler) ArchiveCompletedInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.ArchiveCompletedInbox(r.Context(), db.ArchiveCompletedInboxParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive completed inbox")
		return
	}

	slog.Info("inbox: archive completed", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	h.publish(protocol.EventInboxBatchArchived, workspaceID, "member", userID, map[string]any{
		"recipient_id": userID,
		"count":        count,
	})

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}
