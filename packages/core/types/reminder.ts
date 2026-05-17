// PUL-154: «Wake up in N» — shape returned by the reminder HTTP endpoints.
// The server response format (snake_case) is preserved verbatim so the
// dropdown UI and the pending-chip both read the same source of truth.

export type IssueReminderStatus =
  | "pending"
  | "fired"
  | "cancelled"
  | "superseded";

export type IssueReminderCancelReason =
  | "manual"
  | "activity"
  | "creator_gone";

export type IssueReminder = {
  id: string;
  workspace_id: string;
  issue_id: string;
  created_by_type: "member" | "agent";
  created_by_id: string;
  fire_at: string;
  note: string | null;
  status: IssueReminderStatus;
  fired_at: string | null;
  fired_comment_id: string | null;
  cancelled_at: string | null;
  cancel_reason: IssueReminderCancelReason | null;
  created_at: string;
};

export type CreateReminderRequest = {
  fire_at: string;
  note?: string;
};
