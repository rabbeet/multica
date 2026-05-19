-- Reverse of 084. Safe to apply iff no rows with source='hook_pr_merged'
-- exist; if they do, drop them first (manual operator step — the operator
-- may want to inspect them) or the CHECK re-add below will fail.

BEGIN;

ALTER TABLE issue_status_history DROP CONSTRAINT IF EXISTS issue_status_history_source_check;
ALTER TABLE issue_status_history ADD CONSTRAINT issue_status_history_source_check
    CHECK (source IN (
        'manual',
        'hook_comment',
        'hook_reminder',
        'skill_publish',
        'skill_pickup',
        'webhook_forge',
        'backfill'
    ));

COMMIT;
