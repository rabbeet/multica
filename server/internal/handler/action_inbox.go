package handler

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Returns the underlying string, or empty when Valid=false. The inbox
// payload doesn't NULL-elide content/status fields — readers treat an
// empty body as a real (if rare) state distinct from "no comment".
func pgTextValue(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// PUL-231 Mission Control workspace inbox.
//
// One endpoint, one SQL query: returns up to 100 active issues with
// their latest agent comment + latest skill state pre-joined. The
// client (packages/views/inbox/components/mission-control.tsx) runs
// extractAgentActions() on the comment body to render chip rows
// without a per-issue follow-up fetch.

// ActionInboxItem is one row in the workspace action-inbox feed.
//
// Field-naming mirrors IssueResponse where the underlying column is
// the same, plus three nested-but-flat groups (LatestAgentComment,
// LatestSkill, derived AwaitingUser) so the client can read them
// straight from the JSON without an extra parse layer.
type ActionInboxItem struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	Number       int32   `json:"number"`
	Identifier   string  `json:"identifier"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	CreatorType  string  `json:"creator_type"`
	CreatorID    string  `json:"creator_id"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`

	// Latest agent-authored comment on this issue, if any. nil when no
	// agent has commented yet (issue is in pre-pickup state).
	LatestAgentComment *LatestAgentComment `json:"latest_agent_comment,omitempty"`
	// Most recent skill phase the issue has touched (office-hours,
	// plan-eng-review, etc.). nil when no skill event has fired yet.
	LatestSkill *LatestSkill `json:"latest_skill,omitempty"`
}

type LatestAgentComment struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	AuthorID  string `json:"author_id"`
	CreatedAt string `json:"created_at"`
}

type LatestSkill struct {
	Slug      string `json:"slug"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
}

func actionInboxRowToResponse(r db.ListWorkspaceActionInboxRow, issuePrefix string) ActionInboxItem {
	item := ActionInboxItem{
		ID:           uuidToString(r.ID),
		WorkspaceID:  uuidToString(r.WorkspaceID),
		Number:       r.Number,
		Identifier:   issuePrefix + "-" + strconv.Itoa(int(r.Number)),
		Title:        r.Title,
		Status:       r.Status,
		Priority:     r.Priority,
		AssigneeType: textToPtr(r.AssigneeType),
		AssigneeID:   uuidToPtr(r.AssigneeID),
		CreatorType:  r.CreatorType,
		CreatorID:    uuidToString(r.CreatorID),
		CreatedAt:    timestampToString(r.CreatedAt),
		UpdatedAt:    timestampToString(r.UpdatedAt),
	}

	if r.LatestAgentCommentID.Valid {
		item.LatestAgentComment = &LatestAgentComment{
			ID:        uuidToString(r.LatestAgentCommentID),
			Content:   pgTextValue(r.LatestAgentCommentContent),
			AuthorID:  uuidToString(r.LatestAgentAuthorID),
			CreatedAt: timestampToString(r.LatestAgentCommentAt),
		}
	}
	if r.SkillSlug.Valid {
		item.LatestSkill = &LatestSkill{
			Slug:      r.SkillSlug.String,
			Status:    pgTextValue(r.SkillStatus),
			UpdatedAt: timestampToString(r.SkillUpdatedAt),
		}
	}

	return item
}

// ListWorkspaceActionInbox returns the Mission Control feed for the
// current workspace — up to 100 active issues, agent-commented ones
// first, with the latest agent comment body pre-joined so the client
// can render chip rows without an N+1 fetch.
func (h *Handler) ListWorkspaceActionInbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListWorkspaceActionInbox(ctx, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list action inbox")
		return
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	items := make([]ActionInboxItem, len(rows))
	for i, row := range rows {
		items[i] = actionInboxRowToResponse(row, prefix)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}
