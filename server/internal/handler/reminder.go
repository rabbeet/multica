// PUL-154 «Wake up in N»: HTTP routes for issue-scoped reminders.
//
// Three endpoints, all behind the existing RequireWorkspaceMember middleware:
//
//   POST   /api/issues/{id}/reminders        — schedule a new reminder
//   GET    /api/issues/{id}/reminders        — list pending reminders for issue
//   DELETE /api/reminders/{reminderId}       — cancel a pending reminder
//
// The DELETE route lives outside /api/issues/{id} because clients only know
// the reminder.id (returned by Create or List), not the parent issue id —
// matching the same pattern as /api/comments/{commentId} elsewhere.
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Server-side guardrails. Mirrored by the DB CHECK constraints in
// migration 076 so a bypass that skips this handler still fails to insert.
const (
	reminderMaxNoteLen   = 500
	reminderMaxFutureDur = 365 * 24 * time.Hour
	// reminderMinFutureDur prevents racing the next scheduler tick: a
	// reminder set for "5 seconds from now" would likely fire on the very
	// next tick anyway, but enforcing a small floor here makes the
	// activity-cancel window meaningful and gives the UI time to render the
	// pending chip before the fire event lands.
	reminderMinFutureDur = 30 * time.Second
)

type CreateReminderRequest struct {
	FireAt string  `json:"fire_at"` // RFC3339
	Note   *string `json:"note,omitempty"`
}

type ReminderResponse struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	IssueID        string  `json:"issue_id"`
	CreatedByType  string  `json:"created_by_type"`
	CreatedByID    string  `json:"created_by_id"`
	FireAt         string  `json:"fire_at"`
	Note           *string `json:"note"`
	Status         string  `json:"status"`
	FiredAt        *string `json:"fired_at"`
	FiredCommentID *string `json:"fired_comment_id"`
	CancelledAt    *string `json:"cancelled_at"`
	CancelReason   *string `json:"cancel_reason"`
	CreatedAt      string  `json:"created_at"`
}

func reminderToResponse(r db.IssueReminder) ReminderResponse {
	return ReminderResponse{
		ID:             uuidToString(r.ID),
		WorkspaceID:    uuidToString(r.WorkspaceID),
		IssueID:        uuidToString(r.IssueID),
		CreatedByType:  r.CreatedByType,
		CreatedByID:    uuidToString(r.CreatedByID),
		FireAt:         timestampToString(r.FireAt),
		Note:           textToPtr(r.Note),
		Status:         r.Status,
		FiredAt:        timestampToPtr(r.FiredAt),
		FiredCommentID: uuidToPtr(r.FiredCommentID),
		CancelledAt:    timestampToPtr(r.CancelledAt),
		CancelReason:   textToPtr(r.CancelReason),
		CreatedAt:      timestampToString(r.CreatedAt),
	}
}

// CreateReminder schedules a wake-up reminder on the addressed issue.
// Validates fire_at falls in a sensible window and that any note honors the
// length cap. The actual transactional work (UPDATE issue.status, INSERT
// comment) happens later in reminder_scheduler.go — this endpoint only
// persists the intent.
func (h *Handler) CreateReminder(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateReminderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	fireAt, err := time.Parse(time.RFC3339, req.FireAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid fire_at; expected RFC3339")
		return
	}

	now := time.Now().UTC()
	if !fireAt.After(now.Add(reminderMinFutureDur)) {
		writeError(w, http.StatusBadRequest, "fire_at must be at least 30s in the future")
		return
	}
	if fireAt.After(now.Add(reminderMaxFutureDur)) {
		writeError(w, http.StatusBadRequest, "fire_at must be within 1 year")
		return
	}

	var note pgtype.Text
	if req.Note != nil {
		if len(*req.Note) > reminderMaxNoteLen {
			writeError(w, http.StatusBadRequest, "note exceeds 500 characters")
			return
		}
		if *req.Note != "" {
			note = pgtype.Text{String: *req.Note, Valid: true}
		}
	}

	authorType, authorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))

	reminder, err := h.Queries.CreateIssueReminder(r.Context(), db.CreateIssueReminderParams{
		WorkspaceID:   issue.WorkspaceID,
		IssueID:       issue.ID,
		CreatedByType: authorType,
		CreatedByID:   parseUUID(authorID),
		FireAt:        pgtype.Timestamptz{Time: fireAt, Valid: true},
		Note:          note,
	})
	if err != nil {
		slog.Warn("create reminder failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create reminder")
		return
	}

	resp := reminderToResponse(reminder)
	h.publish(protocol.EventReminderCreated, uuidToString(issue.WorkspaceID), authorType, authorID, map[string]any{
		"reminder": resp,
	})
	writeJSON(w, http.StatusCreated, resp)
}

// ListPendingReminders returns the pending reminders for an issue. Fired and
// cancelled rows are intentionally excluded — they exist only to make the
// audit trail diffable; the UI does not render them.
func (h *Handler) ListPendingReminders(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	rows, err := h.Queries.ListPendingRemindersForIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list reminders")
		return
	}

	out := make([]ReminderResponse, len(rows))
	for i, row := range rows {
		out[i] = reminderToResponse(row)
	}
	writeJSON(w, http.StatusOK, out)
}

// CancelReminder cancels a pending reminder. Permitted callers: the reminder
// creator OR a workspace owner/admin. Anyone else gets 403. Already-fired
// or already-cancelled reminders return 409.
func (h *Handler) CancelReminder(w http.ResponseWriter, r *http.Request) {
	reminderID := chi.URLParam(r, "reminderId")
	reminderUUID, ok := parseUUIDOrBadRequest(w, reminderID, "reminder id")
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	reminder, err := h.Queries.GetIssueReminder(r.Context(), reminderUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "reminder not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load reminder")
		return
	}

	workspaceID := uuidToString(reminder.WorkspaceID)
	callerType, callerID := h.resolveActor(r, userID, workspaceID)

	// Permission: creator OR workspace owner/admin. For single-user multica
	// these collapse to the same person, but the check stays explicit so a
	// future multi-member workspace inherits the right semantics.
	isCreator := callerType == reminder.CreatedByType && callerID == uuidToString(reminder.CreatedByID)
	isAdmin := false
	if member, mErr := h.getWorkspaceMember(r.Context(), userID, workspaceID); mErr == nil {
		isAdmin = roleAllowed(member.Role, "owner", "admin")
	}
	if !isCreator && !isAdmin {
		writeError(w, http.StatusForbidden, "only the reminder creator or a workspace admin can cancel")
		return
	}

	cancelled, err := h.Queries.CancelIssueReminder(r.Context(), reminderUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Row exists but status != 'pending' — already fired or cancelled.
			writeError(w, http.StatusConflict, "reminder is not pending")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to cancel reminder")
		return
	}

	resp := reminderToResponse(cancelled)
	h.publish(protocol.EventReminderCancelled, workspaceID, callerType, callerID, map[string]any{
		"reminder":      resp,
		"cancel_reason": "manual",
	})
	writeJSON(w, http.StatusOK, resp)
}

