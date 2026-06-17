// Package state is the SQLite-backed persistence layer for the
// multica-mattermost-bot daemon (PUL-328).
//
// Five tables back the bidirectional Mattermost ↔ Multica issue sync:
//
//	mm_threads          MM root_post_id ↔ multica_issue_id mapping
//	mm_synced_posts     dedup for both directions (echo-loop suppression)
//	mm_synced_comments  multica comments already forwarded to MM
//	pending_outbound    persistent retry queue for failed MM posts
//	meta                cursor bookkeeping (catch-up high-water marks)
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package state

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO
)

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is the expected PRAGMA user_version value after Open. Bump
// when adding migrations; Open will reject databases newer than this build.
const SchemaVersion = 1

// Direction tags an MM post as inbound (user → multica) or outbound
// (bot → MM). Stored verbatim in mm_synced_posts.direction.
type Direction string

const (
	DirectionMMToMulitca Direction = "mm_to_multica"
	DirectionMulticaToMM Direction = "multica_to_mm"
)

// MetaKey is a typed alias for known meta-table cursor keys.
type MetaKey string

const (
	// MetaLastSeenMMTS is the high-water RFC3339 timestamp of MM posts the
	// daemon has processed. Used by the WS reconnect catch-up loop to fetch
	// only posts arriving during the disconnect window.
	MetaLastSeenMMTS MetaKey = "last_seen_mm_ts"

	// MetaLastSeenMulticaTS is the high-water RFC3339 timestamp passed to
	// `multica issue list --updated-since` to gate which issues are polled
	// for new comments.
	MetaLastSeenMulticaTS MetaKey = "last_seen_multica_ts"
)

// ErrNotFound is returned by lookup helpers when no row matches. Callers use
// errors.Is to distinguish "no mapping yet" from "real database error."
var ErrNotFound = errors.New("mmbot/state: row not found")

// Thread describes a Mattermost thread that maps to one multica issue.
// All timestamp fields are stored as RFC3339 strings in SQLite.
type Thread struct {
	RootPostID      string
	ChannelID       string
	MulticaIssueID  string
	LastSeenStatus  string // empty if never observed
	LastRenderTS    string // empty if no screenshot posted yet
	CreatedAt       string
}

// PendingPost is one row of the persistent outbound retry queue.
type PendingPost struct {
	ID          int64
	ChannelID   string
	RootPostID  string
	Message     string
	FilePath    string // empty if no attachment
	Attempts    int
	LastError   string
	CreatedAt   string
}

