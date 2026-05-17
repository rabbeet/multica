-- PUL-148 — switch cascade loop-guard query from pr_url to issue_id.
--
-- The pre-PUL-148 query keyed off pr_url, leaning on
-- idx_cascade_retrigger_loop_guard (pr_url, head_sha, fired_at, action)
-- from migration 072. After PR #21's adapter relaxation, workflow_run
-- events without populated pull_requests reach the worker carrying
-- pr_url=""; the worker resolver (commits/{sha}/pulls) usually
-- back-fills pr_url, but on a transient API failure / 0-or-many
-- result the loop-guard could collapse unrelated PRs into the
-- empty-pr_url bucket and false-trip after 3 distinct head_shas.
--
-- Switching to issue_id is robust regardless of resolver outcome.
-- The worker has already resolved issue_id by the time this query
-- runs (resolveIssue() populates cascade_retrigger.issue_id before
-- processOne()).
--
-- The old idx_cascade_retrigger_loop_guard stays in place — its
-- trailing columns may help analytics / reconciliation queries that
-- still filter by pr_url. Dropping it is a separate cleanup PR with
-- its own risk window.

CREATE INDEX IF NOT EXISTS idx_cascade_retrigger_loop_guard_by_issue
    ON cascade_retrigger (issue_id, action, fired_at DESC);
