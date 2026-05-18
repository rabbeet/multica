-- PUL-164 rollback. Drop outbox, drop fanout columns + index, restore
-- comment.type whitelist without 'child_progress'. Safe to apply only when
-- no rows of type='child_progress' exist (the .up migration is single-user
-- multica, so a quick DELETE before rollback is acceptable).

BEGIN;

-- (5) Outbox table.
DROP TABLE IF EXISTS issue_child_progress_outbox;

-- (4) Idempotency index.
DROP INDEX IF EXISTS comment_child_progress_unique;

-- (3) source_history_id column.
ALTER TABLE comment DROP COLUMN IF EXISTS source_history_id;

-- (2) meta column. Note: if a future migration migrates wake_up payloads here,
--     rolling 078 back will lose that data; check before applying down.
ALTER TABLE comment DROP COLUMN IF EXISTS meta;

-- (1c) Restore comment.author_id NOT NULL. Rolling back is only safe when
--      no comment rows have author_id IS NULL — caller is responsible.
ALTER TABLE comment ALTER COLUMN author_id SET NOT NULL;

-- (1b) Restore comment.author_type CHECK to ('member', 'agent').
ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_author_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_author_type_check
    CHECK (author_type IN ('member', 'agent'));

-- (1) comment.type CHECK — restore to PUL-154 whitelist (no child_progress).
ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_type_check
    CHECK (type IN (
        'comment',
        'status_change',
        'progress_update',
        'system',
        'wake_up'
    ));

COMMIT;
