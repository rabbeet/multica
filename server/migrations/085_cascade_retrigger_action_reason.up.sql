-- PUL-198 Part 1: action_reason column for cascade_retrigger.
--
-- Pairs with cascade_retrigger.action to record the *why* behind each
-- worker outcome — task_id for spawn, loop-guard count + window for
-- loop_guard_skip, ErrSpawnGated wrap text for scope_filter_skip,
-- deploy_flip outcome prefix when pr_merged also triggered the
-- PUL-194 status flip. Read by `multica issue cascade <id>` so
-- operators can answer "why did cascade not spawn for this PR" in
-- seconds instead of digging through server logs.
--
-- TEXT NULL because pre-PUL-198 rows have no reason recorded and we
-- do not back-fill. New writes from worker.markRow always populate it.

BEGIN;

ALTER TABLE cascade_retrigger
    ADD COLUMN action_reason TEXT NULL;

COMMIT;
