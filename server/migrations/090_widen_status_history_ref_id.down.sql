-- Reverse of 090. Safe only if every existing ref_id fits in 64 chars; the
-- whole point of 090 was that http-PATCH ref_ids do not. The operator must
-- either truncate or delete any oversize rows manually before running this
-- (e.g. DELETE FROM issue_status_history WHERE length(ref_id) > 64;), or the
-- ALTER below will fail with SQLSTATE 22001.

BEGIN;

ALTER TABLE issue_status_history ALTER COLUMN ref_id TYPE VARCHAR(64);

COMMIT;
