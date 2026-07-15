-- PUL-445 Paginated GET /api/inbox needs to walk inbox_item ordered
-- by created_at DESC for a specific (workspace, recipient), filtered
-- to the active (non-archived) subset. The pre-existing
-- idx_inbox_recipient (recipient_type, recipient_id, read) matches
-- the "unread count" query but NOT the paginated list — LIMIT 50 on
-- a workspace with a few thousand inbox rows fell back to a sort on
-- the LEFT JOIN result, which is what made the endpoint slow at
-- steady state (server-side 130-415ms per access-log measurement on
-- 2026-07-14).
--
-- Partial index on archived=false matches the ListInboxItems WHERE
-- exactly. Archived rows dominate long-term once the sibling 14d
-- retention scheduler (PUL-445 commit 4) starts sweeping, so the
-- on-disk footprint stays proportional to the active view instead
-- of the accumulating tail.
--
-- CONCURRENTLY keeps prod writes unblocked; the migration runner in
-- server/cmd/migrate/main.go execs raw SQL via pgxpool with no
-- surrounding transaction, so this is safe here.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_inbox_recipient_active_recent
  ON inbox_item (workspace_id, recipient_type, recipient_id, created_at DESC)
  WHERE archived = false;
