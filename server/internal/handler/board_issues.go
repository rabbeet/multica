package handler

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-468: bucket-list / board endpoint.
//
// The Issues and My Issues pages used to render by firing one
// GET /api/issues?status=<s> per status in BOARD_STATUSES (10 parallel
// requests) plus an implicit CountIssues per request. This endpoint returns
// every bucket in a single response — the client's ListIssuesCache shape is
// preserved so the swap in fetchFirstPages is drop-in.
//
// The endpoint is gated by MULTICA_ISSUES_BOARD_ENDPOINT so operators can
// dark-launch it and roll back without a client redeploy. When the flag is
// off the route returns 404, matching what the client sees against a server
// that hasn't been upgraded yet — so old-client-plus-new-server and
// new-client-plus-old-server both fall back to the legacy fanout with zero
// behaviour change.
const (
	boardEnvFlag = "MULTICA_ISSUES_BOARD_ENDPOINT"

	boardDefaultLimit = 50
	boardMaxLimit     = 100
	// Hard cap on the number of statuses per request. BOARD_STATUSES has 10
	// today; 20 leaves headroom for future status additions while bounding
	// the worst-case row-touch to boardMaxStatuses * boardMaxLimit rows.
	boardMaxStatuses = 20
)

// BoardBucket is one status column in the response — the shape mirrors the
// per-status entry the client's ListIssuesCache stores today.
type BoardBucket struct {
	Issues []IssueResponse `json:"issues"`
	Total  int64           `json:"total"`
}

// BoardIssuesResponse is the top-level bucket-list envelope. Every requested
// status appears as a key, even when empty, so the client never has to
// distinguish "server didn't return this status" from "this status has zero
// matching issues".
type BoardIssuesResponse struct {
	ByStatus map[string]BoardBucket `json:"by_status"`
}

