-- PUL-177: per-(issue, skill) state queries for Inbox phase + last-applied
-- chips and the SkillHistory section. See
-- plans://Multica/2026-05-18-pul-177-inbox-skill-progress-indicators.md.

-- name: UpsertIssueSkillState :one
-- Single write path used by both the explicit API endpoint and the
-- comment auto-detect path. Two transitions matter:
--
--   1. Fresh in_progress / new row:
--        started_at = now(), completed_at = NULL.
--
--   2. Repeat in_progress on an already-in_progress row (a `/skill`
--      mention while the skill is still running):
--        started_at preserved (don't reset the "started at" clock for
--        every status ping), updated_at bumped (so the row stays the
--        latest one in the Inbox LATERAL LIMIT 1 lookup).
--
--   3. done after in_progress (skill finished):
--        completed_at = now(), updated_at bumped.
--
--   4. in_progress after done (skill being re-run from scratch):
--        started_at reset to now(), completed_at cleared, updated_at
--        bumped — this is the only path that overwrites a done state.
--        The auto-detect path is *not* allowed to take this branch
--        (the handler enforces "don't overwrite done from
--        comment_auto"); the explicit API is.
INSERT INTO issue_skill_state (
    issue_id, skill_slug, status, started_at, completed_at, updated_at, source
)
VALUES (
    @issue_id, @skill_slug, @status,
    now(),
    CASE WHEN @status = 'done' THEN now() ELSE NULL END,
    now(),
    @source
)
ON CONFLICT (issue_id, skill_slug) DO UPDATE
SET
    status = EXCLUDED.status,
    -- Reset started_at only on done → in_progress transition.
    started_at = CASE
        WHEN issue_skill_state.status = 'done' AND EXCLUDED.status = 'in_progress'
            THEN now()
        ELSE issue_skill_state.started_at
    END,
    -- Stamp completed_at when entering done; clear it when leaving done.
    completed_at = CASE
        WHEN EXCLUDED.status = 'done' THEN now()
        ELSE NULL
    END,
    updated_at = now(),
    source = EXCLUDED.source
RETURNING *;

-- name: GetIssueSkillState :one
SELECT * FROM issue_skill_state
WHERE issue_id = $1 AND skill_slug = $2;

-- name: ListIssueSkillStates :many
-- Full skill history for one issue, used by the SkillHistory panel on
-- the issue detail page (issue-detail.tsx). Ordered by updated_at DESC
-- so the freshest entry is on top.
SELECT skill_slug, status, started_at, completed_at, updated_at, source
FROM issue_skill_state
WHERE issue_id = $1
ORDER BY updated_at DESC;

-- name: DeleteIssueSkillState :exec
DELETE FROM issue_skill_state
WHERE issue_id = $1 AND skill_slug = $2;

-- name: CleanupStaleIssueSkillStates :many
-- PUL-182: delete `in_progress` rows whose `started_at` is older than the
-- supplied TTL. A skill that crashed before calling `done` (claude-code
-- session crash, agent worktree force-killed, rate-limit on the claude
-- API) leaves an "in_progress" chip in the Inbox forever; this query is
-- the cron-side cleanup invoked from runSkillStateCleanupScheduler
-- every 15 minutes (default TTL 24h, overridable via
-- ISSUE_SKILL_STATE_STALE_TTL).
--
-- `make_interval(secs => ...)` takes the typed int64 directly so we do
-- not have to round-trip through text concatenation. PG evaluates
-- `now() - interval` once per row at executor time.
--
-- `status = 'in_progress'` is intentionally strict — `done` rows are
-- the historical record used by LastSkillChip and SkillHistory and must
-- survive indefinitely. If PUL-177's CHECK constraint ever gains a
-- `stale` enum value (out of scope for v1), this query needs an
-- explicit re-decision about whether `stale` should also be swept.
--
-- RETURNING the deleted rows so the scheduler can log per-row debug
-- info ("which phantom chip did we just remove?") without a separate
-- SELECT. The slice is empty on a no-op tick, so the scheduler's
-- `slog.Info` only fires when something was actually cleaned.
DELETE FROM issue_skill_state
WHERE status = 'in_progress'
  AND started_at < now() - make_interval(secs => @ttl_seconds::bigint)
RETURNING issue_id, skill_slug, started_at;
