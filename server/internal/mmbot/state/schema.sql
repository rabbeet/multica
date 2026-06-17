-- mmbot state schema.
--
-- Five tables back the bidirectional Mattermost ↔ Multica issue sync.
-- File-backed SQLite (`/var/lib/multica-mattermost-bot/state.db` in prod),
-- WAL journal, schema version tracked in `PRAGMA user_version`.
--
-- See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).

CREATE TABLE IF NOT EXISTS mm_threads (
    -- Mattermost root_post_id of the thread — for top-level posts this equals
    -- the post id itself. Stable, primary lookup key from inbound replies and
    -- outbound polling.
    root_post_id      TEXT PRIMARY KEY,
    channel_id        TEXT NOT NULL,
    multica_issue_id  TEXT NOT NULL UNIQUE,
    -- Last status seen by the outbound poller. Used to emit one MM message per
    -- transition (e.g. → waiting). NULL until first poll observes any status.
    last_seen_status  TEXT,
    -- RFC3339 timestamp of the last screenshot we posted for this issue.
    -- Used to rate-limit the idempotent per-comment screenshot pipeline
    -- (finding #1 of /plan-eng-review) — skip if last_render_ts is within
    -- 60s of now. NULL until first screenshot.
    last_render_ts    TEXT,
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS mm_synced_posts (
    -- Mattermost post id. Direction tells whether we processed this post as
    -- inbound (a user message we forwarded to multica) or outbound (a post
    -- the bot itself created — used for echo-loop suppression, finding #7).
    mm_post_id          TEXT PRIMARY KEY,
    multica_comment_id  TEXT,
    direction           TEXT NOT NULL CHECK (direction IN ('mm_to_multica','multica_to_mm')),
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS mm_synced_comments (
    -- Multica comment id we've already forwarded to MM. Lookup before posting
    -- to avoid double-posting on overlapping polling windows.
    multica_comment_id  TEXT PRIMARY KEY,
    mm_post_id          TEXT NOT NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS pending_outbound (
    -- Persistent queue of MM posts we failed to deliver. The outbound retry
    -- worker drains this in id-order with exponential backoff. Survives daemon
    -- restart.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id    TEXT NOT NULL,
    root_post_id  TEXT NOT NULL,
    message       TEXT NOT NULL,
    file_path     TEXT,
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS meta (
    -- Small key→value store for cursors and bookkeeping.
    -- Known keys:
    --   last_seen_mm_ts        — high-water mark for MM REST catch-up
    --   last_seen_multica_ts   — high-water mark for multica polling
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_synced_posts_direction
    ON mm_synced_posts (direction, created_at);

CREATE INDEX IF NOT EXISTS idx_pending_outbound_attempts
    ON pending_outbound (attempts, id);
