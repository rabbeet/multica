-- PUL-231 Mission Control workspace inbox needs a single SQL query
-- (ListWorkspaceActionInbox) that joins issue ⇄ latest agent comment
-- per issue. The "latest agent comment per issue" CTE walks comment
-- under a DISTINCT ON (c.issue_id) … ORDER BY c.issue_id, c.created_at
-- DESC scan. Without an index on (issue_id, created_at DESC) filtered
-- by type='comment' AND actor_type='agent', the planner falls back to
-- a sort over the full comment table per workspace — fine for small
-- demos, painful as the workspace grows past a few thousand comments.
--
-- Partial index keeps the on-disk footprint tight (most comments meet
-- the predicate, but the explicit filter lets Postgres skip the
-- predicate check at scan time).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comment_issue_agent_recent
  ON comment (issue_id, created_at DESC)
  WHERE type = 'comment' AND actor_type = 'agent';
