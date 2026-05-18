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
	want := Cursor{Repo: "test/mem", LastEventID: 100, LastPolledAt: now, ETag: "v1"}
	if err := m.Save(ctx, want); err != nil {
		t.Fatalf("mem Save: %v", err)
	}
	got, _ = m.Load(ctx, "test/mem")
	if got.LastEventID != 100 || got.ETag != "v1" {
		t.Errorf("mem round-trip: got %+v, want %+v", got, want)
	}

	if err := m.Save(ctx, Cursor{}); err == nil {
		t.Error("mem Save with empty repo should error")
	}
}
