-- PUL-313: widen issue_status_history.ref_id from VARCHAR(64) to VARCHAR(255).
--
-- The HTTP PATCH branch in handler.UpdateIssue builds the ref_id as
--   fmt.Sprintf("http:%s:%s:%s>%s", issueUUID, actorUUID, prevStatus, newStatus)
-- (server/internal/handler/issue.go). Minimum length is
--   len("http:") + 36 + 1 + 36 + 1 + len(prev) + 1 + len(new) ≈ 87 chars,
-- already over the 64-char limit set by migration 070. Every real-world
-- transition therefore aborts the audit-row insert with SQLSTATE 22001,
-- which rolls back the audit + EnqueueChildProgressFanout transaction; the
-- PATCH itself still returns 200 but WS fanout never fires, so the UI
-- appears stuck. Two confirmed prod incidents on 2026-06-10 21:42 / 21:43.
--
-- Widening to VARCHAR(255) accommodates the longest existing ref_id shape
-- with comfortable headroom and is non-rewriting in PostgreSQL (catalog-only
-- change). UNIQUE (source, ref_id) is preserved automatically by ALTER TYPE.
-- No row data needs to migrate.

BEGIN;

ALTER TABLE issue_status_history ALTER COLUMN ref_id TYPE VARCHAR(255);

COMMIT;
