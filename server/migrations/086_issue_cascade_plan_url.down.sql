-- PUL-198 Part 2 down: remove cascade_plan_url and shrink action CHECK back.
--
-- Any cascade_retrigger rows that landed on action='single_pr_no_spawn'
-- (observability rows from the gate added in 086.up) must be deleted
-- before the CHECK can be restored — leaving them would fail the new
-- constraint. They're deploy-flip-only audit rows with no spawn side
-- effect, safe to drop.

BEGIN;

DELETE FROM cascade_retrigger WHERE action = 'single_pr_no_spawn';

ALTER TABLE cascade_retrigger
    DROP CONSTRAINT cascade_retrigger_action_check;

ALTER TABLE cascade_retrigger
    ADD CONSTRAINT cascade_retrigger_action_check
        CHECK (action IS NULL OR action IN (
            'spawn',
            'loop_guard_skip',
            'dedup_skip',
            'scope_filter_skip',
            'state_mismatch_skip',
            'plan_amended_pause',
            'queued_pending'
        ));

ALTER TABLE issue
    DROP COLUMN cascade_plan_url;

COMMIT;
