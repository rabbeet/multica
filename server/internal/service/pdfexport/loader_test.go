package pdfexport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loader_test.go covers LoadDocument with a hand-rolled fake of
// LoaderQueries. The goal is pure data-shape testing — does the
// loader pick up the timeline correctly, does thread mode prune to
// the right subtree, does the actor resolver fall back when an ID is
// missing — without requiring a live Postgres. Integration tests
// against a real DB live in the handler test (issue_export_test.go,
// follows the same skip-if-no-DB pattern as the rest of the handler
// suite).

// fakeQueries is a hand-rolled LoaderQueries that returns canned
// fixtures. Field tags map 1:1 to the LoaderQueries methods so the
// per-test "arrange" block reads like "set up table contents".
type fakeQueries struct {
	comments    []db.Comment
	activities  []db.ActivityLog
	reactions   []db.CommentReaction
	attachments []db.Attachment
	members     []db.ListMembersWithUserRow
	agents      []db.Agent
	projects    map[string]db.Project // key: uuidString(id)

	// errReactions, etc. let individual tests force the
	// "non-fatal best-effort fetch failed" branches.
	errReactions   error
	errAttachments error
}

func (f *fakeQueries) ListCommentsLatest(_ context.Context, _ db.ListCommentsLatestParams) ([]db.Comment, error) {
	return f.comments, nil
}
func (f *fakeQueries) ListActivitiesLatest(_ context.Context, _ db.ListActivitiesLatestParams) ([]db.ActivityLog, error) {
	return f.activities, nil
}
func (f *fakeQueries) ListReactionsByCommentIDs(_ context.Context, _ []pgtype.UUID) ([]db.CommentReaction, error) {
	if f.errReactions != nil {
		return nil, f.errReactions
	}
	return f.reactions, nil
}
func (f *fakeQueries) ListAttachmentsByCommentIDs(_ context.Context, _ db.ListAttachmentsByCommentIDsParams) ([]db.Attachment, error) {
	if f.errAttachments != nil {
		return nil, f.errAttachments
	}
	return f.attachments, nil
}
func (f *fakeQueries) ListMembersWithUser(_ context.Context, _ pgtype.UUID) ([]db.ListMembersWithUserRow, error) {
	return f.members, nil
}
func (f *fakeQueries) ListAgents(_ context.Context, _ pgtype.UUID) ([]db.Agent, error) {
	return f.agents, nil
}
func (f *fakeQueries) GetProject(_ context.Context, id pgtype.UUID) (db.Project, error) {
	if p, ok := f.projects[uuidString(id)]; ok {
		return p, nil
	}
	return db.Project{}, errors.New("project not found")
}

// ------------------------------------------------------------------
// Fixture helpers
// ------------------------------------------------------------------

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("scan uuid %q: %v", s, err)
	}
	return u
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func fixtureIssue(t *testing.T) db.Issue {
	t.Helper()
	return db.Issue{
		ID:          mustUUID(t, "9c18da81-dd3f-486a-98cc-a5020f1e91be"),
		WorkspaceID: mustUUID(t, "f00bf003-35e6-43fd-97f6-d7b3ca36bec9"),
		Number:      266,
		Title:       "Экспорт в PDF",
		Description: pgtype.Text{String: "**hello** from issue body", Valid: true},
		Status:      "in_progress",
		Priority:    "none",
		CreatorType: "member",
		CreatorID:   mustUUID(t, "a97695dd-8838-4828-b862-7497220cd6a4"),
		CreatedAt:   ts(time.Date(2026, 6, 2, 5, 30, 0, 0, time.UTC)),
		UpdatedAt:   ts(time.Date(2026, 6, 2, 5, 45, 0, 0, time.UTC)),
	}
}

// ------------------------------------------------------------------
// Tests
// ------------------------------------------------------------------

