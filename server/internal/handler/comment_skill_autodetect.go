package handler

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-177: auto-detect /<skill-name> tokens in comment content and
// upsert `issue_skill_state` rows. See
// plans://Multica/2026-05-18-pul-177-inbox-skill-progress-indicators.md.
//
// Two-stage pipeline:
//
//  1. extractSkillCandidates — pure regex, no DB. Pulls every plausible
//     /<slug> token out of the content. Cheap; can run inline.
//
//  2. inferSkillStatesFromComment — filters candidates against the
//     workspace skill registry (skill table from migration 008). Only
//     registered slugs survive. Per D9 (/plan-eng-review v2 plan):
//     unknown slugs are silently dropped — we don't want false-positive
//     chips from `/agree`, `/me`, etc. polluting the Inbox row.
//
// Known limitations carried over from the plan as PUL-181 follow-up:
//
//  - The regex matches inside fenced code blocks (```) and inline
//    backtick spans. A user pasting "`/office-hours`" gets a chip.
//  - The regex matches inside markdown blockquotes ("> /skill"). A
//    user quoting another comment also gets a chip.
//
// Both are accepted as v1 noise. PUL-181 swaps the regex for a real
// markdown-aware parser when the false-positive rate becomes a
// problem.

// skillCandidateRe matches /<slug> tokens that appear at the start of
// a line or after whitespace. Multiline mode lets ^ match each line.
//
// Slug shape: must start with [a-z0-9], up to 64 chars of [a-z0-9-].
// This matches the CHECK constraint on issue_skill_state.skill_slug,
// so anything the regex extracts is safe to pass to the upsert.
var skillCandidateRe = regexp.MustCompile(`(?m)(?:^|\s)/([a-z0-9][a-z0-9-]{0,63})\b`)

// extractSkillCandidates returns the deduplicated set of slug tokens
// found in `content`. Empty slice when nothing matches.
func extractSkillCandidates(content string) []string {
	if content == "" {
		return nil
	}
	matches := skillCandidateRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		slug := m[1]
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out
}

// inferSkillStatesFromComment returns the slugs from `content` that
// are also registered as skills in workspace `wsID`. Soft-fails on
// any registry-side error — auto-detect is a nice-to-have and must
// never block a comment write.
func (h *Handler) inferSkillStatesFromComment(ctx context.Context, wsID pgtype.UUID, content string) []string {
	candidates := extractSkillCandidates(content)
	if len(candidates) == 0 {
		return nil
	}
	registered, err := h.Queries.ListWorkspaceSkillNames(ctx, wsID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("skill autodetect registry lookup failed", "error", err, "workspace_id", uuidToString(wsID))
		return nil
	}
	if len(registered) == 0 {
		return nil
	}
	registerSet := make(map[string]struct{}, len(registered))
	for _, name := range registered {
		registerSet[name] = struct{}{}
	}
	matched := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if _, ok := registerSet[c]; ok {
			matched = append(matched, c)
		}
	}
	return matched
}

// applyCommentSkillAutoDetect runs the full auto-detect pipeline and
// upserts in_progress rows. Per D3 (don't overwrite done from
// comment_auto), the upsert is gated: if the row already exists with
// status='done', we skip it so a `/skill` mention in a future comment
// doesn't rewind a completed skill back to in_progress.
//
// Called from CreateComment after the comment row has been
// persisted. Failures are logged and discarded — the comment is
// already written, the user has already seen the success response.
func (h *Handler) applyCommentSkillAutoDetect(ctx context.Context, r *commentAutoDetectInput) {
	slugs := h.inferSkillStatesFromComment(ctx, r.WorkspaceID, r.Content)
	for _, slug := range slugs {
		// Don't overwrite a done state from comment_auto. Explicit
		// API calls (POST /skill-state) can still flip done back to
		// in_progress; this restriction only applies to the
		// auto-detect path.
		existing, err := h.Queries.GetIssueSkillState(ctx, db.GetIssueSkillStateParams{
			IssueID:   r.IssueID,
			SkillSlug: slug,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("skill autodetect get existing failed",
				"error", err, "issue_id", uuidToString(r.IssueID), "skill", slug)
			continue
		}
		if err == nil && existing.Status == "done" {
			continue // preserve done — see comment above
		}

		if _, err := h.Queries.UpsertIssueSkillState(ctx, db.UpsertIssueSkillStateParams{
			IssueID:   r.IssueID,
			SkillSlug: slug,
			Status:    "in_progress",
			Source:    "comment_auto",
		}); err != nil {
			slog.Warn("skill autodetect upsert failed",
				"error", err, "issue_id", uuidToString(r.IssueID), "skill", slug)
		}
	}
}

type commentAutoDetectInput struct {
	WorkspaceID pgtype.UUID
	IssueID     pgtype.UUID
	Content     string
}
