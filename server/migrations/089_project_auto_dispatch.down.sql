ALTER TABLE project
    DROP CONSTRAINT IF EXISTS project_default_assignee_pair_check,
    DROP CONSTRAINT IF EXISTS project_default_assignee_type_check,
    DROP COLUMN IF EXISTS default_assignee_id,
    DROP COLUMN IF EXISTS default_assignee_type;
