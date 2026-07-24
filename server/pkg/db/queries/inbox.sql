-- name: ListInboxItems :many
-- Latest page of dedup'd inbox rows for a recipient, newest first.
--
-- PUL-177 added the phase + last-applied-skill chips. PUL-180 adds the
-- third Inbox chip — ownership ("me" | "agent" | "waiting") — derived
-- server-side from three additional signals joined per issue:
--
--   active_tasks  — latest currently-active agent_task_queue row
--                   (status IN queued/dispatched/running). Drives
--                   ownership='agent' + meta.agent_name + meta.since.
--                   Indexed by idx_agent_task_queue_issue_active
--                   (PUL-180 migration 082).
--   last_status   — most recent issue_status_history.created_at.
--                   Drives meta.since for ownership='me'. Indexed by
--                   ix_issue_status_history_issue.
--   last_comment  — most recent comment.created_at WHERE
--                   type='comment'. The type filter excludes the
--                   auto-generated status_change/system/wake_up/
--                   child_progress rows so the "ход за мной 5 мин
--                   назад" tooltip reflects real user/agent action,
--                   not bookkeeping events. Indexed by
--                   idx_comment_issue_keyset.
--
-- Pattern note — we stay with CTE + LEFT JOIN (not LATERAL/scalar
-- subqueries) for the same sqlc-nullability reason PUL-177 used:
-- LEFT JOIN'd CTE columns infer as nullable, which matches the
-- semantics (issues with no history → NULL), while LATERAL infers
-- the underlying NOT NULL column type and breaks Scan() on legacy
-- issues. The DISTINCT ON form is index-driven by the three indices
-- above.
--
-- PUL-481 added server-side dedup + LIMIT. Historically this returned
-- every non-archived inbox_item and let the client dedupe — active
-- accounts pushed the payload past 13 MB (3716 items for vadim, iOS
-- Safari crashed). Dedup now happens in the `representatives` CTE by
-- COALESCE(issue_id, id): one row per issue (or per standalone item
-- when issue_id is NULL), matching the UI's list semantics. The
-- ArchiveAllReadInbox / ArchiveCompletedInbox mutations already used
-- the same key — this brings ListInboxItems into alignment.
WITH representatives AS (
    SELECT DISTINCT ON (COALESCE(rep.issue_id, rep.id))
        rep.id, rep.created_at
    FROM inbox_item rep
    WHERE rep.workspace_id = $1
      AND rep.recipient_type = $2
      AND rep.recipient_id = $3
      AND rep.archived = false
    ORDER BY COALESCE(rep.issue_id, rep.id), rep.created_at DESC
),
page AS (
    SELECT id, created_at FROM representatives
    ORDER BY created_at DESC, id DESC
    LIMIT sqlc.arg('lim')::int
),
latest_skills AS (
    SELECT DISTINCT ON (issue_id)
        issue_id,
        skill_slug,
        status,
        started_at,
        completed_at,
        updated_at
    FROM issue_skill_state
    ORDER BY issue_id, updated_at DESC
),
active_tasks AS (
    SELECT DISTINCT ON (atq.issue_id)
        atq.issue_id,
        atq.agent_id,
        a.name AS agent_name,
        atq.status,
        atq.dispatched_at,
        atq.started_at
    FROM agent_task_queue atq
    JOIN agent a ON a.id = atq.agent_id
    WHERE atq.status IN ('queued', 'dispatched', 'running')
      AND atq.issue_id IS NOT NULL
    ORDER BY atq.issue_id, atq.created_at DESC
),
last_status AS (
    SELECT DISTINCT ON (issue_id)
        issue_id,
        created_at AS last_status_at
    FROM issue_status_history
    ORDER BY issue_id, created_at DESC
),
last_comment AS (
    SELECT DISTINCT ON (issue_id)
        issue_id,
        created_at AS last_comment_at
    FROM comment
    WHERE type = 'comment'
    ORDER BY issue_id, created_at DESC
)
SELECT i.*,
       iss.status     AS issue_status,
       iss.updated_at AS issue_updated_at,
       ls.skill_slug   AS latest_skill_slug,
       ls.status       AS latest_skill_status,
       ls.started_at   AS latest_skill_started_at,
       ls.completed_at AS latest_skill_completed_at,
       ls.updated_at   AS latest_skill_updated_at,
       at.agent_id      AS active_task_agent_id,
       at.agent_name    AS active_task_agent_name,
       at.status        AS active_task_status,
       at.dispatched_at AS active_task_dispatched_at,
       at.started_at    AS active_task_started_at,
       lst.last_status_at,
       lc.last_comment_at
