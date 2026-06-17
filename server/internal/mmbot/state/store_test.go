package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_AppliesSchemaAndUserVersion(t *testing.T) {
	s := newTestStore(t)
	var v int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
}

func TestOpen_RejectsFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := s.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	_ = s.Close()

	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("expected refusal on future-version database, got nil")
	}
}

func TestOpen_EmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty path, got nil")
	}
}

func TestThread_RecordAndLookupRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	thread := Thread{
		RootPostID:     "post-abc",
		ChannelID:      "chan-1",
		MulticaIssueID: "issue-uuid-1",
	}
	if err := s.RecordThread(ctx, thread); err != nil {
		t.Fatalf("record: %v", err)
	}

	byRoot, err := s.ThreadByRoot(ctx, "post-abc")
	if err != nil {
		t.Fatalf("by root: %v", err)
	}
	if byRoot.MulticaIssueID != "issue-uuid-1" {
		t.Errorf("by root: issue = %q, want issue-uuid-1", byRoot.MulticaIssueID)
	}
	if byRoot.LastSeenStatus != "" || byRoot.LastRenderTS != "" {
		t.Errorf("expected empty status/render cursors on fresh row, got %+v", byRoot)
	}

	byIssue, err := s.ThreadByIssue(ctx, "issue-uuid-1")
	if err != nil {
		t.Fatalf("by issue: %v", err)
	}
	if byIssue.RootPostID != "post-abc" {
		t.Errorf("by issue: root = %q, want post-abc", byIssue.RootPostID)
	}
}

func TestThread_RecordIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	thread := Thread{RootPostID: "p", ChannelID: "c", MulticaIssueID: "i"}
	if err := s.RecordThread(ctx, thread); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.RecordThread(ctx, thread); err != nil {
		t.Fatalf("second (same): %v", err)
	}
}

func TestThread_NotFoundReturnsSentinel(t *testing.T) {
	s := newTestStore(t)
	_, err := s.ThreadByRoot(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("by root: err = %v, want ErrNotFound", err)
	}
	_, err = s.ThreadByIssue(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("by issue: err = %v, want ErrNotFound", err)
	}
}

func TestThread_SetStatusAndActiveList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		thread := Thread{
			RootPostID:     "post-" + string(rune('a'+i)),
			ChannelID:      "c",
			MulticaIssueID: "issue-" + string(rune('a'+i)),
		}
		if err := s.RecordThread(ctx, thread); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	ids, err := s.ActiveIssueIDs(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("active count = %d, want 3", len(ids))
	}

	if err := s.SetThreadStatus(ctx, "issue-b", "waiting"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err := s.ThreadByIssue(ctx, "issue-b")
	if err != nil {
		t.Fatalf("lookup after status: %v", err)
	}
	if got.LastSeenStatus != "waiting" {
		t.Errorf("status = %q, want waiting", got.LastSeenStatus)
	}

	if err := s.SetThreadStatus(ctx, "missing-issue", "deployed"); !errors.Is(err, ErrNotFound) {
		t.Errorf("set status on missing: %v, want ErrNotFound", err)
	}
}

// Finding #1 of /plan-eng-review: idempotent screenshot rate-limit must work
// off a 60s window stamped on the thread. ShouldRender enforces it.
func TestRender_RateLimitWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordThread(ctx, Thread{RootPostID: "p", ChannelID: "c", MulticaIssueID: "issue-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	now := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)

	ok, err := s.ShouldRender(ctx, "issue-1", now, 60*time.Second)
	if err != nil {
		t.Fatalf("first should-render: %v", err)
	}
	if !ok {
		t.Fatal("first call: expected ShouldRender=true (no prior render)")
	}

	if err := s.MarkRendered(ctx, "issue-1", now); err != nil {
		t.Fatalf("mark: %v", err)
	}

	// Immediate retry: still inside the window.
	ok, _ = s.ShouldRender(ctx, "issue-1", now.Add(30*time.Second), 60*time.Second)
	if ok {
		t.Error("inside window: expected ShouldRender=false")
	}

	// Just past the window.
	ok, _ = s.ShouldRender(ctx, "issue-1", now.Add(61*time.Second), 60*time.Second)
	if !ok {
		t.Error("past window: expected ShouldRender=true")
	}
}

func TestRender_ShouldRender_MissingThread(t *testing.T) {
	s := newTestStore(t)
	_, err := s.ShouldRender(context.Background(), "missing", time.Now(), 60*time.Second)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing thread: err = %v, want ErrNotFound", err)
	}
}