// IsBoardEndpointEnabled reports whether MULTICA_ISSUES_BOARD_ENDPOINT
// selects the new endpoint. Read on every request so operators can flip the
// flag without a restart.
func IsBoardEndpointEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(boardEnvFlag))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// BoardIssues serves the per-status bucket-list. See package doc above for
// the rollout rationale and feature-flag semantics.
func (h *Handler) BoardIssues(w http.ResponseWriter, r *http.Request) {
	if !IsBoardEndpointEnabled() {
		writeError(w, http.StatusNotFound, "endpoint not enabled")
		return
	}

	ctx := r.Context()

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	q := r.URL.Query()

	statuses, ok := parseBoardStatuses(w, q.Get("statuses"))
	if !ok {
		return
	}

	limit := int32(boardDefaultLimit)
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > boardMaxLimit {
				v = boardMaxLimit
			}
			limit = int32(v)
		}
	}

	// Filters — same set the legacy ListIssues path accepts. Malformed UUID
	// filters return 400 rather than being coerced to zero, matching the
	// existing ListIssues behaviour (see issue.go:634-666).
	var priorityFilter pgtype.Text
	if p := q.Get("priority"); p != "" {
		priorityFilter = pgtype.Text{String: p, Valid: true}
	}
	var assigneeFilter pgtype.UUID
	if a := q.Get("assignee_id"); a != "" {
		id, ok := parseUUIDOrBadRequest(w, a, "assignee_id")
		if !ok {
			return
		}
		assigneeFilter = id
	}
	var assigneeIdsFilter []pgtype.UUID
	if ids := q.Get("assignee_ids"); ids != "" {
		for _, raw := range strings.Split(ids, ",") {
			if s := strings.TrimSpace(raw); s != "" {
				id, ok := parseUUIDOrBadRequest(w, s, "assignee_ids")
				if !ok {
					return
				}
				assigneeIdsFilter = append(assigneeIdsFilter, id)
			}
		}
	}
	var creatorFilter pgtype.UUID
	if c := q.Get("creator_id"); c != "" {
		id, ok := parseUUIDOrBadRequest(w, c, "creator_id")
		if !ok {
			return
		}
		creatorFilter = id
	}
	var projectFilter pgtype.UUID
	if p := q.Get("project_id"); p != "" {
		id, ok := parseUUIDOrBadRequest(w, p, "project_id")
		if !ok {
			return
		}
		projectFilter = id
	}

	issues, err := h.Queries.ListIssuesBoard(ctx, db.ListIssuesBoardParams{
		WorkspaceID: wsUUID,
		Limit:       limit,
		Statuses:    statuses,
		Priority:    priorityFilter,
		AssigneeID:  assigneeFilter,
		AssigneeIds: assigneeIdsFilter,
		CreatorID:   creatorFilter,
		ProjectID:   projectFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list board")
		return
	}

	counts, err := h.Queries.CountIssuesByStatus(ctx, db.CountIssuesByStatusParams{
		WorkspaceID: wsUUID,
		Statuses:    statuses,
		Priority:    priorityFilter,
		AssigneeID:  assigneeFilter,
		AssigneeIds: assigneeIdsFilter,
		CreatorID:   creatorFilter,
		ProjectID:   projectFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count board")
		return
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	ids := make([]pgtype.UUID, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)

	// Seed every requested status with an empty bucket so the JSON response
	// never omits a key the client asked for — clients rely on that
	// invariant to render column headers with a "0" badge.
	byStatus := make(map[string]BoardBucket, len(statuses))
	for _, s := range statuses {
		byStatus[s] = BoardBucket{Issues: []IssueResponse{}, Total: 0}
	}

	for _, iss := range issues {
		bucket, ok := byStatus[iss.Status]
		if !ok {
			// Defensive: the SQL only returns rows whose status was in the
			// input array, so this branch is unreachable in practice.
			continue
		}
		resp := boardIssueRowToResponse(iss, prefix)
		labels := labelsMap[resp.ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp.Labels = &labels
		bucket.Issues = append(bucket.Issues, resp)
		byStatus[iss.Status] = bucket
	}
	for _, row := range counts {
		bucket, ok := byStatus[row.Status]
		if !ok {
			continue
		}
		bucket.Total = row.Total
		byStatus[row.Status] = bucket
	}

	writeJSON(w, http.StatusOK, BoardIssuesResponse{ByStatus: byStatus})
}

// parseBoardStatuses splits a comma-separated statuses list and validates
// non-empty + within-cap. Returns (nil, false) with the appropriate 400
// already written on w when validation fails.
func parseBoardStatuses(w http.ResponseWriter, raw string) ([]string, bool) {
	if raw == "" {
		writeError(w, http.StatusBadRequest, "statuses is required (comma-separated)")
		return nil, false
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		writeError(w, http.StatusBadRequest, "statuses must contain at least one value")
		return nil, false
	}
	if len(out) > boardMaxStatuses {
		writeError(w, http.StatusBadRequest, "too many statuses (max 20)")
		return nil, false
	}
	return out, true
}

// boardIssueRowToResponse converts a ListIssuesBoardRow to the shared
// IssueResponse. Separate from issueListRowToResponse because sqlc
// generates a distinct row type per query — the field layout is identical
// but the types are not assignable in Go.
func boardIssueRowToResponse(i db.ListIssuesBoardRow, issuePrefix string) IssueResponse {
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		Number:        i.Number,
		Identifier:    identifier,
		Title:         i.Title,
		Description:   textToPtr(i.Description),
		Status:        i.Status,
		Priority:      i.Priority,
		AssigneeType:  textToPtr(i.AssigneeType),
		AssigneeID:    uuidToPtr(i.AssigneeID),
		CreatorType:   i.CreatorType,
		CreatorID:     uuidToString(i.CreatorID),
		ParentIssueID: uuidToPtr(i.ParentIssueID),
		ProjectID:     uuidToPtr(i.ProjectID),
		Position:      i.Position,
		DueDate:       timestampToPtr(i.DueDate),
		CreatedAt:     timestampToString(i.CreatedAt),
		UpdatedAt:     timestampToString(i.UpdatedAt),
	}
}
