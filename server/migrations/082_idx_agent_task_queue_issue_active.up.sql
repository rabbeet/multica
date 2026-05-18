-- PUL-180 ownership chip: partial-index for the per-issue active-task
-- lookup that backs the Inbox ownership signal.
--
-- The existing indices on agent_task_queue cover adjacent windows but
-- not this one:
--   idx_agent_task_queue_issue_id          — btree(issue_id), covers
--     every status (including completed/failed/cancelled), so on a hot
--     ticket with 50+ historical rows the per-issue scan reads all of
--     them and filters by status downstream;
--   idx_one_pending_task_per_issue_agent   — UNIQUE-partial on
--     (issue_id, agent_id) WHERE status IN ('queued','dispatched') —
--     deliberately excludes 'running' to allow the schedule-then-run
--     state transition to keep a single pending row per agent;
--   idx_agent_task_queue_pending           — (agent_id, priority DESC,
--     created_at) for the worker-side claim path, not issue-keyed.
--
-- This index is the missing one: (issue_id, created_at DESC) WHERE
-- status IN ('queued','dispatched','running'). The ownership-chip
-- subquery does exactly DISTINCT ON (issue_id) ORDER BY created_at
-- DESC over that predicate, so this index makes the per-issue lookup
-- bounded by the number of currently-active tasks for that issue
-- (typically 0-2) instead of the full task history for the issue.
--
-- Rationale for promoting from "optional v1.1" to v1 default lives in
-- the PUL-180 plan, Storage section: without it the inbox ListInbox
-- p95 budget (+5ms vs post-PUL-177 baseline) is at risk on tickets
-- that accumulate many completed runs (PUL-148, PUL-166 series).
CREATE INDEX IF NOT EXISTS idx_agent_task_queue_issue_active
    ON public.agent_task_queue (issue_id, created_at DESC)
    WHERE status IN ('queued', 'dispatched', 'running');
