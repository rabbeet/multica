-- PUL-154: extend two existing CHECK constraints to accommodate the new
-- 'wake_up' comment type and the 'hook_reminder' status-history source.
--
-- (1) comment.type — adds 'wake_up'. Marking these comments with a distinct
--     type lets service.DecideFlip skip them automatically (it already
--     filters on type='comment'), and lets handler.shouldEnqueueOnComment
--     suppress the agent on-comment trigger explicitly (added in the same
--     PR). Both filters are critical so a fired reminder does not start an
--     unwanted agent run.
--
-- (2) issue_status_history.source — adds 'hook_reminder'. The reminder
--     scheduler records the waiting/backlog → todo flip with this source,
--     keeping the audit trail honest (distinguishable from 'manual' or
--     'hook_comment'). The UNIQUE(source, ref_id) idempotency contract
--     continues to hold; reminder.id is used as ref_id so retries are safe.
--
-- Both are non-destructive ALTER CONSTRAINT replacements: we DROP IF EXISTS
-- under the existing default constraint name (table_column_check convention)
-- and re-add with the extended whitelist. Each migration is wrapped in a
-- transaction so a partial application is impossible.

BEGIN;

-- (1) comment.type whitelist extension.
ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN (
        'comment',
        'status_change',
        'progress_update',
        'system',
        'wake_up'          -- new (PUL-154)
    ));

-- (2) issue_status_history.source whitelist extension.
ALTER TABLE issue_status_history DROP CONSTRAINT IF EXISTS issue_status_history_source_check;
ALTER TABLE issue_status_history ADD CONSTRAINT issue_status_history_source_check
    CHECK (source IN (
        'manual',
        'hook_comment',
        'hook_reminder',   -- new (PUL-154)
        'skill_publish',
        'skill_pickup',
        'webhook_forge',
        'backfill'
    ));

COMMIT;
