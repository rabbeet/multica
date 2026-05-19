package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cursor represents one row of github_poll_cursor — the per-repo
// position the poller resumes from on each tick. Fields are pointers
// to distinguish "never polled" (nil) from "polled but no events
// yet" (zero values). The poller code keeps the value-vs-pointer
// split confined to cursor.go so callers see plain Go semantics.
type Cursor struct {
	Repo         string
	LastEventID  int64 // legacy single-scalar cursor; ignored by the poller after PUL-201, kept on the column for one or two releases of stabilization before being dropped
	LastPolledAt time.Time
	ETag         string // empty when no successful 200 has been observed
	// CursorByType is the per-event-type position, keyed by GitHub's
	// Event.Type string ("PullRequestEvent", "PushEvent", ...). Added
	// in PUL-201 because GitHub /events returns events from at least
	// two distinct numeric-id streams (~9.6e9 for PR-shaped events,
	// ~12e9 for Push/Create/Delete/WorkflowRun) and a single scalar
	// cursor cannot represent both. nil and an empty map are
	// equivalent ("no type seen yet" — every type compares with 0).
	CursorByType map[string]int64
}

// CursorStore is the pgx-backed implementation of cursor I/O. The
// interface is implicit (consumers just take *CursorStore) — tests
// inject the real pool against a docker postgres or call Load/Save
// directly. Keeping it concrete avoids interface-creep early in the
// life of the package; if a second backing store ever lands we
// refactor to an interface then.
type CursorStore struct {
	Pool *pgxpool.Pool
}

// NewCursorStore returns a store backed by the provided pool.
func NewCursorStore(pool *pgxpool.Pool) *CursorStore {
	return &CursorStore{Pool: pool}
}

// Load returns the cursor for repo. Returns an empty Cursor (zero
// values, no ETag) when no row exists yet — caller treats that as
// "first poll, fetch from the top of /events with no
// If-None-Match". pgx.ErrNoRows is normalized to (zero, nil); other
// errors surface verbatim so the poller can log + retry.
func (s *CursorStore) Load(ctx context.Context, repo string) (Cursor, error) {
	var c Cursor
	var lastEventID *int64
	var lastPolledAt *time.Time
	var etag *string
	var cursorByTypeRaw []byte
	row := s.Pool.QueryRow(ctx, `
		SELECT repo, last_event_id, last_polled_at, etag, cursor_by_type
		FROM github_poll_cursor
		WHERE repo = $1
	`, repo)
	err := row.Scan(&c.Repo, &lastEventID, &lastPolledAt, &etag, &cursorByTypeRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Cursor{Repo: repo}, nil
		}
		return Cursor{}, fmt.Errorf("cursor.Load(%s): %w", repo, err)
	}
	if lastEventID != nil {
		c.LastEventID = *lastEventID
	}
	if lastPolledAt != nil {
		c.LastPolledAt = *lastPolledAt
	}
	if etag != nil {
		c.ETag = *etag
	}
	// JSONB → map[string]int64. json.Unmarshal rejects non-integer
	// values for the int64 target, which is the defense we want: an
	// operator that hand-edits a malformed row (e.g.
	// `'{"PushEvent":"abc"}'::jsonb`) gets a hard error here rather
	// than silently re-introducing the PR-blindness PUL-201 fixed —
	// "{}" → empty map (no per-type position recorded yet). The
	// column is NOT NULL with default '{}'::jsonb, so cursorByTypeRaw
	// is never nil-on-the-wire, but we guard for the case anyway in
	// case an older migration shape ever surfaces.
	if len(cursorByTypeRaw) > 0 {
		decoded := map[string]int64{}
		if err := json.Unmarshal(cursorByTypeRaw, &decoded); err != nil {
			return Cursor{}, fmt.Errorf("cursor.Load(%s): decode cursor_by_type: %w", repo, err)
		}
		c.CursorByType = decoded
	}
	return c, nil
}

