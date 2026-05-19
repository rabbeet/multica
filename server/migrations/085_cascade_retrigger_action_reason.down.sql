BEGIN;

ALTER TABLE cascade_retrigger
    DROP COLUMN action_reason;

COMMIT;