func TestLoadDocument_FullMode_ChronologicalOrder(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	t0 := time.Date(2026, 6, 2, 5, 0, 0, 0, time.UTC)

	f := &fakeQueries{
		comments: []db.Comment{
			// DB returns DESC by created_at; loader must reverse to ASC.
			{
				ID:          mustUUID(t, "00000000-0000-0000-0000-000000000003"),
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				AuthorType:  "member",
				AuthorID:    issue.CreatorID,
				Content:     "third",
				CreatedAt:   ts(t0.Add(20 * time.Minute)),
				UpdatedAt:   ts(t0.Add(20 * time.Minute)),
			},
			{
				ID:          mustUUID(t, "00000000-0000-0000-0000-000000000001"),
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				AuthorType:  "member",
				AuthorID:    issue.CreatorID,
				Content:     "first",
				CreatedAt:   ts(t0),
				UpdatedAt:   ts(t0),
			},
		},
		activities: []db.ActivityLog{
			{
				ID:          mustUUID(t, "00000000-0000-0000-0000-0000000000aa"),
				WorkspaceID: issue.WorkspaceID,
				IssueID:     issue.ID,
				ActorType:   pgtype.Text{String: "member", Valid: true},
				ActorID:     issue.CreatorID,
				Action:      "status_changed",
				CreatedAt:   ts(t0.Add(10 * time.Minute)),
			},
		},
		members: []db.ListMembersWithUserRow{
			{
				ID:       mustUUID(t, "55555555-5555-5555-5555-555555555555"),
				UserID:   issue.CreatorID,
				UserName: "Vadim",
			},
		},
	}

	doc, err := LoadDocument(context.Background(), f, issue, ModeFull, "")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Mode != ModeFull {
		t.Errorf("Mode: got %v, want ModeFull", doc.Mode)
	}
	if doc.Header.Title != "Экспорт в PDF" {
		t.Errorf("Header.Title: got %q", doc.Header.Title)
	}
	if doc.Header.CreatorName != "Vadim" {
		t.Errorf("CreatorName resolver: got %q, want Vadim", doc.Header.CreatorName)
	}
	if doc.Description != "**hello** from issue body" {
		t.Errorf("Description: got %q", doc.Description)
	}

	// 2 comments + 1 activity = 3 items, ASC by created_at.
	if got, want := len(doc.Items), 3; got != want {
		t.Fatalf("Items count: got %d, want %d", got, want)
	}
	first, _ := doc.Items[0].(CommentItem)
	if first.Body != "first" {
		t.Errorf("Items[0]: got %+v, want first comment", first)
	}
	second, ok := doc.Items[1].(ActivityItem)
	if !ok || second.Action != "Status Changed" {
		t.Errorf("Items[1]: got %+v, want activity in middle", doc.Items[1])
	}
	third, _ := doc.Items[2].(CommentItem)
	if third.Body != "third" {
		t.Errorf("Items[2]: got %+v, want third comment", third)
	}
}

func TestLoadDocument_ThreadMode_PrunesToSubtree(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	t0 := time.Date(2026, 6, 2, 5, 0, 0, 0, time.UTC)

	rootID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	replyID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	otherID := mustUUID(t, "33333333-3333-3333-3333-333333333333")

	f := &fakeQueries{
		comments: []db.Comment{
			// Top-level NOT in the thread — must be dropped.
			{
				ID:          otherID,
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				AuthorType:  "member",
				AuthorID:    issue.CreatorID,
				Content:     "unrelated top-level",
				CreatedAt:   ts(t0.Add(time.Minute)),
				UpdatedAt:   ts(t0.Add(time.Minute)),
			},
			{
				ID:          replyID,
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				AuthorType:  "member",
				AuthorID:    issue.CreatorID,
				Content:     "reply to root",
				ParentID:    rootID,
				CreatedAt:   ts(t0.Add(2 * time.Minute)),
				UpdatedAt:   ts(t0.Add(2 * time.Minute)),
			},
			{
				ID:          rootID,
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				AuthorType:  "member",
				AuthorID:    issue.CreatorID,
				Content:     "thread root",
				CreatedAt:   ts(t0),
				UpdatedAt:   ts(t0),
			},
		},
		// Even though activities exist, thread mode must suppress them.
		activities: []db.ActivityLog{
			{
				ID:        mustUUID(t, "44444444-4444-4444-4444-444444444444"),
				IssueID:   issue.ID,
				ActorID:   issue.CreatorID,
				Action:    "status_changed",
				CreatedAt: ts(t0.Add(90 * time.Second)),
			},
		},
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
	}

	doc, err := LoadDocument(context.Background(), f, issue, ModeThread, uuidString(rootID))
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Mode != ModeThread {
		t.Errorf("Mode: got %v, want ModeThread", doc.Mode)
	}
	if doc.ThreadRootID != uuidString(rootID) {
		t.Errorf("ThreadRootID: got %q", doc.ThreadRootID)
	}
	if got, want := len(doc.Items), 2; got != want {
		t.Fatalf("Items count: got %d, want %d (root + reply, no activity, no unrelated)", got, want)
	}
	root, ok := doc.Items[0].(CommentItem)
	if !ok || root.Body != "thread root" {
		t.Errorf("Items[0]: got %+v, want root comment first", doc.Items[0])
	}
	if root.IndentLevel != 0 {
		t.Errorf("root IndentLevel: got %d, want 0", root.IndentLevel)
	}
	reply, ok := doc.Items[1].(CommentItem)
	if !ok || reply.Body != "reply to root" {
		t.Errorf("Items[1]: got %+v, want reply", doc.Items[1])
	}
	if reply.IndentLevel != 1 {
		t.Errorf("reply IndentLevel: got %d, want 1", reply.IndentLevel)
	}
	// Activities suppressed.
	for i, it := range doc.Items {
		if _, isActivity := it.(ActivityItem); isActivity {
			t.Errorf("Items[%d] should not be ActivityItem in thread mode", i)
		}
	}
	// Unrelated top-level filtered.
	for i, it := range doc.Items {
		if c, ok := it.(CommentItem); ok && c.Body == "unrelated top-level" {
			t.Errorf("Items[%d] leaked unrelated top-level comment into thread", i)
		}
	}
}

