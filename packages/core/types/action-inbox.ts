// PUL-231 Mission Control workspace inbox payload.
//
// Returned by GET /api/action-inbox?workspace_id=… — one row per active
// issue in the workspace, with the latest agent-authored comment and
// latest skill state pre-joined. Mission Control re-parses
// `latest_agent_comment.content` via extractAgentActions() to render
// chip rows inline; no per-issue follow-up fetch needed.

import type { IssueAssigneeType, IssueStatus, IssuePriority } from "./issue";

/** Latest agent-authored comment on an issue, or null when no agent
 *  has posted yet. */
export interface LatestAgentComment {
  id: string;
  content: string;
  author_id: string;
  created_at: string;
}

/** Latest skill phase the issue has touched (office-hours, eng-review,
 *  publish-plan, etc.), or null when no skill event has fired yet. */
export interface LatestSkill {
  slug: string;
  status: string;
  updated_at: string;
}

/** One row in the Mission Control inbox feed. */
export interface ActionInboxItem {
  id: string;
  workspace_id: string;
  number: number;
  identifier: string;
  title: string;
  status: IssueStatus;
  priority: IssuePriority;
  assignee_type: IssueAssigneeType | null;
  assignee_id: string | null;
  creator_type: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
  latest_agent_comment?: LatestAgentComment;
  latest_skill?: LatestSkill;
}

export interface ListActionInboxResponse {
  items: ActionInboxItem[];
  total: number;
}
