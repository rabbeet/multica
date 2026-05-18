package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-177 skill_state endpoints + comment auto-detect integration.
// These tests require a reachable database (handled by TestMain's
// pool.Ping skip path); they are otherwise normal handler tests.

// createSkillStateIssue is the skill-state equivalent of
// createIssueForTimeline — it spins up a throwaway issue and registers
// a cleanup that strips both the issue and its skill_state rows.
func createSkillStateIssue(t *testing.T, title string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  title,
		"status": "todo",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	json.NewDecoder(w.Body).Decode(&issue)
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM issue_skill_state WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM activity_log WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue.ID
}

// registerSkill seeds a row in the workspace skill registry so the
// auto-detect path has something to match against. Returns the
// skill id and registers a cleanup.
func registerSkill(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	var skillID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO skill (workspace_id, name)
		VALUES ($1, $2)
		RETURNING id
	`, testWorkspaceID, name).Scan(&skillID); err != nil {
		t.Fatalf("registerSkill(%q): %v", name, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM skill WHERE id = $1`, skillID)
	})
	return skillID
}

// postSkillState wraps the POST /skill-state endpoint with the
// chi route param the handler reads via chi.URLParam.
func postSkillState(t *testing.T, issueID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/skill-state", body)
	req = withURLParam(req, "id", issueID)
	testHandler.PostSkillState(w, req)
	return w
}

func listSkillStates(t *testing.T, issueID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+issueID+"/skill-states", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.ListIssueSkillStates(w, req)
	return w
}

func TestPostSkillState_HappyPath(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test PostSkillState happy")

	w := postSkillState(t, issueID, map[string]any{
		"skill":  "office-hours",
		"status": "in_progress",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PostSkillState: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SkillStateResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Skill != "office-hours" || resp.Status != "in_progress" {
		t.Fatalf("PostSkillState: unexpected payload: %+v", resp)
	}
	if resp.CompletedAt != nil {
		t.Fatalf("in_progress should leave completed_at NULL, got %v", *resp.CompletedAt)
	}
}

func TestPostSkillState_DoneSetsCompletedAt(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test PostSkillState done")
	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "in_progress"})

	w := postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "done"})
	if w.Code != http.StatusOK {
		t.Fatalf("PostSkillState done: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SkillStateResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "done" {
		t.Fatalf("expected status=done, got %s", resp.Status)
	}
	if resp.CompletedAt == nil || *resp.CompletedAt == "" {
		t.Fatalf("done should set completed_at; got nil")
	}
}

func TestPostSkillState_InvalidSlug(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test PostSkillState invalid slug")

	cases := []string{"/office-hours", "Office-Hours", "", "office hours", "слаг", "-leading-dash"}
	for _, slug := range cases {
		w := postSkillState(t, issueID, map[string]any{"skill": slug, "status": "in_progress"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for slug %q, got %d: %s", slug, w.Code, w.Body.String())
		}
	}
}

func TestPostSkillState_InvalidStatus(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test PostSkillState invalid status")

	cases := []string{"failed", "stale", "", "DONE"}
	for _, st := range cases {
		w := postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": st})
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for status %q, got %d: %s", st, w.Code, w.Body.String())
		}
	}
}

func TestListIssueSkillStates_OrderedDescByUpdatedAt(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test ListIssueSkillStates ordering")

	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "in_progress"})
	postSkillState(t, issueID, map[string]any{"skill": "plan-eng-review", "status": "in_progress"})
	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "done"})

	w := listSkillStates(t, issueID)
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueSkillStates: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []SkillStateResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 2 {
		t.Fatalf("expected 2 rows (office-hours upserted in place), got %d", len(resp))
	}
	// office-hours just got updated (status=done) → it should be first.
	if resp[0].Skill != "office-hours" {
		t.Fatalf("expected office-hours first (latest updated_at), got %s", resp[0].Skill)
	}
	if resp[0].Status != "done" {
		t.Fatalf("expected office-hours status=done, got %s", resp[0].Status)
	}
}

func TestDeleteSkillState(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test DeleteSkillState")
	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "in_progress"})

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/issues/"+issueID+"/skill-state?skill=office-hours", nil)
	req = withURLParam(req, "id", issueID)
	testHandler.DeleteSkillState(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteSkillState: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	listW := listSkillStates(t, issueID)
	var resp []SkillStateResponse
	json.NewDecoder(listW.Body).Decode(&resp)
	if len(resp) != 0 {
		t.Fatalf("expected empty list after delete, got %d rows", len(resp))
	}
}

