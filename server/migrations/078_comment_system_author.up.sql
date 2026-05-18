-- PUL-168: cascade Spawner inserts synth comments under author_type='system'
-- to surface the wake-up reason (CI failure, etc.) to the assigned agent.
-- Two relaxations required:
--   1. author_type CHECK must accept 'system' (today it only allows member/agent).
--   2. author_id must be nullable — a system row has no human/agent author.
-- No FK references author_id, and existing readers (daemon.go, issue_status_flip.go)
-- already check .Valid before dereferencing, so going NULL is safe for the column.

ALTER TABLE comment DROP CONSTRAINT IF EXISTS comment_author_type_check;
ALTER TABLE comment ADD CONSTRAINT comment_author_type_check
    CHECK (author_type IN ('member', 'agent', 'system'));

ALTER TABLE comment ALTER COLUMN author_id DROP NOT NULL;
