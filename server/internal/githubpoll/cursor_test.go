package githubpoll

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testDBPool mirrors the pattern in internal/cascade/list_test.go —
// reuse the multica dev DB on default port, skip when unreachable so
// `go test ./...` works without docker-compose running.
func testDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup: drop any rows this test inserted by
		// prefix. Avoids cross-test pollution under -count=N.
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM github_poll_cursor WHERE repo LIKE 'test/%'`)
		pool.Close()
	})
	return pool
}

func TestCursorStore_LoadEmpty(t *testing.T) {
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	got, err := s.Load(context.Background(), "test/nonexistent")
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if got.Repo != "test/nonexistent" {
		t.Errorf("Repo = %q, want %q", got.Repo, "test/nonexistent")
	}
	if got.LastEventID != 0 || !got.LastPolledAt.IsZero() || got.ETag != "" {
		t.Errorf("empty load should return zero values, got %+v", got)
	}
}

func TestCursorStore_SaveLoadRoundTrip(t *testing.T) {
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := Cursor{
		Repo:         "test/roundtrip",
		LastEventID:  12345,
		LastPolledAt: now,
		ETag:         "W/\"abcdef\"",
		CursorByType: map[string]int64{
			"PullRequestEvent": 9648929307,
			"PushEvent":        12078860926,
		},
	}
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(context.Background(), "test/roundtrip")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastEventID != want.LastEventID {
		t.Errorf("LastEventID = %d, want %d", got.LastEventID, want.LastEventID)
	}
	if !got.LastPolledAt.Equal(want.LastPolledAt) {
		t.Errorf("LastPolledAt = %v, want %v", got.LastPolledAt, want.LastPolledAt)
	}
	if got.ETag != want.ETag {
		t.Errorf("ETag = %q, want %q", got.ETag, want.ETag)
	}
	if len(got.CursorByType) != 2 ||
		got.CursorByType["PullRequestEvent"] != 9648929307 ||
		got.CursorByType["PushEvent"] != 12078860926 {
		t.Errorf("CursorByType = %v, want PR:9648929307 + Push:12078860926",
			got.CursorByType)
	}
}

func TestCursorStore_SaveLoadCursorByType_Defaults(t *testing.T) {
	// Save with nil CursorByType should write '{}' (the column DEFAULT)
	// and Load should return nil or empty map — both equivalent.
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	if err := s.Save(context.Background(), Cursor{Repo: "test/defaults"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(context.Background(), "test/defaults")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.CursorByType) != 0 {
		t.Errorf("CursorByType = %v, want empty/nil", got.CursorByType)
	}
}

func TestCursorStore_Load_CorruptedCursorByType_Error(t *testing.T) {
	// An operator (or a future code path that writes garbage) might
	// leave the JSONB in a shape json.Unmarshal cannot coerce into
	// map[string]int64 (here: a string value where int64 is expected).
	// We want a hard error with repo context, NOT a silent empty map
	// — empty map behaves like the pre-PUL-201 single-scalar cursor
	// reset to 0, which re-introduces the pr_merged blindness this
	// fix is here to remove.
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO github_poll_cursor (repo, cursor_by_type)
		VALUES ($1, $2::jsonb)
		ON CONFLICT (repo) DO UPDATE
			SET cursor_by_type = EXCLUDED.cursor_by_type
	`, "test/corrupted", `{"PushEvent": "not-a-number"}`)
	if err != nil {
		t.Fatalf("seed corrupted row: %v", err)
	}
	_, err = s.Load(context.Background(), "test/corrupted")
	if err == nil {
		t.Fatalf("Load on corrupted row returned nil error; want hard error")
	}
}

func TestCursorStore_SaveUpsert(t *testing.T) {
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	a := Cursor{Repo: "test/upsert", LastEventID: 1, LastPolledAt: time.Now().UTC().Truncate(time.Microsecond), ETag: "v1"}
	if err := s.Save(context.Background(), a); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	b := Cursor{Repo: "test/upsert", LastEventID: 2, LastPolledAt: a.LastPolledAt.Add(time.Second), ETag: "v2"}
	if err := s.Save(context.Background(), b); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err := s.Load(context.Background(), "test/upsert")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastEventID != 2 || got.ETag != "v2" {
		t.Errorf("upsert lost overwrite: got %+v, want LastEventID=2 ETag=v2", got)
	}
}

func TestCursorStore_SaveNullableFields(t *testing.T) {
	// Save with zero values should write NULL columns — Load returns
	// zero-values back. Confirms the conversion logic matches the
	// migration's `NULL` declarations.
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	c := Cursor{Repo: "test/nulls"}
	if err := s.Save(context.Background(), c); err != nil {
		t.Fatalf("Save nulls: %v", err)
	}
	got, err := s.Load(context.Background(), "test/nulls")
	if err != nil {
		t.Fatalf("Load nulls: %v", err)
	}
	if got.LastEventID != 0 || !got.LastPolledAt.IsZero() || got.ETag != "" {
		t.Errorf("nullable round-trip lost zeros: %+v", got)
	}
}

func TestCursorStore_SaveEmptyRepo(t *testing.T) {
	pool := testDBPool(t)
	s := NewCursorStore(pool)
	err := s.Save(context.Background(), Cursor{})
	if err == nil {
		t.Error("Save with empty repo should error")
	}
}

func TestMemCursorStore(t *testing.T) {
	// In-memory store gets the same Load/Save semantics tests so
	// integration tests that use the mem store have the same
	// behavioral contract as production.
	m := NewMemCursorStore()
	ctx := context.Background()

	got, err := m.Load(ctx, "test/mem")
	if err != nil || got.Repo != "test/mem" || got.LastEventID != 0 {
		t.Errorf("mem Load empty: got=%+v, err=%v", got, err)
	}

	now := time.Now().UTC()
	want := Cursor{
		Repo:         "test/mem",
		LastEventID:  100,
		LastPolledAt: now,
		ETag:         "v1",
		CursorByType: map[string]int64{"PullRequestEvent": 9648929307, "PushEvent": 12078860926},
	}
	if err := m.Save(ctx, want); err != nil {
		t.Fatalf("mem Save: %v", err)
	}
	got, _ = m.Load(ctx, "test/mem")
	if got.LastEventID != 100 || got.ETag != "v1" {
		t.Errorf("mem round-trip: got %+v, want %+v", got, want)
	}
	if got.CursorByType["PullRequestEvent"] != 9648929307 ||
		got.CursorByType["PushEvent"] != 12078860926 {
		t.Errorf("mem CursorByType round-trip: got %v", got.CursorByType)
	}
	// Defensive copy: mutating the returned map must not corrupt the
	// stored row. Mirrors what production CursorStore gets for free
	// from its JSON round-trip.
	got.CursorByType["PullRequestEvent"] = 1
	again, _ := m.Load(ctx, "test/mem")
	if again.CursorByType["PullRequestEvent"] != 9648929307 {
		t.Errorf("mem store leaked map mutation: stored value changed to %d",
			again.CursorByType["PullRequestEvent"])
	}

	if err := m.Save(ctx, Cursor{}); err == nil {
		t.Error("mem Save with empty repo should error")
	}
}
