-- name: ListIssues :many
SELECT id, workspace_id, title, description, status, priority,
       assignee_type, assignee_id, creator_type, creator_id,
       parent_issue_id, position, due_date, created_at, updated_at, number, project_id
FROM issue
WHERE workspace_id = $1
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority'))
  AND (sqlc.narg('assignee_id')::uuid IS NULL OR assignee_id = sqlc.narg('assignee_id'))
  AND (sqlc.narg('assignee_ids')::uuid[] IS NULL OR assignee_id = ANY(sqlc.narg('assignee_ids')::uuid[]))
  AND (sqlc.narg('creator_id')::uuid IS NULL OR creator_id = sqlc.narg('creator_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
ORDER BY position ASC, created_at DESC
LIMIT $2 OFFSET $3;

-- name: GetIssue :one
SELECT * FROM issue
WHERE id = $1;

-- name: GetIssueInWorkspace :one
SELECT * FROM issue
WHERE id = $1 AND workspace_id = $2;

-- name: GetIssueForUpdate :one
-- Locks the issue row for the duration of the surrounding tx. Used by the
-- comment-create handler (PUL-13 P1 hook_comment auto-flip) to serialize
-- concurrent writers (other comments, manual status sets) against this issue.
-- The lock is released when the tx commits or rolls back.
SELECT * FROM issue
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: CreateIssue :one
INSERT INTO issue (
    workspace_id, title, description, status, priority,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, position, due_date, number, project_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
) RETURNING *;

-- name: GetIssueByNumber :one
SELECT * FROM issue
WHERE workspace_id = $1 AND number = $2;

-- name: UpdateIssue :one
UPDATE issue SET
    title = COALESCE(sqlc.narg('title'), title),
    description = COALESCE(sqlc.narg('description'), description),
    status = COALESCE(sqlc.narg('status'), status),
    priority = COALESCE(sqlc.narg('priority'), priority),
    assignee_type = sqlc.narg('assignee_type'),
    assignee_id = sqlc.narg('assignee_id'),
    position = COALESCE(sqlc.narg('position'), position),
    due_date = sqlc.narg('due_date'),
    parent_issue_id = sqlc.narg('parent_issue_id'),
    project_id = sqlc.narg('project_id'),
    cascade_plan_url = sqlc.narg('cascade_plan_url'),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateIssueStatus :one
UPDATE issue SET
    status = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateIssueStatusToDeployed :one
-- Atomic transition to 'deployed' that also stamps the lifecycle marker
-- deployed_at on first arrival. COALESCE preserves deployed_at on a re-deploy
-- after a bug-fix loop (deployed → developing → deployed) as evidence the
-- issue was on prod at least once. Callers flipping to non-deployed states
-- continue to use UpdateIssueStatus; this query is the dedicated entry point
-- for the PUL-194 server-side auto-flip in cascade.ApplyDeployFlip.
UPDATE issue SET
    status = 'deployed',
    deployed_at = COALESCE(deployed_at, now()),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CreateIssueWithOrigin :one
INSERT INTO issue (
    workspace_id, title, description, status, priority,
    assignee_type, assignee_id, creator_type, creator_id,
    parent_issue_id, position, due_date, number, project_id,
    origin_type, origin_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
    sqlc.narg('origin_type'), sqlc.narg('origin_id')
) RETURNING *;

-- name: DeleteIssue :exec
DELETE FROM issue WHERE id = $1;

-- name: ListOpenIssues :many
SELECT id, workspace_id, title, description, status, priority,
       assignee_type, assignee_id, creator_type, creator_id,
       parent_issue_id, position, due_date, created_at, updated_at, number, project_id
FROM issue
WHERE workspace_id = $1
  AND status NOT IN ('done', 'cancelled')
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority'))
  AND (sqlc.narg('assignee_id')::uuid IS NULL OR assignee_id = sqlc.narg('assignee_id'))
  AND (sqlc.narg('assignee_ids')::uuid[] IS NULL OR assignee_id = ANY(sqlc.narg('assignee_ids')::uuid[]))
  AND (sqlc.narg('creator_id')::uuid IS NULL OR creator_id = sqlc.narg('creator_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
ORDER BY position ASC, created_at DESC;

-- name: CountIssues :one
SELECT count(*) FROM issue
WHERE workspace_id = $1
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority'))
  AND (sqlc.narg('assignee_id')::uuid IS NULL OR assignee_id = sqlc.narg('assignee_id'))
  AND (sqlc.narg('assignee_ids')::uuid[] IS NULL OR assignee_id = ANY(sqlc.narg('assignee_ids')::uuid[]))
  AND (sqlc.narg('creator_id')::uuid IS NULL OR creator_id = sqlc.narg('creator_id'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'));

-- name: ListChildIssues :many
SELECT * FROM issue
WHERE parent_issue_id = $1
ORDER BY position ASC, created_at DESC;

-- name: GetIssueByOrigin :one
-- Finds the issue stamped with a specific (origin_type, origin_id) pair.
-- Used by quick-create completion to deterministically locate the issue
-- produced by a given agent_task_queue.id — robust against concurrent
-- issue creates by the same agent (assignment task + quick-create both
-- running with max_concurrent_tasks > 1).
SELECT * FROM issue
WHERE workspace_id = $1
  AND origin_type = $2
  AND origin_id = $3
LIMIT 1;

-- name: CountCreatedIssueAssignees :many
-- Count assignees on issues created by a specific user.
SELECT
  assignee_type,
  assignee_id,
  COUNT(*)::bigint as frequency
FROM issue
WHERE workspace_id = $1
  AND creator_id = $2
  AND creator_type = 'member'
  AND assignee_type IS NOT NULL
  AND assignee_id IS NOT NULL
GROUP BY assignee_type, assignee_id;

-- name: ChildIssueProgress :many
SELECT parent_issue_id,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE status IN ('done', 'cancelled'))::bigint AS done
FROM issue
WHERE workspace_id = $1
  AND parent_issue_id IS NOT NULL
GROUP BY parent_issue_id;

-- SearchIssues: moved to handler (dynamic SQL for multi-word search support).

-- name: MarkIssueFirstExecuted :one
-- Flips first_executed_at from NULL to now() atomically. Returns the row if
-- this was the first time the issue was executed; no rows otherwise. The
-- analytics issue_executed event fires exactly when this returns a row —
-- retries and re-assignments hit the WHERE clause and no-op.
UPDATE issue
SET first_executed_at = now()
WHERE id = $1 AND first_executed_at IS NULL
RETURNING id, workspace_id, creator_type, creator_id, first_executed_at;

-- name: ListWorkspaceActionInbox :many
-- PUL-231 Mission Control workspace inbox feed. One row per active
-- issue in the workspace, joined with the latest agent comment and
-- latest skill state. Returning the comment body in the same payload
-- lets the client parse open agent questions / ack-gated command
-- blocks with extractAgentActions() — no N+1 per-issue comment fetch.
--
-- Active = status IN ('todo','in_progress','waiting','planned','developing').
-- Ordered to surface "agent posted, waiting on user" first: issues whose
-- latest event was an agent comment float above issues whose latest
-- event was a status change. Inside each group, most-recently-updated
-- wins, so the user's eye lands on what changed last.
--
-- LIMIT 100 caps result size for the single-user MVP; pagination
-- arrives with PR2.5 if a workspace ever needs more.
WITH latest_agent_comment AS (
    SELECT DISTINCT ON (c.issue_id)
        c.issue_id,
        c.id          AS comment_id,
        c.content     AS comment_content,
        c.created_at  AS comment_created_at,
        c.author_id   AS comment_author_id
    FROM comment c
    WHERE c.type = 'comment'
      AND c.author_type = 'agent'
    ORDER BY c.issue_id, c.created_at DESC
),
latest_skill AS (
    SELECT DISTINCT ON (issue_id)
        issue_id,
        skill_slug,
        status     AS skill_status,
        updated_at AS skill_updated_at
    FROM issue_skill_state
    ORDER BY issue_id, updated_at DESC
)
SELECT
    i.id, i.workspace_id, i.number, i.title, i.status, i.priority,
    i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
    i.updated_at, i.created_at,
    lac.comment_id          AS latest_agent_comment_id,
    lac.comment_content     AS latest_agent_comment_content,
    lac.comment_created_at  AS latest_agent_comment_at,
    lac.comment_author_id   AS latest_agent_author_id,
    ls.skill_slug,
    ls.skill_status,
    ls.skill_updated_at
FROM issue i
LEFT JOIN latest_agent_comment lac ON lac.issue_id = i.id
LEFT JOIN latest_skill         ls  ON ls.issue_id  = i.id
WHERE i.workspace_id = $1
  AND i.status IN ('todo','in_progress','waiting','planned','developing')
ORDER BY
    -- Issues with a recent agent comment surface first — that's where
    -- the user's chip taps land. Then by most-recent activity.
    (lac.comment_created_at IS NOT NULL) DESC,
    GREATEST(lac.comment_created_at, i.updated_at) DESC NULLS LAST
LIMIT 100;
