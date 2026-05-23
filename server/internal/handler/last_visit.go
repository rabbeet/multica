package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-239 cross-device last-visit sync for Mission Control delta-mode.
//
// PUL-231 PR2/PR3 shipped useLastVisitStore as workspace-scoped
// localStorage. That gave us the timestamp on the device the user
// touched, but visiting an issue on phone left no signal for the Mac.
// This handler is the server-side promotion: per-(workspace, user)
// upsert + list, hydrated into the same store at mount time so the
// client UX stays identical.

// LastVisitItem is one row of the map returned by ListUserIssueLastVisits.
type LastVisitItem struct {
	IssueID       string `json:"issue_id"`
	LastVisitedAt string `json:"last_visited_at"`
}

// MarkIssueVisited records the current actor (member or agent) as
// having visited the given issue right now. Idempotent — repeated
// calls just bump the timestamp. The request body is empty; the issue
// id comes from the path and the (workspace, user, user_type) tuple
// from the auth headers.
func (h *Handler) MarkIssueVisited(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	issueID := chi.URLParam(r, "id")
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue id")
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	actorUUID, ok := parseUUIDOrBadRequest(w, actorID, "actor id")
	if !ok {
		return
	}

	if err := h.Queries.UpsertUserIssueLastVisit(ctx, db.UpsertUserIssueLastVisitParams{
		WorkspaceID: wsUUID,
		UserID:      actorUUID,
		UserType:    actorType,
		IssueID:     issueUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark issue visited")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListLastVisits returns the full last-visit map for the current actor
// in the current workspace. Mission Control hits this once on mount
// to hydrate the client zustand store.
func (h *Handler) ListLastVisits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	actorUUID, ok := parseUUIDOrBadRequest(w, actorID, "actor id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListUserIssueLastVisits(ctx, db.ListUserIssueLastVisitsParams{
		WorkspaceID: wsUUID,
		UserID:      actorUUID,
		UserType:    actorType,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list last visits")
		return
	}

	items := make([]LastVisitItem, len(rows))
	for i, row := range rows {
		items[i] = LastVisitItem{
			IssueID:       uuidToString(row.IssueID),
			LastVisitedAt: timestampToString(row.LastVisitedAt),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}
