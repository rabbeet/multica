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

// PUL-180 ownership signal — server-derived from agent_task_queue,
// issue.status, and the most recent comment / status_history rows.
// Null when the chip is hidden (phase=done/cancelled, or agent
// recipient). See server/internal/handler/inbox.go::deriveOwnership.
export type OwnershipSlug = "me" | "agent" | "waiting";

export interface OwnershipMeta {
  // ISO-8601. Source varies by ownership: me → max(status_history,
  // user/agent comment); agent → started_at OR dispatched_at;
  // waiting → issue.updated_at. Null when none of the underlying
  // timestamps exist (brand-new issue / freshly-queued task).
  since: string | null;
  // Only set for ownership === "agent" — the agent.name surfaced in
  // the tooltip.
  agent_name: string | null;
  // Only set for ownership === "waiting" — controls which localized
  // tooltip ("waiting on review" vs "waiting on approval") is shown.
  reason: "review" | "approval" | null;
}

/**
 * PUL-481 cursor-paginated inbox page. Items are newest-first, deduped
 * server-side by COALESCE(issue_id, id) — one row per issue (or per
 * standalone item). `next_cursor` is an opaque base64 string; pass it
 * back verbatim as `?before=<cursor>` to fetch the next (older) page.
 * `has_more` is redundant with `next_cursor != null` but is what UI
 * code typically reads to gate the "load more" affordance.
 */
export interface InboxListPage {
  items: InboxItem[];
  next_cursor: string | null;
  has_more: boolean;
}

/**
 * Query params for `api.listInbox`. Omit both fields to fetch the
 * latest page with the server's default limit.
 */
export interface InboxListParams {
  limit?: number;
  /** Opaque cursor from a previous page's `next_cursor`. */
  before?: string;
}

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
  // PUL-180 ownership chip (third Inbox slot). Always paired:
  // ownership and ownership_meta are both null (chip hidden) or both
  // non-null (chip rendered). Server enforces this — clients can
  // treat them as a single optional pair.
  ownership: OwnershipSlug | null;
  ownership_meta: OwnershipMeta | null;
}
