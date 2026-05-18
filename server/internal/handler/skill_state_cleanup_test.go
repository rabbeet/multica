package handler

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// PUL-182 CleanupStaleIssueSkillStates query tests. Lives in the handler
// package so we can lean on testHandler.Queries and the per-issue cleanup
// fixture (createSkillStateIssue) from skill_state_test.go.
//
// The query is server-side: PG `now()` is the clock, not Go's. We backdate
// rows by UPDATE-ing started_at directly through testPool, which is the
// only safe way to simulate "this in_progress row sat for >24h" without
// actually waiting.

const (
	cleanupHour = 3600
	cleanupDay  = 24 * cleanupHour
)

// seedSkillStateAged inserts (or upserts) a row through the existing
// PostSkillState handler — so every CHECK constraint and Upsert branch
// PUL-177 wired up is exercised by the seed — then rewinds started_at
// (and updated_at, to keep ordering invariants stable) by ageSeconds.
//
// Returns the issue id. The caller is responsible for staying within
// the t.Cleanup blast radius of createSkillStateIssue: that hook strips
// issue_skill_state, comment, activity_log, inbox_item, and issue, in
// that order, so the seeded row never escapes the test.
func seedSkillStateAged(t *testing.T, slug, status string, ageSeconds int) string {
	t.Helper()
	issueID := createSkillStateIssue(t, "cleanup test "+slug+"/"+status)
	postSkillState(t, issueID, map[string]any{"skill": slug, "status": status})
	if _, err := testPool.Exec(
		context.Background(),
		`UPDATE issue_skill_state
		    SET started_at = now() - make_interval(secs => $3::bigint),
		        updated_at = now() - make_interval(secs => $3::bigint)
		  WHERE issue_id = $1::uuid AND skill_slug = $2`,
		issueID, slug, ageSeconds,
	); err != nil {
		t.Fatalf("seedSkillStateAged: rewind started_at: %v", err)
	}
	return issueID
}

