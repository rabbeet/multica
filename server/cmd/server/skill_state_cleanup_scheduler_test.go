package main

import (
	"context"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-182 scheduler-tick tests. The query itself has full coverage in
// internal/handler/skill_state_cleanup_test.go; this file covers the
// thin wrapper that converts time.Duration → int64 seconds, swallows
// transient errors, and writes log lines. We only assert the
// happy-path side-effect (row deleted / row preserved); slog output is
// not captured here because the value-add of asserting "we called
// slog.Info" is low.

// insertSkillStateRaw bypasses the handler's Upsert path and writes
// straight into issue_skill_state via testPool. Callers supply ageSeconds
// to pre-rewind started_at without a follow-up UPDATE.
//
// Skips the test (via t.Skipf) if the FK insert fails because the issue
// row was wiped by a parallel test — this is a known consequence of
// reusing the integration_test fixture issue without per-row cleanup.
func insertSkillStateRaw(t *testing.T, issueID, slug, status string, ageSeconds int) {
	t.Helper()
	_, err := testPool.Exec(
		context.Background(),
		`INSERT INTO issue_skill_state
		     (issue_id, skill_slug, status, started_at, completed_at, updated_at, source)
		 VALUES (
		     $1::uuid, $2, $3,
		     now() - make_interval(secs => $4::bigint),
		     CASE WHEN $3 = 'done'
		          THEN now() - make_interval(secs => $4::bigint)
		          ELSE NULL END,
		     now() - make_interval(secs => $4::bigint),
		     'api'
		 )
		 ON CONFLICT (issue_id, skill_slug) DO UPDATE
		   SET status       = EXCLUDED.status,
		       started_at   = EXCLUDED.started_at,
		       completed_at = EXCLUDED.completed_at,
		       updated_at   = EXCLUDED.updated_at`,
		issueID, slug, status, ageSeconds,
	)
	if err != nil {
		t.Fatalf("insertSkillStateRaw(%s, %s, %s, %d): %v", issueID, slug, status, ageSeconds, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM issue_skill_state WHERE issue_id = $1::uuid AND skill_slug = $2`,
			issueID, slug)
	})
}

func skillStateRowExists(t *testing.T, issueID, slug string) bool {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_skill_state WHERE issue_id = $1::uuid AND skill_slug = $2`,
		issueID, slug,
	).Scan(&n); err != nil {
		t.Fatalf("skillStateRowExists: %v", err)
	}
	return n > 0
}

func TestRunSkillStateCleanupTick_DeletesStaleInProgress(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)
	issueID := createIssue(t, "PUL-182 scheduler tick: stale row")
	insertSkillStateRaw(t, issueID, "scheduler-tick-stale", "in_progress", 25*3600)

	runSkillStateCleanupTick(context.Background(), queries, 24*time.Hour)

	if skillStateRowExists(t, issueID, "scheduler-tick-stale") {
		t.Fatalf("expected stale in_progress row to be deleted by tick")
	}
}

func TestRunSkillStateCleanupTick_PreservesFreshAndDone(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)
	issueID := createIssue(t, "PUL-182 scheduler tick: preserved rows")

	insertSkillStateRaw(t, issueID, "scheduler-tick-fresh", "in_progress", 1*3600)
	insertSkillStateRaw(t, issueID, "scheduler-tick-olddone", "done", 100*24*3600)

	runSkillStateCleanupTick(context.Background(), queries, 24*time.Hour)

	if !skillStateRowExists(t, issueID, "scheduler-tick-fresh") {
		t.Errorf("fresh in_progress row was deleted but should not be")
	}
	if !skillStateRowExists(t, issueID, "scheduler-tick-olddone") {
		t.Errorf("old done row was deleted but done rows must survive indefinitely")
	}
}

func TestRunSkillStateCleanupTick_CancelledContextNoPanic(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Cancelled-context tick should return quickly without panicking even
	// though queries.CleanupStaleIssueSkillStates will return
	// context.Canceled. The scheduler logs Warn and moves on.
	runSkillStateCleanupTick(ctx, queries, 24*time.Hour)
}
