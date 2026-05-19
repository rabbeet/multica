-- PUL-198 Part 2: explicit cascade_plan_url primitive + worker gate.
--
-- Adds a new column `issue.cascade_plan_url TEXT NULL` so a multica issue
-- can declare "this work is governed by the plan at <URL>" — a self-published
-- signal that the cascade worker uses (next migration step + worker.go) to
-- decide whether a pr_merged event should wake the agent.
--
-- Parallel primitive to `cascade_state` from migration 072: cascade_state is
-- set by the PUL-102 approval-flow when an issue enters a managed cascade;
-- cascade_plan_url is the self-publishing path, written by `/publish-plan`
-- (multica skill) after pushing a plan to the plans-repo. Either alone
-- enables spawn; the two are complementary, not exclusive.
--
-- Also extends cascade_retrigger.action CHECK with 'single_pr_no_spawn' —
-- the new outcome worker.processOne records when an issue has neither
-- cascade primitive set and the event is therefore deploy-flip-only.
-- Postgres has no ADD VALUE for CHECK, so we DROP + ADD with the full
-- value set spelled out.

BEGIN;

ALTER TABLE issue
    ADD COLUMN cascade_plan_url TEXT NULL;

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
            'queued_pending',
            'single_pr_no_spawn'
        ));

COMMIT;
