package main

import (
	"context"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PUL-445 scheduler-tick tests. Mirrors the shape of
// skill_state_cleanup_scheduler_test.go — the underlying query is
// exercised in internal/handler/inbox_test.go
// (TestArchiveOldReadInbox_TTLBoundary); this file covers the thin
// wrapper that converts time.Duration → int64 seconds, clamps a
// too-small TTL, and swallows a cancelled-context tick without
// panicking.

// insertInboxRaw inserts a single inbox_item row with an explicit
// age (seconds since now, back-dated in created_at). Returns the row
// id and registers a cleanup that DELETEs the row after the test.
func insertInboxRaw(t *testing.T, workspaceID, recipientID string, read bool, ageSeconds int) string {
	t.Helper()
	var id string
	err := testPool.QueryRow(
		context.Background(),
		`INSERT INTO inbox_item
		     (workspace_id, recipient_type, recipient_id, type, title, read, archived, created_at)
		 VALUES ($1::uuid, 'member', $2::uuid, 'comment', 'PUL-445 tick fixture', $3, false, now() - make_interval(secs => $4::bigint))
		 RETURNING id`,
		workspaceID, recipientID, read, ageSeconds,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insertInboxRaw: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE id = $1::uuid`, id)
	})
	return id
}

func inboxRowArchived(t *testing.T, id string) bool {
	t.Helper()
	var archived bool
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT archived FROM inbox_item WHERE id = $1::uuid`,
		id,
	).Scan(&archived); err != nil {
		t.Fatalf("inboxRowArchived: %v", err)
	}
	return archived
}

func TestRunInboxCleanupTick_ArchivesOldRead(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)

	// Old + read → archive.
	oldRead := insertInboxRaw(t, testWorkspaceID, testUserID, true, 25*3600)
	// Old + unread → skip.
	oldUnread := insertInboxRaw(t, testWorkspaceID, testUserID, false, 25*3600)
	// Fresh + read → skip.
	freshRead := insertInboxRaw(t, testWorkspaceID, testUserID, true, 1*3600)

	runInboxCleanupTick(context.Background(), queries, 24*time.Hour)

	if !inboxRowArchived(t, oldRead) {
		t.Errorf("old-read row was NOT archived; expected sweep to catch it")
	}
	if inboxRowArchived(t, oldUnread) {
		t.Errorf("old-unread row was archived; sweep must skip unread rows")
	}
	if inboxRowArchived(t, freshRead) {
		t.Errorf("fresh-read row was archived; sweep must skip rows inside TTL")
	}
}

func TestRunInboxCleanupTick_CancelledContextNoPanic(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Cancelled-context tick should return quickly without panicking or
	// Warn-logging (the code path treats context.Canceled as a shutdown
	// signal, not a failure).
	runInboxCleanupTick(ctx, queries, 24*time.Hour)
}

func TestRunInboxCleanupScheduler_ClampsTinyTTL(t *testing.T) {
	if testPool == nil {
		t.Skip("no DB")
	}
	queries := db.New(testPool)

	// A too-small TTL would otherwise archive every read row on every
	// tick — a retention amplifier. Seed a row that is 5s old and read,
	// then call the scheduler with a 500ms TTL and an immediately-
	// cancelled context. The scheduler must clamp the TTL up to the
	// 14-day default before the first tick — so the 5s-old row must
	// survive the immediate startup sweep.
	rowID := insertInboxRaw(t, testWorkspaceID, testUserID, true, 5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runInboxCleanupScheduler(ctx, queries, 500*time.Millisecond)

	if inboxRowArchived(t, rowID) {
		t.Errorf("row was archived under a tiny TTL; scheduler must clamp TTL to the 14d default before sweeping")
	}
}
