-- PUL-212: drain is intentionally irreversible.
--
-- We cannot distinguish PUL-212-drained rows from rows that were
-- legitimately scope_filter_skipped by the worker for any other
-- reason (same action, similar action_reason format). The down
-- migration is a no-op; if you need to rollback the code, the
-- legacy marker rows remain as-is and the restored classifier will
-- re-emit fresh events on the next poll (idempotency-keyed by
-- (repo, eventID) UUIDv5; see internal/githubpoll/idempotency.go).

SELECT 1; -- no-op placeholder; migration framework requires non-empty .down.sql
