-- PUL-479: two-way Telegram bridge for multica tickets.
-- PR1 (this migration) — outbound (multica → TG forum topic).
-- PR2 (follow-up) adds inbound-dedup table telegram_inbound + cursor.
--
-- Design (see plans-multica://Multica/2026-07-24-pul-479-telegram-bridge.md):
--   1. telegram_thread — issue ↔ forum topic mapping. Populated lazily by
--      the outbound scheduler on first sendMessage for the issue; NOT
--      populated on EventIssueCreated (bus is synchronous + non-
--      transactional, so a listener-driven create can lose the row on
--      backend restart between commit and publish).
--   2. telegram_outbox — at-least-once queue for outbound messages. The
--      HTTP handler (via service.CommentService.Create) inserts one row
--      inside the same tx as the comment INSERT — either both land or
--      neither does. The scheduler drains rows with FOR UPDATE SKIP LOCKED
--      every 2s, respects rate-limits + backoff, escalates fatal errors
--      (BOT_KICKED, TOPIC_DELETED, 401) via the existing notify.Bridge
--      cascade alert.
--
-- Same design pattern as PUL-164 (issue_child_progress_outbox +
-- child_progress_scheduler). Reused deliberately so future ops readers
-- recognize the shape.

BEGIN;

-- (1) telegram_thread — issue ↔ forum topic mapping.
-- One row per issue that has ever produced an outbound message. Multiple
-- deploys / bot rotations may change chat_id; we treat the tuple
-- (chat_id, message_thread_id) as opaque and re-create on TOPIC_DELETED
-- by deleting the row and letting the scheduler recreate.
CREATE TABLE telegram_thread (
    issue_id           UUID PRIMARY KEY REFERENCES issue(id) ON DELETE CASCADE,
    -- Supergroup chat IDs are negative and can exceed int32. BIGINT is
    -- mandatory here; using INTEGER silently truncates in Postgres.
    chat_id            BIGINT NOT NULL,
    -- Bot API returns message_thread_id as int; INTEGER is sufficient.
    message_thread_id  INTEGER NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when the issue transitions to a terminal status. Auto-cleared
    -- when the next comment reopens the topic (v1 leaves this NULL — the
    -- close/reopen flow is deferred to a follow-up PR).
    closed_at          TIMESTAMPTZ NULL
);

-- Secondary lookup by (chat_id, message_thread_id) — used by the inbound
-- Sink in PR2 to resolve a Telegram reply back to an issue_id. Not hot in
-- PR1 but cheap to add now.
CREATE INDEX idx_telegram_thread_chat_topic
    ON telegram_thread (chat_id, message_thread_id);


-- (2) telegram_outbox — at-least-once queue for outbound messages.
-- kind discriminates payload shape ('comment' carries the multica
-- comment.id; 'status_change' / 'assignee_change' are v1-optional and
-- flagged as future work in the plan). payload is JSONB so we can grow
-- fields without new migrations.
CREATE TABLE telegram_outbox (
    id             BIGSERIAL PRIMARY KEY,
    kind           TEXT NOT NULL CHECK (kind IN ('comment', 'status_change', 'assignee_change')),
    issue_id       UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    -- NULL for status_change / assignee_change (kind='comment' rows
    -- reference the comment they mirror; the FK CASCADE means deleting
    -- a comment also removes any pending outbox entry for it).
    comment_id     UUID NULL REFERENCES comment(id) ON DELETE CASCADE,
    payload        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Rate-limit / backoff parking. Scheduler ignores rows where
    -- not_before_at > now(). Default = now() means "eligible immediately".
    not_before_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when a worker claims the row; released to NULL on success (row
    -- deleted) or transient failure (bump retry_count). Reset by
    -- ResetStuckTelegramOutboxClaims after 60s to survive worker crash.
    claimed_at     TIMESTAMPTZ NULL,
    retry_count    INTEGER NOT NULL DEFAULT 0,
    -- Set on permanent failure (retry_count > 10, or 4xx fatal like
    -- BOT_KICKED / TOPIC_DELETED / 401). Rows with failed_at IS NOT NULL
    -- are excluded from the pending index and left for operator review.
    failed_at      TIMESTAMPTZ NULL,
    last_error     TEXT NULL
);

-- Hot path: scheduler tick claims due, un-claimed, non-failed rows.
-- Partial index keeps the index small (only pending rows).
CREATE INDEX idx_telegram_outbox_pending
    ON telegram_outbox (not_before_at)
    WHERE claimed_at IS NULL AND failed_at IS NULL;

-- Recovery: stuck-claim reset by ResetStuckTelegramOutboxClaims.
CREATE INDEX idx_telegram_outbox_stuck
    ON telegram_outbox (claimed_at)
    WHERE claimed_at IS NOT NULL AND failed_at IS NULL;

COMMIT;
