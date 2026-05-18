-- PUL-168 rollback. Backfill any NULL author_id rows to the all-zeros sentinel
-- before re-asserting NOT NULL so the rollback does not error on real data.
-- Then drop any system-typed rows (they would otherwise violate the restored
-- CHECK) and restore the original member/agent-only CHECK.

UPDATE comment
SET author_id = '00000000-0000-0000-0000-000000000000'::uuid
WHERE author_id IS NULL;

-- Note: agent_task_queue.trigger_comment_id has ON DELETE SET NULL (migration
-- 028), so deleting system comments here will null-out the trigger link on any
-- task that referenced one. Existing readers guard on .Valid, but the audit
-- trail back to the original wake-up reason is lost. Acceptable for a rollback.
DELETE FROM comment WHERE author_type = 'system';

ALTER TABLE comment ALTER COLUMN author_id SET NOT NULL;

ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_author_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_author_type_check
    CHECK (author_type IN ('member', 'agent'));
