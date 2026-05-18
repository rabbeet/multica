package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-177: explicit API for per-(issue, skill) state used by the Inbox
// phase + last-applied chips and the SkillHistory panel on the issue
// detail page.
//
// See plans://Multica/2026-05-18-pul-177-inbox-skill-progress-indicators.md.
//
// Source-of-truth for what slugs are valid is the workspace skill
// registry (skill table from migration 008). Auto-detect lives in
// comment.go; this file is the explicit API surface.

// Slug shape mirrors the CHECK constraint on issue_skill_state.skill_slug.
// Validating at the handler layer too keeps the error response shape
// consistent (400 invalid_skill_slug instead of a Postgres CHECK
// violation surfacing as 500).
var skillSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// validSkillStatuses lists the two states the v1 state machine
// recognises. PUL-182 (TTL cleanup) may add 'stale' later.
var validSkillStatuses = map[string]struct{}{
	"in_progress": {},
	"done":        {},
}

// SkillStateResponse is the wire shape returned by all skill-state
// endpoints. Matches the SkillState type in
// packages/core/types/inbox.ts so the frontend can deserialize
// without per-field mapping.
type SkillStateResponse struct {
	Skill       string  `json:"skill"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func skillStateFromModel(row db.IssueSkillState) SkillStateResponse {
	return SkillStateResponse{
		Skill:       row.SkillSlug,
		Status:      row.Status,
		StartedAt:   timestampToString(row.StartedAt),
		CompletedAt: timestampToPtr(row.CompletedAt),
		UpdatedAt:   timestampToString(row.UpdatedAt),
	}
}

func skillStateFromListRow(row db.ListIssueSkillStatesRow) SkillStateResponse {
	return SkillStateResponse{
		Skill:       row.SkillSlug,
		Status:      row.Status,
		StartedAt:   timestampToString(row.StartedAt),
		CompletedAt: timestampToPtr(row.CompletedAt),
		UpdatedAt:   timestampToString(row.UpdatedAt),
	}
}

// PostSkillState — explicit setter.
//
//	POST /api/issues/:id/skill-state
//	body: {"skill": "office-hours", "status": "in_progress"|"done"}
//
// 200 returns the resulting row. Reused by both the multica CLI
// (`multica issue skill mark`, separate PR) and any gstack-side
// integration hook in the user's claude-code skills.
func (h *Handler) PostSkillState(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	var req struct {
		Skill  string `json:"skill"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !skillSlugRe.MatchString(req.Skill) {
		writeError(w, http.StatusBadRequest, "invalid skill slug")
		return
	}
	if _, ok := validSkillStatuses[req.Status]; !ok {
		writeError(w, http.StatusBadRequest, "invalid status (expected in_progress or done)")
		return
	}

	row, err := h.Queries.UpsertIssueSkillState(r.Context(), db.UpsertIssueSkillStateParams{
		IssueID:   issue.ID,
		SkillSlug: req.Skill,
		Status:    req.Status,
		Source:    "api",
	})
	if err != nil {
		slog.Warn("upsert issue skill state failed",
			append(logger.RequestAttrs(r), "error", err, "issue_id", issueID, "skill", req.Skill)...)
		writeError(w, http.StatusInternalServerError, "failed to upsert skill state")
		return
	}

	writeJSON(w, http.StatusOK, skillStateFromModel(row))
}

// ListIssueSkillStates — full history of skills applied to one
// issue. Backs the SkillHistory panel.
//
//	GET /api/issues/:id/skill-states
//	-> [SkillStateResponse, ...]  (ordered by updated_at DESC)
func (h *Handler) ListIssueSkillStates(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	rows, err := h.Queries.ListIssueSkillStates(r.Context(), issue.ID)
	if err != nil {
		slog.Warn("list issue skill states failed",
			append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to list skill states")
		return
	}

	resp := make([]SkillStateResponse, len(rows))
	for i, row := range rows {
		resp[i] = skillStateFromListRow(row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// DeleteSkillState — manual cleanup for debugging or explicit "this
// chip was wrong, remove it" flow.
//
//	DELETE /api/issues/:id/skill-state?skill=office-hours
func (h *Handler) DeleteSkillState(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	slug := r.URL.Query().Get("skill")
	if !skillSlugRe.MatchString(slug) {
		writeError(w, http.StatusBadRequest, "invalid skill slug")
		return
	}

	if err := h.Queries.DeleteIssueSkillState(r.Context(), db.DeleteIssueSkillStateParams{
		IssueID:   issue.ID,
		SkillSlug: slug,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("delete issue skill state failed",
			append(logger.RequestAttrs(r), "error", err, "issue_id", issueID, "skill", slug)...)
		writeError(w, http.StatusInternalServerError, "failed to delete skill state")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
