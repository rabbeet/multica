-- PUL-201 fix: GitHub /events returns events from at least two
-- distinct numeric-id streams (PullRequestEvent/PullRequestReviewEvent/
-- IssueCommentEvent on the ~9.6e9 line, PushEvent/CreateEvent/
-- DeleteEvent/WorkflowRunEvent on the ~12e9 line). A single scalar
-- cursor (the original `last_event_id`) cannot represent both streams:
-- once the poller advances past a 12e9 Push, every subsequent PR-event
-- on the 9.6e9 line falls below the cursor and is filtered as "already
-- seen", silently dropping every pr_merged forever.
--
-- Fix: one cursor per Event.Type, stored as JSONB keyed by GitHub's
-- event-type string (e.g. "PullRequestEvent": 9648929307). FetchEvents
-- filters per-type, and the poller advances per-type. Legacy
-- last_event_id is left intact for the 1-2 week stabilization window
-- per the PUL-201 plan; a follow-up migration drops it.
--
-- See plans://Multica/2026-05-19-pul-201-githubpoll-per-event-type-cursor.md

BEGIN;

ALTER TABLE github_poll_cursor
    ADD COLUMN cursor_by_type JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Reset last_polled_at and etag on every existing row. Two reasons:
--
-- 1) Force first-run-seed path in poller.tickOne for these rows. The
--    seed branch is gated on cursor.LastPolledAt.IsZero(). Without
--    this UPDATE, existing rows keep their non-NULL last_polled_at
--    and skip the seed branch; the new cursor_by_type stays at the
--    default '{}' (which behaves like "id 0 for every type"); the
--    next tick then forwards every event currently on page 1 (up to
--    100 events) into the sink, producing a deploy-time flood in
--    cascade_retrigger.
-- 2) Discard the stale ETag — keeping it would cause GitHub to reply
--    304 to the first post-migration tick, masking the seed entirely.
--
-- The cost is dropping any pr_merged events that occurred between the
-- last successful tick and the post-deploy seed tick. Per PUL-201 the
-- backfill is explicitly out-of-scope ("операционно проще руками").
UPDATE github_poll_cursor
   SET last_polled_at = NULL,
       etag           = NULL;

COMMIT;
