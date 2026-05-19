-- PUL-194: extend issue_status_history.source CHECK with 'hook_pr_merged' so
-- the cascade worker can record server-side auto-flips to 'deployed' on
-- pr_merged events without violating the existing whitelist.
--
-- The new source is emitted by cascade.ApplyDeployFlip (server/internal/cascade/
-- deploy_flip.go) when a PR with [PUL-N] in its title is merged on a target
-- repo and the issue is eligible (no active cascade, status not already
-- deployed/cancelled). UNIQUE (source, ref_id) — preserved by this migration
-- — provides idempotency: the GitHub event_id is used as ref_id, so a
-- redelivery of the same event no-ops cleanly on the duplicate insert.
--
-- Mirror of 077_extend_check_for_wake_up.up.sql: non-destructive DROP+ADD
-- under the default constraint name, wrapped in a single transaction so a
-- partial application is impossible.

BEGIN;

ALTER TABLE issue_status_history DROP CONSTRAINT IF EXISTS issue_status_history_source_check;
ALTER TABLE issue_status_history ADD CONSTRAINT issue_status_history_source_check
    CHECK (source IN (
        'manual',
        'hook_comment',
        'hook_reminder',   -- PUL-154
        'hook_pr_merged',  -- new (PUL-194)
        'skill_publish',
        'skill_pickup',
        'webhook_forge',
        'backfill'
    ));

COMMIT;