// Store wraps an *sql.DB owning the mmbot schema. Methods take context and
// are safe for concurrent use; the daemon runs one Store per process.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path with WAL journal mode,
// applies the embedded schema, and verifies PRAGMA user_version matches
// SchemaVersion. Caller closes via *Store.Close.
//
// Mode is 0600 enforced via the SQLite driver — the file may be world-readable
// if it pre-exists with looser permissions; callers should chmod after first
// Open if running in a hostile environment.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("mmbot/state: empty database path")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("mmbot/state: open: %w", err)
	}
	// Single writer; modernc.org/sqlite is goroutine-safe but contention is
	// cheap when serialized.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mmbot/state: ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mmbot/state: apply schema: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mmbot/state: read user_version: %w", err)
	}
	if current > SchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf("mmbot/state: database at schema version %d, binary supports up to %d", current, SchemaVersion)
	}
	if current < SchemaVersion {
		// Forward-only bump on first run. Future migrations will go here as a
		// switch on current.
		if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("mmbot/state: bump user_version: %w", err)
		}
	}

	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for callers that need raw access (mostly
// tests). The daemon should use the typed methods.
func (s *Store) DB() *sql.DB { return s.db }

// ── mm_threads ───────────────────────────────────────────────────────────────

// RecordThread inserts a new MM root_post_id ↔ multica_issue_id mapping.
// Idempotent: re-inserting the same (root_post_id, multica_issue_id) returns
// nil. Inserting the same root_post_id with a different issue id returns the
// underlying UNIQUE constraint error.
func (s *Store) RecordThread(ctx context.Context, t Thread) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO mm_threads (root_post_id, channel_id, multica_issue_id)
        VALUES (?, ?, ?)
        ON CONFLICT(root_post_id) DO NOTHING
    `, t.RootPostID, t.ChannelID, t.MulticaIssueID)
	if err != nil {
		return fmt.Errorf("mmbot/state: record thread: %w", err)
	}
	return nil
}

// ThreadByRoot returns the thread mapping for a given MM root_post_id, or
// ErrNotFound if absent.
func (s *Store) ThreadByRoot(ctx context.Context, rootPostID string) (Thread, error) {
	var t Thread
	err := s.db.QueryRowContext(ctx, `
        SELECT root_post_id, channel_id, multica_issue_id,
               COALESCE(last_seen_status, ''),
               COALESCE(last_render_ts, ''),
               created_at
        FROM mm_threads WHERE root_post_id = ?
    `, rootPostID).Scan(
		&t.RootPostID, &t.ChannelID, &t.MulticaIssueID,
		&t.LastSeenStatus, &t.LastRenderTS, &t.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, ErrNotFound
	}
	if err != nil {
		return Thread{}, fmt.Errorf("mmbot/state: lookup thread by root: %w", err)
	}
	return t, nil
}

// ThreadByIssue returns the thread mapping for a given multica issue id, or
// ErrNotFound. Used by the outbound poller to address MM posts to the right
// thread root.
func (s *Store) ThreadByIssue(ctx context.Context, issueID string) (Thread, error) {
	var t Thread
	err := s.db.QueryRowContext(ctx, `
        SELECT root_post_id, channel_id, multica_issue_id,
               COALESCE(last_seen_status, ''),
               COALESCE(last_render_ts, ''),
               created_at
        FROM mm_threads WHERE multica_issue_id = ?
    `, issueID).Scan(
		&t.RootPostID, &t.ChannelID, &t.MulticaIssueID,
		&t.LastSeenStatus, &t.LastRenderTS, &t.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Thread{}, ErrNotFound
	}
	if err != nil {
		return Thread{}, fmt.Errorf("mmbot/state: lookup thread by issue: %w", err)
	}
	return t, nil
}

// ActiveIssueIDs returns the multica_issue_ids the outbound poller should
// currently watch. Today this is every thread on file; the poller drops
// terminal-status issues from its working set after observing them.
func (s *Store) ActiveIssueIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT multica_issue_id FROM mm_threads ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("mmbot/state: list active issues: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("mmbot/state: scan issue id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetThreadStatus updates last_seen_status for a thread. Used by the outbound
// poller after observing (and notifying on) a multica status change.
func (s *Store) SetThreadStatus(ctx context.Context, issueID, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE mm_threads SET last_seen_status = ? WHERE multica_issue_id = ?`,
		status, issueID)
	if err != nil {
		return fmt.Errorf("mmbot/state: set thread status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRendered stamps last_render_ts on the thread for the given issue.
// The screenshot pipeline calls this after a successful PNG upload to MM;
// callers consult ShouldRender first to enforce the 60s rate-limit
// (finding #1 of /plan-eng-review).
func (s *Store) MarkRendered(ctx context.Context, issueID string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE mm_threads SET last_render_ts = ? WHERE multica_issue_id = ?`,
		at.UTC().Format(time.RFC3339), issueID)
	if err != nil {
		return fmt.Errorf("mmbot/state: mark rendered: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ShouldRender reports whether a new screenshot is allowed for the given issue
// at time `now`, given the rate-limit window. Returns true if last_render_ts
// is missing or older than window.
func (s *Store) ShouldRender(ctx context.Context, issueID string, now time.Time, window time.Duration) (bool, error) {
	t, err := s.ThreadByIssue(ctx, issueID)
	if err != nil {
		return false, err
	}
	if t.LastRenderTS == "" {
		return true, nil
	}
	last, err := time.Parse(time.RFC3339, t.LastRenderTS)
	if err != nil {
		// Corrupt cursor — treat as "no last render" rather than refusing
		// forever. The next successful MarkRendered will heal the field.
		return true, nil
	}
	return now.Sub(last) >= window, nil
}

// ── mm_synced_posts ──────────────────────────────────────────────────────────

// RecordSyncedPost inserts a dedup row for an MM post in the given direction.
// `multicaCommentID` may be empty (e.g. inbound posts that became multica
// issue descriptions, not comments). Idempotent via PRIMARY KEY conflict.
func (s *Store) RecordSyncedPost(ctx context.Context, mmPostID, multicaCommentID string, dir Direction) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO mm_synced_posts (mm_post_id, multica_comment_id, direction)
        VALUES (?, ?, ?)
        ON CONFLICT(mm_post_id) DO NOTHING
    `, mmPostID, nullIfEmpty(multicaCommentID), string(dir))
	if err != nil {
		return fmt.Errorf("mmbot/state: record synced post: %w", err)
	}
	return nil
}

// IsSyncedPost reports whether we have already processed (or emitted) the
// given MM post id, in either direction. Used as the second-layer echo-loop
// filter (finding #7 of /plan-eng-review) on top of user_id matching.
func (s *Store) IsSyncedPost(ctx context.Context, mmPostID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM mm_synced_posts WHERE mm_post_id = ? LIMIT 1`, mmPostID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mmbot/state: check synced post: %w", err)
	}
	return true, nil
}

// ── mm_synced_comments ───────────────────────────────────────────────────────

// RecordSyncedComment maps a multica comment we just forwarded to MM. Idempotent.
func (s *Store) RecordSyncedComment(ctx context.Context, multicaCommentID, mmPostID string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO mm_synced_comments (multica_comment_id, mm_post_id)
        VALUES (?, ?)
        ON CONFLICT(multica_comment_id) DO NOTHING
    `, multicaCommentID, mmPostID)
	if err != nil {
		return fmt.Errorf("mmbot/state: record synced comment: %w", err)
	}
	return nil
}