func TestRender_CorruptCursorTreatedAsExpired(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordThread(ctx, Thread{RootPostID: "p", ChannelID: "c", MulticaIssueID: "issue-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE mm_threads SET last_render_ts = 'not-a-timestamp' WHERE multica_issue_id = ?`, "issue-1"); err != nil {
		t.Fatalf("corrupt cursor: %v", err)
	}
	ok, err := s.ShouldRender(ctx, "issue-1", time.Now(), 60*time.Second)
	if err != nil {
		t.Fatalf("should-render: %v", err)
	}
	if !ok {
		t.Error("corrupt cursor: expected ShouldRender=true (heal-on-next-write)")
	}
}

// Finding #7: outbound dedup persists in mm_synced_posts(direction=multica_to_mm)
// so the echo-loop filter doesn't depend solely on MM_BOT_USER_ID matching.
func TestSyncedPost_BothDirectionsAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.RecordSyncedPost(ctx, "mm-inbound-1", "multica-comment-X", DirectionMMToMulitca); err != nil {
		t.Fatalf("record inbound: %v", err)
	}
	if err := s.RecordSyncedPost(ctx, "mm-outbound-1", "", DirectionMulticaToMM); err != nil {
		t.Fatalf("record outbound: %v", err)
	}
	// Idempotency: second insert is silent no-op.
	if err := s.RecordSyncedPost(ctx, "mm-outbound-1", "", DirectionMulticaToMM); err != nil {
		t.Fatalf("record outbound (repeat): %v", err)
	}

	got, err := s.IsSyncedPost(ctx, "mm-outbound-1")
	if err != nil {
		t.Fatalf("is synced (outbound): %v", err)
	}
	if !got {
		t.Error("expected mm-outbound-1 to be marked synced")
	}

	got, err = s.IsSyncedPost(ctx, "mm-never-seen")
	if err != nil {
		t.Fatalf("is synced (missing): %v", err)
	}
	if got {
		t.Error("unrecorded post reported as synced")
	}
}

func TestSyncedPost_InvalidDirectionRejected(t *testing.T) {
	s := newTestStore(t)
	err := s.RecordSyncedPost(context.Background(), "mm-x", "", "weird-direction")
	if err == nil {
		t.Fatal("expected CHECK constraint to reject bad direction, got nil")
	}
}

func TestSyncedComment_RoundTripAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordSyncedComment(ctx, "mc-1", "mm-post-1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.RecordSyncedComment(ctx, "mc-1", "mm-post-2"); err != nil {
		t.Fatalf("second: %v", err)
	}
	got, err := s.IsSyncedComment(ctx, "mc-1")
	if err != nil {
		t.Fatalf("is synced: %v", err)
	}
	if !got {
		t.Error("expected mc-1 marked")
	}
}

// FIFO ordering matters: if MM was down for 4 hours and we queued 10 replies
// for the same thread, they must surface in the order Лина would expect.
func TestPending_FIFOAndAttemptCounter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := s.EnqueuePending(ctx, PendingPost{
			ChannelID:  "c",
			RootPostID: "thread-1",
			Message:    "msg-" + string(rune('A'+i)),
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	batch, err := s.NextPending(ctx, 3)
	if err != nil {
		t.Fatalf("next pending: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("batch size = %d, want 3", len(batch))
	}
	for i, p := range batch {
		want := "msg-" + string(rune('A'+i))
		if p.Message != want {
			t.Errorf("batch[%d].Message = %q, want %q (FIFO broken)", i, p.Message, want)
		}
	}

	// Bump attempts on the first row, verify it stays first (still id=1).
	if err := s.IncrementPendingAttempt(ctx, batch[0].ID, "transient failure"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	again, err := s.NextPending(ctx, 1)
	if err != nil {
		t.Fatalf("next pending after bump: %v", err)
	}
	if len(again) != 1 || again[0].ID != batch[0].ID {
		t.Errorf("after bump: got id %v, want %v", again[0].ID, batch[0].ID)
	}
	if again[0].Attempts != 1 || again[0].LastError != "transient failure" {
		t.Errorf("after bump: attempts=%d last_error=%q, want 1 / 'transient failure'", again[0].Attempts, again[0].LastError)
	}

	// Delete the head, the new head is the second message.
	if err := s.DeletePending(ctx, batch[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	head, err := s.NextPending(ctx, 1)
	if err != nil {
		t.Fatalf("next pending after delete: %v", err)
	}
	if head[0].Message != "msg-B" {
		t.Errorf("after delete: head = %q, want msg-B", head[0].Message)
	}
}

func TestMeta_RoundTripAndUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	v, err := s.GetMeta(ctx, MetaLastSeenMMTS)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if v != "" {
		t.Errorf("empty get returned %q, want \"\"", v)
	}

	if err := s.SetMeta(ctx, MetaLastSeenMMTS, "2026-06-17T03:00:00Z"); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err = s.GetMeta(ctx, MetaLastSeenMMTS)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if v != "2026-06-17T03:00:00Z" {
		t.Errorf("got %q, want 2026-06-17T03:00:00Z", v)
	}

	// Upsert overwrites.
	if err := s.SetMeta(ctx, MetaLastSeenMMTS, "2026-06-17T04:00:00Z"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	v, _ = s.GetMeta(ctx, MetaLastSeenMMTS)
	if v != "2026-06-17T04:00:00Z" {
		t.Errorf("after upsert got %q, want 2026-06-17T04:00:00Z", v)
	}
}

func TestSchema_ReopenPersistsRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if err := s.RecordThread(ctx, Thread{RootPostID: "p", ChannelID: "c", MulticaIssueID: "i"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	_ = s.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, err := s2.ThreadByRoot(ctx, "p")
	if err != nil {
		t.Fatalf("lookup after reopen: %v", err)
	}
	if got.MulticaIssueID != "i" {
		t.Errorf("lost data on reopen: got %q", got.MulticaIssueID)
	}
}