FROM page p
JOIN inbox_item i ON i.id = p.id
LEFT JOIN issue iss ON iss.id = i.issue_id
LEFT JOIN latest_skills ls ON ls.issue_id = i.issue_id
LEFT JOIN active_tasks  at ON at.issue_id = i.issue_id
LEFT JOIN last_status   lst ON lst.issue_id = i.issue_id
LEFT JOIN last_comment  lc  ON lc.issue_id = i.issue_id
ORDER BY i.created_at DESC, i.id DESC;

-- name: ListInboxItemsBefore :many
-- Older page: same row shape as ListInboxItems, scoped to representatives
-- strictly older than the given (created_at, id) keyset cursor. PUL-481
-- adds infinite-scroll for accounts with more than one page of inbox.
WITH representatives AS (
    SELECT DISTINCT ON (COALESCE(rep.issue_id, rep.id))
        rep.id, rep.created_at
    FROM inbox_item rep
    WHERE rep.workspace_id = $1
      AND rep.recipient_type = $2
      AND rep.recipient_id = $3
      AND rep.archived = false
    ORDER BY COALESCE(rep.issue_id, rep.id), rep.created_at DESC
),
page AS (
    SELECT id, created_at FROM representatives
    WHERE (created_at, id) < ($4::timestamptz, $5::uuid)
    ORDER BY created_at DESC, id DESC
    LIMIT sqlc.arg('lim')::int
),
latest_skills AS (
    SELECT DISTINCT ON (issue_id)
        issue_id, skill_slug, status, started_at, completed_at, updated_at
    FROM issue_skill_state
    ORDER BY issue_id, updated_at DESC
),
active_tasks AS (
    SELECT DISTINCT ON (atq.issue_id)
        atq.issue_id, atq.agent_id, a.name AS agent_name,
        atq.status, atq.dispatched_at, atq.started_at
    FROM agent_task_queue atq
    JOIN agent a ON a.id = atq.agent_id
    WHERE atq.status IN ('queued', 'dispatched', 'running')
      AND atq.issue_id IS NOT NULL
    ORDER BY atq.issue_id, atq.created_at DESC
),
last_status AS (
    SELECT DISTINCT ON (issue_id)
        issue_id, created_at AS last_status_at
    FROM issue_status_history
    ORDER BY issue_id, created_at DESC
),
last_comment AS (
    SELECT DISTINCT ON (issue_id)
        issue_id, created_at AS last_comment_at
    FROM comment
    WHERE type = 'comment'
    ORDER BY issue_id, created_at DESC
)
SELECT i.*,
       iss.status     AS issue_status,
       iss.updated_at AS issue_updated_at,
       ls.skill_slug   AS latest_skill_slug,
       ls.status       AS latest_skill_status,
       ls.started_at   AS latest_skill_started_at,
       ls.completed_at AS latest_skill_completed_at,
       ls.updated_at   AS latest_skill_updated_at,
       at.agent_id      AS active_task_agent_id,
       at.agent_name    AS active_task_agent_name,
       at.status        AS active_task_status,
       at.dispatched_at AS active_task_dispatched_at,
       at.started_at    AS active_task_started_at,
       lst.last_status_at,
       lc.last_comment_at
FROM page p
JOIN inbox_item i ON i.id = p.id
LEFT JOIN issue iss ON iss.id = i.issue_id
LEFT JOIN latest_skills ls ON ls.issue_id = i.issue_id
LEFT JOIN active_tasks  at ON at.issue_id = i.issue_id
LEFT JOIN last_status   lst ON lst.issue_id = i.issue_id
LEFT JOIN last_comment  lc  ON lc.issue_id = i.issue_id
ORDER BY i.created_at DESC, i.id DESC;