func TestLoadDocument_ThreadMode_RootMissingReturns404(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	f := &fakeQueries{
		comments: []db.Comment{}, // no comments at all
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
	}
	_, err := LoadDocument(context.Background(), f, issue, ModeThread,
		"11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, ErrThreadRootNotFound) {
		t.Errorf("want ErrThreadRootNotFound, got %v", err)
	}
}

func TestLoadDocument_ThreadMode_InvalidUUIDReturns400(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	f := &fakeQueries{
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
	}
	_, err := LoadDocument(context.Background(), f, issue, ModeThread, "not-a-uuid")
	if !errors.Is(err, ErrInvalidThreadRoot) {
		t.Errorf("want ErrInvalidThreadRoot, got %v", err)
	}
}

func TestLoadDocument_ProjectTitlePopulated(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	projID := mustUUID(t, "2f75645a-6f89-4a85-a86b-a283a52bf74e")
	issue.ProjectID = projID

	f := &fakeQueries{
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
		projects: map[string]db.Project{
			uuidString(projID): {ID: projID, Title: "Multica"},
		},
	}
	doc, err := LoadDocument(context.Background(), f, issue, ModeFull, "")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Header.ProjectTitle != "Multica" {
		t.Errorf("ProjectTitle: got %q, want Multica", doc.Header.ProjectTitle)
	}
}

func TestLoadDocument_FullModeWithThreadIDIsAnError(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	f := &fakeQueries{}
	_, err := LoadDocument(context.Background(), f, issue, ModeFull, "11111111-1111-1111-1111-111111111111")
	if err == nil {
		t.Fatal("want error when ModeFull is paired with a non-empty threadRootID")
	}
}

func TestLoadDocument_AssigneeNameResolved(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	agentID := mustUUID(t, "15a64543-daee-49f2-861c-b3ec121c9d7e")
	issue.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	issue.AssigneeID = agentID
	f := &fakeQueries{
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
		agents: []db.Agent{
			{ID: agentID, Name: "agent-1"},
		},
	}
	doc, err := LoadDocument(context.Background(), f, issue, ModeFull, "")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Header.AssigneeName != "agent-1" {
		t.Errorf("AssigneeName: got %q, want agent-1", doc.Header.AssigneeName)
	}
}

func TestLoadDocument_UnknownActorFallsBackToShortID(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	// Creator ID won't be in members or agents list — resolver should
	// fall back to the first 8 chars of the UUID rather than ""ing.
	f := &fakeQueries{}
	doc, err := LoadDocument(context.Background(), f, issue, ModeFull, "")
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Header.CreatorName == "" {
		t.Error("CreatorName fallback should produce a non-empty string")
	}
	if len(doc.Header.CreatorName) != 8 {
		t.Errorf("CreatorName fallback should be 8-char prefix, got %q", doc.Header.CreatorName)
	}
}

func TestLoadDocument_FailedReactionsFetchDoesNotFailExport(t *testing.T) {
	t.Parallel()
	issue := fixtureIssue(t)
	t0 := time.Date(2026, 6, 2, 5, 0, 0, 0, time.UTC)
	f := &fakeQueries{
		comments: []db.Comment{{
			ID:          mustUUID(t, "00000000-0000-0000-0000-000000000001"),
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			AuthorType:  "member",
			AuthorID:    issue.CreatorID,
			Content:     "still useful",
			CreatedAt:   ts(t0),
			UpdatedAt:   ts(t0),
		}},
		errReactions: errors.New("simulated reactions query failure"),
		members: []db.ListMembersWithUserRow{
			{UserID: issue.CreatorID, UserName: "Vadim"},
		},
	}
	doc, err := LoadDocument(context.Background(), f, issue, ModeFull, "")
	if err != nil {
		t.Fatalf("LoadDocument should succeed despite reactions failure: %v", err)
	}
	if len(doc.Items) != 1 {
		t.Fatalf("Items: got %d, want 1", len(doc.Items))
	}
	c, _ := doc.Items[0].(CommentItem)
	if len(c.Reactions) != 0 {
		t.Errorf("Reactions should be empty when fetch failed, got %d", len(c.Reactions))
	}
}
