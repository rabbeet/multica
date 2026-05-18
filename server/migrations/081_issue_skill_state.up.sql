-- PUL-177: per-(issue, skill) state for Inbox phase + last-applied-skill chips
-- and the SkillHistory section on the issue detail page.
--
-- See plans://Multica/2026-05-18-pul-177-inbox-skill-progress-indicators.md
-- for the full design (passed through /office-hours + /plan-eng-review;
-- 7 architectural decisions captured in the plan).
--
-- One row per (issue, skill) — Upserts overwrite the same row on re-runs
-- of the same skill (in_progress → done → in_progress → done ...).
--
-- Reads from two hot paths:
--   1) Inbox list — LATERAL LIMIT 1 ORDER BY updated_at DESC to fetch
--      just the latest applied skill per issue. The composite index
--      (issue_id, updated_at DESC) makes this O(log N) per row even at
--      40k+ rows.
--   2) Issue detail — ListIssueSkillStates returns every row for the
--      issue ordered by updated_at DESC (full SkillHistory panel).
--
-- Population sources:
--   - api          — explicit POST /api/issues/:id/skill-state
--   - comment_auto — comment.go auto-detect of `/<skill-name>` tokens
--                    filtered through workspace skill registry
--                    (server/migrations/008_structured_skills.up.sql)
--   - system       — reserved for cron/derived state in v2+
--
-- ON DELETE CASCADE on issue_id is safe because issue is hard-deleted
-- (DELETE FROM issue WHERE id = $1; see issue.sql:80-81).

BEGIN;

CREATE TABLE issue_skill_state (
    issue_id     UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,

    -- Slug shape matches gstack skill naming convention. Restrictive
    -- regex blocks injection of weird tokens via the auto-detect path
    -- (a future bug in extractSkillCandidates can't produce a "/" or
    -- whitespace and reach a write).
    skill_slug   TEXT NOT NULL CHECK (skill_slug ~ '^[a-z0-9][a-z0-9-]{0,63}$'),

    -- Two-state machine. No `failed`/`skipped`/`stale` in v1; PUL-182
    -- adds a TTL cleanup path that may introduce `stale` later.
    status       TEXT NOT NULL CHECK (status IN ('in_progress', 'done')),

    -- started_at survives in_progress → in_progress re-pushes (so the
    -- chip tooltip shows the *original* start time, not the latest
    -- ping). It only resets when a done → in_progress transition
    -- happens (a fresh "I'm running the skill again" intent).
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,

    -- Used by the Inbox LATERAL LIMIT 1 query. Always bumped on
    -- Upsert, regardless of whether started_at changed.
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Audit-only column (per /plan-eng-review 3A): never rendered in
    -- the UI today. Kept so future debugging ("why did this chip
    -- appear?") and v2 features (per-source styling, opt-out of
    -- comment_auto per workspace) don't need a migration.
    source       TEXT NOT NULL CHECK (source IN ('api', 'comment_auto', 'system')),

    PRIMARY KEY (issue_id, skill_slug)
);

-- The Inbox query does LATERAL LIMIT 1 ORDER BY updated_at DESC per
-- issue. This composite descending index is what makes it cheap; the
-- planner uses it as a covering index for the lateral subquery.
CREATE INDEX idx_issue_skill_state_issue_updated
    ON issue_skill_state (issue_id, updated_at DESC);

COMMIT;
