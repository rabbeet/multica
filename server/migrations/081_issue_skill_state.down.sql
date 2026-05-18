BEGIN;

DROP INDEX IF EXISTS idx_issue_skill_state_issue_updated;
DROP TABLE IF EXISTS issue_skill_state;

COMMIT;
