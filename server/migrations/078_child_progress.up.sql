-- PUL-164: auto-mirror child issue status transitions to parent.
--
-- When an issue with parent_issue_id IS NOT NULL changes status (and the
-- transition is not in the noisy waiting↔in_progress pair), a system-authored
-- comment of type='child_progress' is posted on each ancestor up to a hard
-- depth limit of 5. Comments are emitted asynchronously by a 5s-tick worker
-- (cmd/server/child_progress_scheduler.go) that drains
-- issue_child_progress_outbox; the status-update transaction only inserts the
-- outbox row, so the user-visible status flip is decoupled from the comment
-- pipeline and stays fast / failure-isolated.
--
-- Schema additions:
--   1. comment.type CHECK extension — add 'child_progress'.
--   2. comment.meta JSONB column — generic structured payload keyed by
--      meta.kind discriminator ('child_progress' for v1, future types share).
--   3. comment.source_history_id BIGINT NULL — pointer to the
--      issue_status_history row that produced this fan-out comment. Real
--      column (not jsonb path) for clean idempotency index and read plans.
--   4. UNIQUE partial index on (source_history_id, issue_id) WHERE
--      type='child_progress' — idempotency contract for worker retries.
--   5. issue_child_progress_outbox table — at-least-once durable queue.

BEGIN;

-- (1) comment.type whitelist extension.
ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN (
        'comment',
        'status_change',
        'progress_update',
        'system',
        'wake_up',
        'child_progress'   -- new (PUL-164)
    ));

-- (2) comment.meta JSONB. Generic payload for non-comment types. v1 consumer
--     is child_progress; PUL-154 wake_up could migrate to it later without
--     schema churn. Default '{}' so existing rows remain valid.
ALTER TABLE comment ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}'::jsonb;

-- (3) source_history_id. NULL for all non-child_progress comments. Points at
--     issue_status_history.id (BIGSERIAL). No FK — issue_status_history rows
--     are append-only and not deleted independently of the issue (CASCADE on
--     issue delete handles the issue-deleted case).
ALTER TABLE comment ADD COLUMN IF NOT EXISTS source_history_id BIGINT NULL;

-- (4) Idempotency contract for the outbox worker. Two ancestor comments for
--     the same status change land with the same source_history_id but
--     different issue_id (the ancestor each comment is attached to), so the
--     index is (source_history_id, issue_id). WHERE clause keeps the index
--     small — only child_progress rows participate.
CREATE UNIQUE INDEX IF NOT EXISTS comment_child_progress_unique
    ON comment (source_history_id, issue_id)
    WHERE type = 'child_progress' AND source_history_id IS NOT NULL;

-- (5) issue_child_progress_outbox: at-least-once queue. The status-update
--     transaction inserts one row per fan-out event; the worker drains rows
--     with FOR UPDATE SKIP LOCKED, calls CommentService.Create per ancestor,
--     and marks the row processed. Failed rows retry on subsequent ticks up
--     to retry_count=10 before being parked as 'failed'.
--
--     ancestor_chain is frozen at INSERT time: even if a user re-parents the
--     child between status flip and fan-out, the audit-honest answer is "the
--     ancestors that existed at flip time get notified."
CREATE TABLE IF NOT EXISTS issue_child_progress_outbox (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    source_history_id   BIGINT NOT NULL,
    child_issue_id      UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    ancestor_chain      UUID[] NOT NULL,
    prev_status         TEXT NOT NULL,
    new_status          TEXT NOT NULL,
    actor_id            UUID,
    actor_type          TEXT CHECK (actor_type IS NULL OR actor_type IN ('member', 'agent', 'system')),
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'processing', 'processed', 'failed')),
    retry_count         INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at          TIMESTAMPTZ,
    processed_at        TIMESTAMPTZ,

    -- ancestor_chain must be a non-empty array (otherwise this row was an
    -- orphan child and never should have been enqueued).
    CHECK (array_length(ancestor_chain, 1) >= 1),

    -- Cap chain length at 5 hops (matches buildAncestorChain CTE limit).
    CHECK (array_length(ancestor_chain, 1) <= 5)
);

-- Hot path: claim due rows. Only pending + not-yet-claimed rows matter.
CREATE INDEX IF NOT EXISTS idx_issue_child_progress_outbox_pending
    ON issue_child_progress_outbox(created_at)
    WHERE status = 'pending' AND claimed_at IS NULL;

-- Recovery: claims that did not finalize within the tick window are reset by
-- the worker on next tick / startup.
CREATE INDEX IF NOT EXISTS idx_issue_child_progress_outbox_recovery
    ON issue_child_progress_outbox(claimed_at)
    WHERE status = 'processing' AND claimed_at IS NOT NULL;

COMMIT;
