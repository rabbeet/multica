-- PUL-260: project-level default assignee.
--
-- When set, CreateIssue falls back to default_assignee_type/id when the
-- caller doesn't supply an assignee. Combined with the existing
-- shouldEnqueueAgentTask path, that means new tickets in this project
-- automatically dispatch to the configured agent.
--
-- Distinct from lead_type/lead_id, which is a label/owner concept and does
-- NOT trigger task enqueue.
ALTER TABLE project
    ADD COLUMN default_assignee_type TEXT,
    ADD COLUMN default_assignee_id UUID;

ALTER TABLE project
    ADD CONSTRAINT project_default_assignee_type_check
        CHECK (default_assignee_type IS NULL
               OR default_assignee_type = ANY (ARRAY['member'::text, 'agent'::text])),
    ADD CONSTRAINT project_default_assignee_pair_check
        CHECK ((default_assignee_type IS NULL) = (default_assignee_id IS NULL));
