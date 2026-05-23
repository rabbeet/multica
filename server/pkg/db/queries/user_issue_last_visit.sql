-- PUL-239 cross-device last-visit sync (Mission Control delta-mode).
-- See migrations/089_user_issue_last_visit.up.sql for the table.

-- name: UpsertUserIssueLastVisit :exec
-- Mark a (workspace, user, issue) tuple as visited now. Idempotent —
-- subsequent calls bump the timestamp via ON CONFLICT.
INSERT INTO user_issue_last_visit (workspace_id, user_id, user_type, issue_id, last_visited_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (workspace_id, user_id, user_type, issue_id) DO UPDATE
SET last_visited_at = excluded.last_visited_at;

-- name: ListUserIssueLastVisits :many
-- Return the full map for one (workspace, user) tuple. Mission Control
-- hits this once on mount and stitches it into the local zustand store
-- via useLastVisitStore.hydrateFromServer().
SELECT issue_id, last_visited_at
FROM user_issue_last_visit
WHERE workspace_id = $1 AND user_id = $2 AND user_type = $3
ORDER BY last_visited_at DESC;
