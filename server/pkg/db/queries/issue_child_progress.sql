-- PUL-164: auto-mirror child issue status transitions to parent.
-- Outbox queue + claim/finalize lifecycle for the child_progress fan-out
-- worker. See server/migrations/078_child_progress.up.sql for the schema
-- rationale.

-- name: EnqueueChildProgressFanout :one
-- Insert a fan-out outbox row keyed by the status_history.id. The
-- ancestor_chain is built inline via recursive CTE on the issue table — up
-- to 5 hops, terminating on parent_issue_id IS NULL or cycle detection (CTE
-- visits each node at most once by construction). If the child has no
-- parent the INSERT is skipped via the array_length(>=1) check, which raises
-- a check violation the caller treats as "no fan-out needed" (the noisy
-- case is already filtered by the service before this call, so reaching
-- here with an orphan child is a programmer error worth surfacing).
WITH RECURSIVE ancestors AS (
    SELECT issue.id, issue.parent_issue_id, 1 AS depth, ARRAY[issue.id] AS visited
      FROM issue
      WHERE issue.id = @child_issue_id AND issue.parent_issue_id IS NOT NULL
    UNION ALL
    SELECT i.id, i.parent_issue_id, a.depth + 1, a.visited || i.id
      FROM issue i
      JOIN ancestors a ON i.id = a.parent_issue_id
      WHERE a.depth < 5
        AND NOT i.id = ANY(a.visited)
),
chain AS (
    SELECT array_agg(parent_issue_id ORDER BY depth) AS ids
      FROM ancestors
      WHERE parent_issue_id IS NOT NULL
)
INSERT INTO issue_child_progress_outbox (
    workspace_id,
    source_history_id,
    child_issue_id,
    ancestor_chain,
    prev_status,
    new_status,
    actor_id,
    actor_type
)
SELECT
    @workspace_id,
    @source_history_id,
    @child_issue_id,
    ids,
    @prev_status,
    @new_status,
    sqlc.narg('actor_id')::UUID,
    sqlc.narg('actor_type')::TEXT
FROM chain
WHERE ids IS NOT NULL
RETURNING *;

-- name: ClaimDueChildProgressOutbox :many
-- Claim up to 100 pending rows for processing. FOR UPDATE SKIP LOCKED lets
-- multiple workers (we only run one in v1, but the contract holds) split
-- the work without blocking. Worker calls
-- MarkChildProgressOutboxProcessed (or MarkChildProgressOutboxFailed) per
-- row to finalize the claim.
UPDATE issue_child_progress_outbox
SET status = 'processing',
    claimed_at = now()
WHERE id IN (
    SELECT id FROM issue_child_progress_outbox
    WHERE status = 'pending' AND claimed_at IS NULL
    ORDER BY created_at
    LIMIT 100
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkChildProgressOutboxProcessed :one
-- Finalize a successful processing pass. Only succeeds if row is still
-- 'processing' to be retry-safe under a worker crash + recovery race.
UPDATE issue_child_progress_outbox
SET status = 'processed',
    processed_at = now()
WHERE id = @id AND status = 'processing'
RETURNING *;

-- name: MarkChildProgressOutboxFailed :one
-- Mark a row as failed after retry_count >= 10. Stays in 'failed' status
-- (no further retries) so operators can investigate.
UPDATE issue_child_progress_outbox
SET status = 'failed',
    last_error = @last_error,
    processed_at = now()
WHERE id = @id
RETURNING *;

-- name: BumpChildProgressOutboxRetry :one
-- Transient failure path: bump retry_count, release the claim so the next
-- tick picks the row back up. If retry_count exceeds 10, the caller
-- escalates to MarkChildProgressOutboxFailed instead.
UPDATE issue_child_progress_outbox
SET retry_count = retry_count + 1,
    status = 'pending',
    claimed_at = NULL,
    last_error = @last_error
WHERE id = @id AND status = 'processing'
RETURNING *;

-- name: ResetStuckChildProgressOutboxClaims :execrows
-- Recovery: any row that has been in 'processing' for more than 60s without
-- finalize is assumed to have lost its worker. Release the claim so the
-- next tick retries.
UPDATE issue_child_progress_outbox
SET status = 'pending',
    claimed_at = NULL
WHERE status = 'processing'
  AND claimed_at IS NOT NULL
  AND claimed_at < now() - interval '60 seconds';

-- name: GetChildProgressOutbox :one
SELECT * FROM issue_child_progress_outbox WHERE id = @id;

-- name: GetIssueAncestorChain :one
-- Public-facing form of the same CTE — returns the ancestor chain (up to 5
-- hops) for a given issue. Used by the service-layer wrapper that wants the
-- chain ahead of the outbox INSERT (e.g. to suppress fan-out when chain is
-- empty without raising a check violation).
WITH RECURSIVE ancestors AS (
    SELECT issue.id, issue.parent_issue_id, 1 AS depth, ARRAY[issue.id] AS visited
      FROM issue
      WHERE issue.id = @child_issue_id AND issue.parent_issue_id IS NOT NULL
    UNION ALL
    SELECT i.id, i.parent_issue_id, a.depth + 1, a.visited || i.id
      FROM issue i
      JOIN ancestors a ON i.id = a.parent_issue_id
      WHERE a.depth < 5
        AND NOT i.id = ANY(a.visited)
)
SELECT COALESCE(array_agg(parent_issue_id ORDER BY depth), ARRAY[]::UUID[])::UUID[] AS ancestor_chain
  FROM ancestors
  WHERE parent_issue_id IS NOT NULL;
