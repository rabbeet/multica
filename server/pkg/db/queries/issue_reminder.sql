-- PUL-154: «Wake up in N» — one-shot reminders that post a wake_up comment
-- when fire_at arrives, transitioning waiting/backlog → todo atomically.
-- See server/migrations/076_issue_reminder.up.sql for the schema rationale.

-- name: CreateIssueReminder :one
INSERT INTO issue_reminder (
    workspace_id, issue_id, created_by_type, created_by_id, fire_at, note
) VALUES (
    @workspace_id, @issue_id, @created_by_type, @created_by_id, @fire_at, sqlc.narg('note')
)
RETURNING *;

-- name: GetIssueReminder :one
SELECT * FROM issue_reminder
WHERE id = @id;

-- name: ListPendingRemindersForIssue :many
SELECT * FROM issue_reminder
WHERE issue_id = @issue_id AND status = 'pending'
ORDER BY fire_at;

-- name: CancelIssueReminder :one
-- Caller-initiated cancel. Only pending reminders can be cancelled; firing /
-- already-fired / cancelled rows return ErrNoRows so the handler returns 409
-- to the user instead of silently no-op'ing.
UPDATE issue_reminder
SET status = 'cancelled',
    cancelled_at = now(),
    cancel_reason = 'manual'
WHERE id = @id AND status = 'pending'
RETURNING *;

-- name: MarkRemindersCancelledByActivity :execrows
-- Activity-prune: any pending reminder whose issue has received a comment
-- after the reminder was created is silently cancelled. Run periodically
-- (every ~5 min) AND once at the top of each scheduler tick to keep the
-- claim path simple — ClaimDueReminders can then be a pure due+pending
-- claim without an inline EXISTS check.
UPDATE issue_reminder r
SET status = 'cancelled',
    cancelled_at = now(),
    cancel_reason = 'activity'
WHERE r.status = 'pending'
  AND EXISTS (
      SELECT 1 FROM comment c
      WHERE c.issue_id = r.issue_id
        AND c.created_at > r.created_at
  );

-- name: ClaimDueReminders :many
-- Two-phase claim. SET firing_at=now() on rows that are due and not yet
-- being processed; FOR UPDATE SKIP LOCKED lets concurrent workers split the
-- work without blocking. The caller then completes each claimed row by
-- creating the wake_up comment + applying the status transition + calling
-- MarkReminderFired. If the caller crashes mid-way, ResetStuckClaims will
-- release the claim after the recovery threshold.
UPDATE issue_reminder
SET firing_at = now()
WHERE id IN (
    SELECT id FROM issue_reminder
    WHERE status = 'pending'
      AND firing_at IS NULL
      AND fire_at <= now()
    ORDER BY fire_at
    LIMIT 100
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkReminderFired :one
-- Finalize a claim: only succeeds if the row is still in the claimed state
-- (status='pending' AND firing_at IS NOT NULL). A second call returns
-- ErrNoRows so retries don't double-write fired_at / fired_comment_id.
UPDATE issue_reminder
SET status = 'fired',
    fired_at = now(),
    firing_at = NULL,
    fired_comment_id = @fired_comment_id
WHERE id = @id
  AND status = 'pending'
  AND firing_at IS NOT NULL
RETURNING *;

-- name: UnclaimReminder :exec
-- Error-path release: caller failed to create the comment/apply the
-- transition; clear firing_at so the next scheduler tick retries.
UPDATE issue_reminder
SET firing_at = NULL
WHERE id = @id AND status = 'pending';

-- name: ResetStuckClaims :execrows
-- Crash recovery: clear firing_at on claims older than the recovery
-- threshold. Run periodically (every ~5 min) and once at scheduler start.
-- 5 minutes is comfortably longer than a normal fire (comment INSERT +
-- status flip + history insert, all sub-100ms) but short enough that a
-- crashed worker's reminders fire promptly on the next tick.
UPDATE issue_reminder
SET firing_at = NULL
WHERE status = 'pending'
  AND firing_at IS NOT NULL
  AND firing_at < now() - INTERVAL '5 minutes';

-- name: CancelReminderForGoneCreator :exec
-- Edge case: the member or agent who created the reminder was deleted
-- before fire. The scheduler detects this via a creator-lookup before
-- attempting to post the comment, and cancels the reminder so it never
-- fires under a phantom author.
UPDATE issue_reminder
SET status = 'cancelled',
    cancelled_at = now(),
    cancel_reason = 'creator_gone'
WHERE id = @id AND status = 'pending';
