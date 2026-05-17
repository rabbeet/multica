BEGIN;

DROP INDEX IF EXISTS idx_issue_reminder_issue;
DROP INDEX IF EXISTS idx_issue_reminder_recovery;
DROP INDEX IF EXISTS idx_issue_reminder_due;
DROP TABLE IF EXISTS issue_reminder;

COMMIT;
