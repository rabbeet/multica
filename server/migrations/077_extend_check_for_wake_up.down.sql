-- Reverse of 077. Safe to apply on single-user multica because the only
-- writer of wake_up comments and hook_reminder history rows is the reminder
-- scheduler shipped in the same PR; rolling 077 back implies rolling 076
-- back too, which cascade-deletes pending reminders. Existing fired
-- comments and history rows would then violate the narrower CHECK — drop
-- them first if necessary (manual operator step, not automated here
-- because the operator may want to inspect them).

BEGIN;

ALTER TABLE issue_status_history DROP CONSTRAINT IF EXISTS issue_status_history_source_check;
ALTER TABLE issue_status_history ADD CONSTRAINT issue_status_history_source_check
    CHECK (source IN (
        'manual',
        'hook_comment',
        'skill_publish',
        'skill_pickup',
        'webhook_forge',
        'backfill'
    ));

ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN (
        'comment',
        'status_change',
        'progress_update',
        'system'
    ));

COMMIT;
