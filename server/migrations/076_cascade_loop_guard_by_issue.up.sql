-- PUL-148 — switch cascade loop-guard query from pr_url to issue_id.
--
-- The old query (`WHERE pr_url = $1 AND action = 'spawn' AND fired_at > ...`)
-- relied on idx_cascade_retrigger_loop_guard (pr_url, head_sha, fired_at, action)
-- from migration 072. That key fell apart on real GitHub deliveries where
-- workflow_run.pull_requests = [] — every empty-pr_url event collapsed into
-- a single bucket, false-tripping the guard across unrelated PRs.
--
-- The new query keys off issue_id instead. issue_id is always set by the
-- time the row reaches the loop-guard check (worker resolves it from
-- pr_title / branch via cascade.LookupIssueIdentifier before this query).
-- This index serves the new query shape directly.
--
-- The old idx_cascade_retrigger_loop_guard stays in place — its trailing
-- columns may help analytics / reconciliation queries that still filter by
-- pr_url. Dropping it would force a separate migration with its own risk
-- window.

CREATE INDEX IF NOT EXISTS idx_cascade_retrigger_loop_guard_by_issue
    ON cascade_retrigger (issue_id, action, fired_at DESC);
