-- Down side of PUL-201. Drops the per-event-type cursor column. The
-- accompanying last_polled_at / etag NULL'ing in 086.up.sql is not
-- restored — there is nothing to restore to. On rollback the next
-- tick uses the legacy last_event_id scalar (unchanged by 086) and
-- re-introduces the PR-event blindness this migration fixed; that is
-- the cost of rolling back, and the operator should know it.

BEGIN;

ALTER TABLE github_poll_cursor
    DROP COLUMN cursor_by_type;

COMMIT;