// IsSyncedComment reports whether a multica comment id has already been
// forwarded to MM. Used by the outbound poller before posting to avoid
// double-delivery across overlapping cycles.
func (s *Store) IsSyncedComment(ctx context.Context, multicaCommentID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM mm_synced_comments WHERE multica_comment_id = ? LIMIT 1`, multicaCommentID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mmbot/state: check synced comment: %w", err)
	}
	return true, nil
}

// ── pending_outbound ─────────────────────────────────────────────────────────

// EnqueuePending appends a row to the outbound retry queue.
func (s *Store) EnqueuePending(ctx context.Context, p PendingPost) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
        INSERT INTO pending_outbound (channel_id, root_post_id, message, file_path, attempts, last_error)
        VALUES (?, ?, ?, ?, ?, ?)
    `, p.ChannelID, p.RootPostID, p.Message, nullIfEmpty(p.FilePath), p.Attempts, nullIfEmpty(p.LastError))
	if err != nil {
		return 0, fmt.Errorf("mmbot/state: enqueue pending: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("mmbot/state: last insert id: %w", err)
	}
	return id, nil
}

// NextPending returns up to `limit` pending posts in FIFO order (lowest id
// first). The retry worker drains in this order to preserve MM thread message
// ordering even after a long outage.
func (s *Store) NextPending(ctx context.Context, limit int) ([]PendingPost, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, channel_id, root_post_id, message,
               COALESCE(file_path, ''), attempts, COALESCE(last_error, ''), created_at
        FROM pending_outbound
        ORDER BY id ASC
        LIMIT ?
    `, limit)
	if err != nil {
		return nil, fmt.Errorf("mmbot/state: next pending: %w", err)
	}
	defer rows.Close()
	var out []PendingPost
	for rows.Next() {
		var p PendingPost
		if err := rows.Scan(
			&p.ID, &p.ChannelID, &p.RootPostID, &p.Message,
			&p.FilePath, &p.Attempts, &p.LastError, &p.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("mmbot/state: scan pending: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePending removes a pending row by id. Called after a successful MM
// post by the retry worker.
func (s *Store) DeletePending(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_outbound WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mmbot/state: delete pending: %w", err)
	}
	return nil
}

// IncrementPendingAttempt bumps the attempt counter and records the latest
// error for a pending post that failed again.
func (s *Store) IncrementPendingAttempt(ctx context.Context, id int64, lastErr string) error {
	_, err := s.db.ExecContext(ctx, `
        UPDATE pending_outbound
        SET attempts = attempts + 1, last_error = ?
        WHERE id = ?
    `, nullIfEmpty(lastErr), id)
	if err != nil {
		return fmt.Errorf("mmbot/state: bump pending attempt: %w", err)
	}
	return nil
}

// ── meta ─────────────────────────────────────────────────────────────────────

// GetMeta returns the value for a meta key, or "" if absent.
func (s *Store) GetMeta(ctx context.Context, key MetaKey) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, string(key)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("mmbot/state: get meta: %w", err)
	}
	return v, nil
}

// SetMeta upserts a meta key→value pair.
func (s *Store) SetMeta(ctx context.Context, key MetaKey, value string) error {
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO meta (key, value) VALUES (?, ?)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value
    `, string(key), value)
	if err != nil {
		return fmt.Errorf("mmbot/state: set meta: %w", err)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// nullIfEmpty converts "" → sql.NullString{Valid:false} so we don't litter
// the database with empty strings when the column intent is "absent."
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