// TestCommentAutoDetect_FullChain is the API integration test
// promised by /plan-eng-review decision 6C: end-to-end coverage of
// the comment → skill_state row write path without spinning up
// Playwright. Walks the same chain a real client would —
// CreateComment writes the row, ListIssueSkillStates reads it back.
func TestCommentAutoDetect_FullChain(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test auto-detect full chain")
	registerSkill(t, "office-hours")
	registerSkill(t, "qa")

	// Comment that mentions one registered priority skill, one
	// registered dynamic skill, and one slug that isn't registered.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "starting /office-hours and /qa later, /randomstuff ignored",
		"type":    "comment",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Read back via the SkillHistory endpoint and confirm only the
	// two registered skills made it through, both as in_progress.
	listW := listSkillStates(t, issueID)
	var resp []SkillStateResponse
	json.NewDecoder(listW.Body).Decode(&resp)
	if len(resp) != 2 {
		t.Fatalf("expected 2 registered-and-matched skills, got %d: %+v", len(resp), resp)
	}
	skills := map[string]string{}
	for _, s := range resp {
		skills[s.Skill] = s.Status
	}
	if skills["office-hours"] != "in_progress" {
		t.Errorf("expected office-hours in_progress, got %q", skills["office-hours"])
	}
	if skills["qa"] != "in_progress" {
		t.Errorf("expected qa in_progress, got %q", skills["qa"])
	}
	if _, leaked := skills["randomstuff"]; leaked {
		t.Errorf("randomstuff should be filtered by registry, but appeared")
	}
}

// TestCommentAutoDetect_DoesNotOverwriteDone covers the D3 rule:
// an auto-detected `/skill` mention in a later comment must not
// rewind a finished skill back to in_progress.
func TestCommentAutoDetect_DoesNotOverwriteDone(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test auto-detect preserves done")
	registerSkill(t, "office-hours")

	// Mark office-hours done explicitly via API.
	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "in_progress"})
	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "done"})

	// Post a comment that mentions /office-hours again.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "/office-hours mentioned again",
		"type":    "comment",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Expectation: row is still done — comment_auto path is gated.
	listW := listSkillStates(t, issueID)
	var resp []SkillStateResponse
	json.NewDecoder(listW.Body).Decode(&resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 row, got %d", len(resp))
	}
	if resp[0].Status != "done" {
		t.Errorf("expected office-hours to remain done, got %s", resp[0].Status)
	}
}

// TestCommentAutoDetect_UnregisteredSlugIgnored covers D9: slug
// candidates that don't match the workspace skill registry are
// silently dropped (no chip noise from random /foo mentions).
func TestCommentAutoDetect_UnregisteredSlugIgnored(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test auto-detect unregistered")
	// No skills registered for this workspace path-of-this-test.

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "/some-random-slug here",
		"type":    "comment",
	})
	req = withURLParam(req, "id", issueID)
	testHandler.CreateComment(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	listW := listSkillStates(t, issueID)
	var resp []SkillStateResponse
	json.NewDecoder(listW.Body).Decode(&resp)
	if len(resp) != 0 {
		t.Errorf("expected no skill_state rows for unregistered slug, got %d: %+v", len(resp), resp)
	}
}

// TestInboxIncludesPhaseAndLatestSkill is the regression test
// flagged by /plan-eng-review as CRITICAL: the LATERAL/CTE
// extension to ListInboxItems must not break the existing
// issue_status enrichment, and the new phase + latest_skill
// fields must populate as expected.
func TestInboxIncludesPhaseAndLatestSkill(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := createSkillStateIssue(t, "test inbox includes phase+latest")

	// Move the issue into "in_progress" so phase derivation has
	// something interesting to report (and we can assert it's
	// "coding", not the default "backlog").
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("update issue status: %v", err)
	}

	// Seed an inbox row for testUser/issue so ListInbox returns
	// something. (Production code creates these via the comment/event
	// pipeline; in tests we insert directly to keep the setup
	// surface narrow.)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title)
		VALUES ($1, 'member', $2, 'comment_added', 'info', $3, $4)
	`, testWorkspaceID, testUserID, issueID, fmt.Sprintf("Inbox seed for %s", issueID)); err != nil {
		t.Fatalf("insert inbox_item: %v", err)
	}

	postSkillState(t, issueID, map[string]any{"skill": "office-hours", "status": "in_progress"})

	// ListInbox reads workspace_id from request context (set by the
	// workspace middleware in real traffic); newRequest only sets the
	// X-Workspace-ID header, so we have to inject the context ourselves
	// to mirror what the middleware would have done.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/inbox", nil)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
	testHandler.ListInbox(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListInbox: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rows []InboxItemResponse
	json.NewDecoder(w.Body).Decode(&rows)

	var match *InboxItemResponse
	for i := range rows {
		if rows[i].IssueID != nil && *rows[i].IssueID == issueID {
			match = &rows[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("inbox row for issue %s missing from ListInbox response (%d rows total)", issueID, len(rows))
	}

	if match.Phase != "coding" {
		t.Errorf("expected phase=coding (from issue.status='in_progress'), got %q", match.Phase)
	}
	if match.IssueStatus == nil || *match.IssueStatus != "in_progress" {
		t.Errorf("regression: issue_status should still flow through (CTE didn't break the join), got %v", match.IssueStatus)
	}
	if match.LatestSkill == nil {
		t.Fatal("expected latest_skill populated, got nil")
	}
	if match.LatestSkill.Skill != "office-hours" || match.LatestSkill.Status != "in_progress" {
		t.Errorf("unexpected latest_skill: %+v", match.LatestSkill)
	}
}