// countSkillStateRows returns how many rows match (issueID, slug). The
// primary key on issue_skill_state guarantees at most one — anything
// other than 0 or 1 here is a test-bug.
func countSkillStateRows(t *testing.T, issueID, slug string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM issue_skill_state WHERE issue_id = $1::uuid AND skill_slug = $2`,
		issueID, slug,
	).Scan(&n); err != nil {
		t.Fatalf("countSkillStateRows: %v", err)
	}
	return n
}

func TestCleanupStaleIssueSkillStates_StaleInProgressDeleted(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := seedSkillStateAged(t, "office-hours", "in_progress", 25*cleanupHour)

	deleted, err := testHandler.Queries.CleanupStaleIssueSkillStates(
		context.Background(), int64(24*cleanupHour),
	)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// At least the row we just seeded should come back. Other phantom
	// rows from prior test runs may be present — we are not the only
	// writer in this DB.
	found := false
	for _, row := range deleted {
		if row.SkillSlug == "office-hours" && util.UUIDToString(row.IssueID) == issueID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Cleanup did not return seeded row for issue %s", issueID)
	}
	if got := countSkillStateRows(t, issueID, "office-hours"); got != 0 {
		t.Fatalf("expected row to be gone, got count=%d", got)
	}
}

func TestCleanupStaleIssueSkillStates_FreshInProgressPreserved(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	issueID := seedSkillStateAged(t, "qa", "in_progress", 1*cleanupHour)

	deleted, err := testHandler.Queries.CleanupStaleIssueSkillStates(
		context.Background(), int64(24*cleanupHour),
	)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, row := range deleted {
		if util.UUIDToString(row.IssueID) == issueID {
			t.Fatalf("fresh row should not be deleted, but Cleanup returned it: %+v", row)
		}
	}
	if got := countSkillStateRows(t, issueID, "qa"); got != 1 {
		t.Fatalf("expected fresh row to survive, got count=%d", got)
	}
}

func TestCleanupStaleIssueSkillStates_DoneNeverTouched(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	// 100-day-old done row: nominally older than any sane TTL.
	issueID := seedSkillStateAged(t, "ship", "done", 100*cleanupDay)

	deleted, err := testHandler.Queries.CleanupStaleIssueSkillStates(
		context.Background(), int64(24*cleanupHour),
	)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, row := range deleted {
		if util.UUIDToString(row.IssueID) == issueID {
			t.Fatalf("done row must never be deleted, got: %+v", row)
		}
	}
	if got := countSkillStateRows(t, issueID, "ship"); got != 1 {
		t.Fatalf("expected done row to survive, got count=%d", got)
	}
}

func TestCleanupStaleIssueSkillStates_MixedBatch(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	staleInProgress := seedSkillStateAged(t, "office-hours", "in_progress", 25*cleanupHour)
	freshInProgress := seedSkillStateAged(t, "qa", "in_progress", 1*cleanupHour)
	staleDone := seedSkillStateAged(t, "review", "done", 25*cleanupHour)
	freshDone := seedSkillStateAged(t, "ship", "done", 1*cleanupHour)

	deleted, err := testHandler.Queries.CleanupStaleIssueSkillStates(
		context.Background(), int64(24*cleanupHour),
	)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	matched := map[string]bool{}
	for _, row := range deleted {
		switch util.UUIDToString(row.IssueID) {
		case staleInProgress:
			matched["staleInProgress"] = true
		case freshInProgress:
			matched["freshInProgress"] = true
		case staleDone:
			matched["staleDone"] = true
		case freshDone:
			matched["freshDone"] = true
		}
	}

	if !matched["staleInProgress"] {
		t.Errorf("staleInProgress row was not deleted")
	}
	if matched["freshInProgress"] {
		t.Errorf("freshInProgress was deleted but should not be")
	}
	if matched["staleDone"] {
		t.Errorf("staleDone (done status, age > TTL) was deleted but done rows must survive")
	}
	if matched["freshDone"] {
		t.Errorf("freshDone was deleted but done rows must survive")
	}

	if got := countSkillStateRows(t, staleInProgress, "office-hours"); got != 0 {
		t.Errorf("staleInProgress: expected 0 rows, got %d", got)
	}
	if got := countSkillStateRows(t, freshInProgress, "qa"); got != 1 {
		t.Errorf("freshInProgress: expected 1 row, got %d", got)
	}
	if got := countSkillStateRows(t, staleDone, "review"); got != 1 {
		t.Errorf("staleDone: expected 1 row, got %d", got)
	}
	if got := countSkillStateRows(t, freshDone, "ship"); got != 1 {
		t.Errorf("freshDone: expected 1 row, got %d", got)
	}
}

// Documents the boundary: `started_at < now() - ttl` is strict-less-than,
// so a row whose age equals the TTL exactly survives. We seed two rows
// straddling the boundary in a single test so the relative ordering is
// stable regardless of how the wall clock drifts during the test.
func TestCleanupStaleIssueSkillStates_TTLBoundary(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	justOver := seedSkillStateAged(t, "qa", "in_progress", 24*cleanupHour+10)
	justUnder := seedSkillStateAged(t, "office-hours", "in_progress", 24*cleanupHour-10)

	deleted, err := testHandler.Queries.CleanupStaleIssueSkillStates(
		context.Background(), int64(24*cleanupHour),
	)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	overDeleted, underDeleted := false, false
	for _, row := range deleted {
		switch util.UUIDToString(row.IssueID) {
		case justOver:
			overDeleted = true
		case justUnder:
			underDeleted = true
		}
	}
	if !overDeleted {
		t.Errorf("row aged TTL+10s should be deleted")
	}
	if underDeleted {
		t.Errorf("row aged TTL-10s should NOT be deleted (strict < boundary)")
	}
}
