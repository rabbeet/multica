-- PUL-479 PR1: outbound Telegram bridge.
-- See server/migrations/091_telegram_bridge.up.sql for schema rationale
-- and plans-multica://Multica/2026-07-24-pul-479-telegram-bridge.md for
-- the design context.

-- name: GetTelegramThreadByIssue :one
-- Lookup used by the outbound scheduler before every send. Returns
-- pgx.ErrNoRows when the issue has no topic yet — the scheduler then
-- creates the topic via Bot API and calls InsertTelegramThread.
SELECT * FROM telegram_thread WHERE issue_id = @issue_id;

-- name: GetTelegramThreadByChatTopic :one
-- Reverse lookup by (chat_id, message_thread_id). Used by the inbound
-- Sink in PR2 to resolve a Telegram reply back to its multica issue.
-- Not called from PR1 code; ships now so the migration + queries land
-- in one PR instead of two.
SELECT * FROM telegram_thread
WHERE chat_id = @chat_id AND message_thread_id = @message_thread_id;

-- name: InsertTelegramThread :one
-- Idempotent insert. ON CONFLICT (issue_id) DO NOTHING covers the race
-- where two scheduler ticks both saw no thread and both called
-- createForumTopic. The second caller sees zero rows RETURNING and
-- re-reads via GetTelegramThreadByIssue. In v1 there is a single
-- worker, so the race is theoretical, but the guarantee costs nothing.
INSERT INTO telegram_thread (issue_id, chat_id, message_thread_id)
VALUES (@issue_id, @chat_id, @message_thread_id)
ON CONFLICT (issue_id) DO NOTHING
RETURNING *;

-- name: DeleteTelegramThreadByIssue :exec
-- Called on Bot API TOPIC_DELETED. Removing the row lets the scheduler
-- lazily recreate the topic on the next queued outbox row for this
-- issue. Existing telegram_outbox rows for the issue survive (FK is
-- issue.id, not thread), so the pending queue re-uses the newly-
-- created topic.
DELETE FROM telegram_thread WHERE issue_id = @issue_id;


-- name: InsertTelegramOutboxRow :one
-- Called from service.CommentService.Create inside the same tx as the
-- comment INSERT. If the caller's tx rolls back, this row rolls back
-- too — no orphan outbox entries pointing at nonexistent comments.
INSERT INTO telegram_outbox (kind, issue_id, comment_id, payload)
VALUES (@kind, @issue_id, sqlc.narg('comment_id')::UUID, @payload)
RETURNING *;

-- name: ClaimPendingTelegramOutbox :many
-- Claim up to 50 due, unclaimed, non-failed rows. FOR UPDATE SKIP LOCKED
-- lets multiple workers coexist even though v1 wires only one. Rows
-- become invisible for 60s (see ResetStuckTelegramOutboxClaims); the
-- worker calls DeleteTelegramOutboxRow / BumpTelegramOutboxRetry /
-- ParkTelegramOutboxFailed / ParkTelegramOutboxRateLimit to finalize.
UPDATE telegram_outbox
SET claimed_at = now()
WHERE id IN (
    SELECT id FROM telegram_outbox
    WHERE claimed_at IS NULL
      AND failed_at IS NULL
      AND not_before_at <= now()
    ORDER BY not_before_at
    LIMIT 50
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: DeleteTelegramOutboxRow :exec
-- Successful send. Row is deleted (no history retention needed — the
-- authoritative record is the multica comment + the delivered Telegram
-- message).
DELETE FROM telegram_outbox WHERE id = @id;

-- name: BumpTelegramOutboxRetry :one
-- Transient failure (5xx, network). Bump retry_count, release the
-- claim so the next tick retries. Not_before_at is advanced by the
-- caller with an exponential backoff before this call — the query only
-- updates the timing/retry fields.
UPDATE telegram_outbox
SET retry_count = retry_count + 1,
    claimed_at = NULL,
    not_before_at = @not_before_at,
    last_error = @last_error
WHERE id = @id AND claimed_at IS NOT NULL AND failed_at IS NULL
RETURNING *;

-- name: ParkTelegramOutboxRateLimit :one
-- Bot API 429. Retry-After honored by advancing not_before_at without
-- bumping retry_count (rate-limit is not a real failure, it's a
-- throttle signal).
UPDATE telegram_outbox
SET claimed_at = NULL,
    not_before_at = @not_before_at,
    last_error = @last_error
WHERE id = @id AND claimed_at IS NOT NULL AND failed_at IS NULL
RETURNING *;

-- name: ParkTelegramOutboxFailed :one
-- Permanent failure (4xx fatal or retry_count > 10). Row stays in the
-- table for operator inspection but is excluded from ClaimPending via
-- the failed_at IS NULL guard.
UPDATE telegram_outbox
SET failed_at = now(),
    claimed_at = NULL,
    last_error = @last_error
WHERE id = @id AND failed_at IS NULL
RETURNING *;

-- name: ResetStuckTelegramOutboxClaims :execrows
-- Recovery on scheduler startup + every tick. Any claim older than
-- 60s is assumed to belong to a crashed worker; release it so the next
-- claim picks it up. This is the safety net that keeps at-least-once
-- honest under process restart.
UPDATE telegram_outbox
SET claimed_at = NULL
WHERE claimed_at IS NOT NULL
  AND failed_at IS NULL
  AND claimed_at < now() - interval '60 seconds';

-- name: CountFailedTelegramOutbox :one
-- Observability shim. Called by ops/tests to detect a growing pile of
-- permanently-failed rows (typically means the bot lost group access).
SELECT COUNT(*)::BIGINT AS failed_count FROM telegram_outbox
WHERE failed_at IS NOT NULL;

-- name: GetTelegramOutbox :one
-- Test helper — read a specific row by id. Not used from production
-- code (the worker only reads via ClaimPending), but the tests need it.
SELECT * FROM telegram_outbox WHERE id = @id;
