-- PUL-239 Cross-device last-visit sync for Mission Control delta-mode
-- (PUL-231 PR2/PR3 shipped this as workspace-scoped localStorage; the
-- server table promotes it to per-user, cross-device).
--
-- Composite PK on (workspace_id, user_id, user_type, issue_id) covers
-- the only access patterns: "all my visits in this workspace" (Mission
-- Control mount) and "mark this one visit now" (upsert).
-- user_type stays text to match the rest of the schema (member vs agent).
CREATE TABLE IF NOT EXISTS user_issue_last_visit (
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL,
    user_type       TEXT NOT NULL CHECK (user_type IN ('member', 'agent')),
    issue_id        UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    last_visited_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id, user_type, issue_id)
);

-- Reverse-lookup index for the rare "anyone visited this issue lately"
-- query — used only by Phase 3 collaboration polish, but the index is
-- cheap and adding it later behind CONCURRENTLY is messy.
CREATE INDEX IF NOT EXISTS idx_user_issue_last_visit_issue
  ON user_issue_last_visit (issue_id, last_visited_at DESC);
