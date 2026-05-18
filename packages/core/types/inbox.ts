import type { IssueStatus } from "./issue";

// PUL-177 — wire types for Inbox phase + last-applied-skill chips
// and the SkillHistory section on the issue detail page. Mirrors
// SkillStateResponse / PhaseSlug on the Go side (server/internal/
// handler/skill_state.go, inbox.go::derivePhaseFromStatus).
export type SkillStatus = "in_progress" | "done";

export interface SkillState {
  skill: string; // slug, e.g. "office-hours"
  status: SkillStatus;
  started_at: string;
  completed_at: string | null;
  updated_at: string;
}

export type PhaseSlug =
  | "backlog"
  | "planning"
  | "coding"
  | "review"
  | "done"
  | "blocked"
  | "cancelled";

export type InboxSeverity = "action_required" | "attention" | "info";

export type InboxItemType =
  | "issue_assigned"
  | "unassigned"
  | "assignee_changed"
  | "status_changed"
  | "priority_changed"
  | "due_date_changed"
  | "new_comment"
  | "mentioned"
  | "review_requested"
  | "task_completed"
  | "task_failed"
  | "agent_blocked"
  | "agent_completed"
  | "reaction_added"
  | "quick_create_done"
  | "quick_create_failed";

export interface InboxItem {
  id: string;
  workspace_id: string;
  recipient_type: "member" | "agent";
  recipient_id: string;
  actor_type: "member" | "agent" | null;
  actor_id: string | null;
  type: InboxItemType;
  severity: InboxSeverity;
  issue_id: string | null;
  title: string;
  body: string | null;
  issue_status: IssueStatus | null;
  read: boolean;
  archived: boolean;
  created_at: string;
  details: Record<string, string> | null;
  // PUL-177 phase chip is always present (derived server-side from
  // issue.status — see derivePhaseFromStatus). latest_skill is null
  // for tickets that have never had a skill applied.
  phase: PhaseSlug;
  latest_skill: SkillState | null;
}
