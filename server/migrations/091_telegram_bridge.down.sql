-- Rollback PUL-479 PR1.
-- No data preserved: on re-migrate-up the scheduler recreates forum
-- topics lazily on next comment. Existing TG-side topics keep their
-- history; only the multica-side mapping is lost.

BEGIN;

DROP TABLE IF EXISTS telegram_outbox;
DROP TABLE IF EXISTS telegram_thread;

COMMIT;