// Save upserts the cursor row. INSERT ... ON CONFLICT (repo) DO
// UPDATE lets the poller call Save unconditionally — the table has
// one row per repo, and Save replaces it idempotently.
//
// Save is intended to be called exactly once per successful tick,
// AFTER the event batch has been processed (handed off to the sink).
// If a crash happens between handoff and Save, the next tick
// reprocesses the same events — the sink-side ON CONFLICT
// (event_id) on cascade_retrigger then dedups them, which is the
// idempotency contract documented in the plan. Save-before-process
// would risk silent gaps and is forbidden.
func (s *CursorStore) Save(ctx context.Context, c Cursor) error {
	if c.Repo == "" {
		return fmt.Errorf("cursor.Save: empty repo")
	}
	// Nullable inserts: 0 / zero-time / empty-string mean "still no
	// data" and are stored as NULL so the column semantics match
	// what Load reads back. Avoids the "0 means before-the-start"
	// ambiguity that would surface if we wrote 0 explicitly.
	var lastEventID any
	if c.LastEventID != 0 {
		lastEventID = c.LastEventID
	}
	var lastPolledAt any
	if !c.LastPolledAt.IsZero() {
		lastPolledAt = c.LastPolledAt
	}
	var etag any
	if c.ETag != "" {
		etag = c.ETag
	}
	// cursor_by_type is NOT NULL with default '{}', so we always send
	// a JSON value. nil and an empty map both serialize to '{}', which
	// is the canonical "no per-type position recorded yet" value.
	cursorByType := c.CursorByType
	if cursorByType == nil {
		cursorByType = map[string]int64{}
	}
	cursorByTypeJSON, err := json.Marshal(cursorByType)
	if err != nil {
		return fmt.Errorf("cursor.Save(%s): encode cursor_by_type: %w", c.Repo, err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO github_poll_cursor (repo, last_event_id, last_polled_at, etag, cursor_by_type)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (repo) DO UPDATE
			SET last_event_id  = EXCLUDED.last_event_id,
			    last_polled_at = EXCLUDED.last_polled_at,
			    etag           = EXCLUDED.etag,
			    cursor_by_type = EXCLUDED.cursor_by_type
	`, c.Repo, lastEventID, lastPolledAt, etag, cursorByTypeJSON)
	if err != nil {
		return fmt.Errorf("cursor.Save(%s): %w", c.Repo, err)
	}
	return nil
}

// memCursorStore is an in-memory cursor store used by tests that
// don't want a database. Exported via NewMemCursorStore so other
// packages' integration tests can drive the poller without a pg.
type memCursorStore struct {
	rows map[string]Cursor
}

// NewMemCursorStore returns an in-memory CursorStore for tests.
//
// Returns a *memCursorStore not *CursorStore because the production
// type embeds a pool. Callers that want either implementation should
// depend on the Loader/Saver methods directly, or take an interface
// (we don't expose one yet — see the comment on CursorStore).
func NewMemCursorStore() *memCursorStore { //nolint:revive // test helper
	return &memCursorStore{rows: map[string]Cursor{}}
}

// Load implements the same shape as CursorStore.Load.
func (m *memCursorStore) Load(_ context.Context, repo string) (Cursor, error) {
	c, ok := m.rows[repo]
	if !ok {
		return Cursor{Repo: repo}, nil
	}
	// Defensive copy: mutating the returned map must not corrupt the
	// stored row (the production store has the same property by
	// virtue of the JSON round-trip).
	if c.CursorByType != nil {
		copied := make(map[string]int64, len(c.CursorByType))
		for k, v := range c.CursorByType {
			copied[k] = v
		}
		c.CursorByType = copied
	}
	return c, nil
}

// Save implements the same shape as CursorStore.Save.
func (m *memCursorStore) Save(_ context.Context, c Cursor) error {
	if c.Repo == "" {
		return fmt.Errorf("cursor.Save: empty repo")
	}
	if m.rows == nil {
		m.rows = map[string]Cursor{}
	}
	// Defensive copy of CursorByType so a test that mutates the live
	// poller's map (or vice versa) does not corrupt the stored row.
	// Production CursorStore implicitly copies via the JSON round-trip;
	// here we have to be explicit.
	if c.CursorByType != nil {
		copied := make(map[string]int64, len(c.CursorByType))
		for k, v := range c.CursorByType {
			copied[k] = v
		}
		c.CursorByType = copied
	}
	m.rows[c.Repo] = c
	return nil
}