-- name: GetInboxItem :one
SELECT * FROM inbox_item
WHERE id = $1;

-- name: GetInboxItemInWorkspace :one
SELECT * FROM inbox_item
WHERE id = $1 AND workspace_id = $2;

-- name: CreateInboxItem :one
INSERT INTO inbox_item (
    workspace_id, recipient_type, recipient_id,
    type, severity, issue_id, title, body,
    actor_type, actor_id, details
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: MarkInboxRead :one
UPDATE inbox_item SET read = true
WHERE id = $1
RETURNING *;

-- name: ArchiveInboxItem :one
UPDATE inbox_item SET archived = true
WHERE id = $1
RETURNING *;

-- name: ArchiveInboxByIssue :execrows
UPDATE inbox_item SET archived = true
WHERE workspace_id = $1 AND recipient_type = $2 AND recipient_id = $3 AND issue_id = $4 AND archived = false;

-- name: CountUnreadInbox :one
-- PUL-481 dedupes by COALESCE(issue_id, id) to match the UI's list
-- semantics: an issue with three unread notifications shows as one row
-- and counts once against the badge. Prior to this change the server
-- counted individual rows while the client counted dedup'd reps, so
-- the badge and the API disagreed for any user with sibling
-- notifications on the same issue (PUL-39 for archive was symmetric).
WITH representatives AS (
    SELECT DISTINCT ON (COALESCE(issue_id, id)) read
    FROM inbox_item
    WHERE workspace_id = $1
      AND recipient_type = $2
      AND recipient_id = $3
      AND archived = false
    ORDER BY COALESCE(issue_id, id), created_at DESC
)
SELECT count(*) FROM representatives WHERE read = false;

-- name: MarkAllInboxRead :execrows
UPDATE inbox_item SET read = true
WHERE workspace_id = $1 AND recipient_type = 'member' AND recipient_id = $2 AND archived = false AND read = false;

-- name: ArchiveAllInbox :execrows
UPDATE inbox_item SET archived = true
WHERE workspace_id = $1 AND recipient_type = 'member' AND recipient_id = $2 AND archived = false;

-- name: ArchiveAllReadInbox :execrows
-- Archive all inbox items belonging to "representatives" whose newest non-archived item
-- is read. A group is an issue (when issue_id is set) or a single standalone
-- item (when issue_id is NULL). This matches the inbox UI's dedup-by-issue
-- semantic: the list shows one row per issue keyed on the newest non-archived
-- inbox_item, so "Archive all read" should archive entire issues that the user
-- sees as read. Per-row archiving (the previous behavior) hid the read row but
-- exposed older unread siblings, flipping the issue from "read" to "unread"
-- in the list (PUL-39).
WITH representatives AS (
  SELECT DISTINCT ON (COALESCE(issue_id, id))
    issue_id,
    id AS representative_id,
    read AS representative_read
  FROM inbox_item
  WHERE workspace_id = $1
    AND recipient_type = 'member'
    AND recipient_id = $2
    AND archived = false
  ORDER BY COALESCE(issue_id, id), created_at DESC
)
UPDATE inbox_item AS i SET archived = true
WHERE i.workspace_id = $1
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2
  AND i.archived = false
  AND (
    (i.issue_id IS NOT NULL AND i.issue_id IN (
      SELECT r.issue_id FROM representatives r WHERE r.representative_read = true AND r.issue_id IS NOT NULL
    ))
    OR (i.issue_id IS NULL AND i.id IN (
      SELECT r.representative_id FROM representatives r WHERE r.representative_read = true AND r.issue_id IS NULL
    ))
  );

-- name: ArchiveCompletedInbox :execrows
UPDATE inbox_item i SET archived = true
WHERE i.workspace_id = $1 AND i.recipient_type = 'member' AND i.recipient_id = $2 AND i.archived = false
  AND i.issue_id IN (SELECT id FROM issue WHERE status IN ('done', 'cancelled'));
