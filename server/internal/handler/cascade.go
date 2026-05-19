package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/cascade"
)

// cascadeResponseItem is the JSON shape the dashboard consumes.
// Keeping field names snake_case to match the rest of the multica API
// surface; new fields here MUST also be added to the zod schema in
// packages/core/api/schema.ts so the frontend parses safely
// (per CLAUDE.md "Parse, don't cast" rule). The frontend page is a
// follow-up — backend ships first so manual testers can exercise
// the endpoint via curl.
type cascadeResponseItem struct {
	IssueID            string  `json:"issue_id"`
	IssueNumber        int32   `json:"issue_number"`
	IssueTitle         string  `json:"issue_title"`
	IssueStatus        string  `json:"issue_status"`
	IssueAssigneeID    string  `json:"issue_assignee_id,omitempty"`
	IssueAssigneeType  string  `json:"issue_assignee_type,omitempty"`
	CascadeState       string  `json:"cascade_state"`
	CascadeStartedAt   string  `json:"cascade_started_at"`
	CascadeLastEventAt string  `json:"cascade_last_event_at,omitempty"`
	TotalPRs           int     `json:"total_prs,omitempty"`
	CurrentStep        int     `json:"current_step,omitempty"`
	LastPRNumber       int     `json:"last_pr_number,omitempty"`
	LastEventType      string  `json:"last_event_type,omitempty"`
}

type cascadeResponse struct {
	Items   []cascadeResponseItem `json:"items"`
	Page    int                   `json:"page"`
	PerPage int                   `json:"per_page"`
	HasMore bool                  `json:"has_more"`
}

// ListCascades GET /api/cascades — workspace-scoped dashboard data.
// Filters: cascade_state, agent (assignee uuid). Pagination: page,
// per_page (capped at 50).
//
// Auth: standard workspace-scoped path; uses resolveWorkspaceID for
// the requesting workspace context, same as SearchIssues and friends.
func (h *Handler) ListCascades(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := h.resolveWorkspaceID(r)

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	filters := cascade.ListFilters{
		WorkspaceID:  uuid.UUID(wsUUID.Bytes),
		CascadeState: r.URL.Query().Get("cascade_state"),
	}
	if agentStr := r.URL.Query().Get("agent"); agentStr != "" {
		agentUUID, ok := parseUUIDOrBadRequest(w, agentStr, "agent")
		if !ok {
			return
		}
		filters.AgentID = uuid.UUID(agentUUID.Bytes)
	}

	page := cascade.ListPage{Page: 1, PerPage: 50}
	if p := r.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page.Page = v
		}
	}
	if pp := r.URL.Query().Get("per_page"); pp != "" {
		if v, err := strconv.Atoi(pp); err == nil && v > 0 {
			page.PerPage = v
		}
	}

	// Fetch one extra row to populate has_more without a second query.
	// The dashboard's "load more" trigger uses this flag.
	fetchPerPage := page.PerPage + 1
	rows, err := cascade.ListCascades(ctx, h.DB, cascade.ListFilters{
		WorkspaceID:  filters.WorkspaceID,
		CascadeState: filters.CascadeState,
		AgentID:      filters.AgentID,
	}, cascade.ListPage{Page: page.Page, PerPage: fetchPerPage})
	if err != nil {
		slog.Warn("cascade list failed", "error", err, "workspace_id", workspaceID)
		writeError(w, http.StatusInternalServerError, "failed to list cascades")
		return
	}

	hasMore := false
	if len(rows) > page.PerPage {
		rows = rows[:page.PerPage]
		hasMore = true
	}

	resp := cascadeResponse{
		Items:   make([]cascadeResponseItem, 0, len(rows)),
		Page:    page.Page,
		PerPage: page.PerPage,
		HasMore: hasMore,
	}
	for _, r := range rows {
		item := cascadeResponseItem{
			IssueID:          r.IssueID.String(),
			IssueNumber:      r.IssueNumber,
			IssueTitle:       r.IssueTitle,
			IssueStatus:      r.IssueStatus,
			CascadeState:     r.CascadeState,
			CascadeStartedAt: r.CascadeStartedAt.Format(time.RFC3339),
		}
		if r.IssueAssigneeID != nil {
			item.IssueAssigneeID = r.IssueAssigneeID.String()
		}
		item.IssueAssigneeType = r.IssueAssigneeType
		if r.CascadeLastEventAt != nil {
			item.CascadeLastEventAt = r.CascadeLastEventAt.Format(time.RFC3339)
		}
		if r.Progress != nil {
			item.TotalPRs = r.Progress.TotalPRs
			item.CurrentStep = r.Progress.CurrentStep
			item.LastPRNumber = r.Progress.LastPRNumber
			item.LastEventType = r.Progress.LastEventType
		}
		resp.Items = append(resp.Items, item)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// CascadeRetriggerResponse is one row from cascade_retrigger as
// surfaced by GET /api/issues/:id/cascade — the audit log behind
// `multica issue cascade <id>` (PUL-198). Field shape matches the
// CLI's table renderer; null DB columns become JSON nulls so the
// CLI can decide whether to render "—" or the value.
type CascadeRetriggerResponse struct {
	ID           int64   `json:"id"`
	EventID      string  `json:"event_id"`
	EventType    string  `json:"event_type"`
	PRURL        string  `json:"pr_url"`
	PRNumber     int32   `json:"pr_number"`
	HeadSHA      string  `json:"head_sha"`
	FiredAt      string  `json:"fired_at"`
	ProcessedAt  *string `json:"processed_at"`
	Action       *string `json:"action"`
	ActionReason *string `json:"action_reason"`
}

// ListIssueCascade returns cascade_retrigger audit rows for an issue,
// newest first. Backs `multica issue cascade <id>` — operators reach
// for this when "cascade did not spawn for this PR" and they need to
// know whether the row was loop-guarded, scope-filtered, or queued
// behind an active run, plus the deploy-flip outcome (PUL-194) carried
// in action_reason. See PUL-198.
func (h *Handler) ListIssueCascade(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	rows, err := h.DB.Query(r.Context(), `
SELECT id, event_id, event_type, pr_url, pr_number, head_sha,
       fired_at, processed_at, action, action_reason
FROM cascade_retrigger
WHERE issue_id = $1
ORDER BY fired_at DESC, id DESC`,
		issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list cascade events")
		return
	}
	defer rows.Close()

	resp := make([]CascadeRetriggerResponse, 0, 16)
	for rows.Next() {
		var (
			row          CascadeRetriggerResponse
			eventID      pgtype.UUID
			fired        pgtype.Timestamptz
			processed    pgtype.Timestamptz
			action       pgtype.Text
			actionReason pgtype.Text
		)
		if err := rows.Scan(
			&row.ID, &eventID, &row.EventType, &row.PRURL, &row.PRNumber, &row.HeadSHA,
			&fired, &processed, &action, &actionReason,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read cascade row")
			return
		}
		if eventID.Valid {
			row.EventID = uuid.UUID(eventID.Bytes).String()
		}
		if fired.Valid {
			row.FiredAt = fired.Time.Format(time.RFC3339)
		}
		if processed.Valid {
			s := processed.Time.Format(time.RFC3339)
			row.ProcessedAt = &s
		}
		if action.Valid {
			s := action.String
			row.Action = &s
		}
		if actionReason.Valid {
			s := actionReason.String
			row.ActionReason = &s
		}
		resp = append(resp, row)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to iterate cascade rows")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
