-- PUL-182: partial index on `issue_skill_state.updated_at` filtered by
-- `status = 'in_progress'`. Backs the TTL cleanup query
-- `CleanupStaleIssueSkillStates` (server/pkg/db/queries/issue_skill_state.sql)
-- which runs every 15 minutes and otherwise would seq-scan the full
-- table — and the table grows permanently with `done` rows that
-- SkillHistory depends on.
--
-- Partial index keeps the on-disk footprint tiny: only rows still
-- `in_progress` are indexed, and that set is bounded by "how many
-- active skill runs exist concurrently" (single digits in practice,
-- bounded by user×agent pairs). Maintenance cost on Upsert is
-- one B-tree insert when entering `in_progress`, one delete when
-- transitioning to `done`.
--
-- Index column = `updated_at`, not `started_at`, because the cleanup
-- query filters on `updated_at < now() - ttl` (see query comment for
-- the started_at-vs-updated_at rationale tied to the Upsert behaviour).

BEGIN;

CREATE INDEX idx_issue_skill_state_in_progress_updated
    ON issue_skill_state (updated_at)
    WHERE status = 'in_progress';

COMMIT;
