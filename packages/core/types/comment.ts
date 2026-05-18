export type CommentType =
  | "comment"
  | "status_change"
  | "progress_update"
  | "system"
  | "wake_up" // PUL-154 reminder scheduler
  | "child_progress"; // PUL-164 fan-out worker

// system is the author_type emitted by server-side schedulers (PUL-164
// child_progress fan-out worker). Existing member/agent values continue to
// cover all HTTP-authored comments.
export type CommentAuthorType = "member" | "agent" | "system";

// ChildProgressMeta is the structured payload on comment.meta for
// type='child_progress' comments. Mirrors service.ChildProgressMeta in Go.
// Fields except kind/child_issue_id are best-effort — render layers MUST
// tolerate missing values.
export interface ChildProgressMeta {
  kind: "child_progress";
  child_issue_id: string;
  child_identifier?: string;
  child_title: string;
  prev_status: string;
  new_status: string;
  actor_id?: string;
  actor_type?: string;
  source_history_id: number;
}

export interface Reaction {
  id: string;
  comment_id: string;
  actor_type: string;
  actor_id: string;
  emoji: string;
  created_at: string;
}

export interface Comment {
  id: string;
  issue_id: string;
  author_type: CommentAuthorType;
  author_id: string;
  content: string;
  type: CommentType;
  parent_id: string | null;
  reactions: Reaction[];
  attachments: import("./attachment").Attachment[];
  created_at: string;
  updated_at: string;
  // PUL-164: structured payload for type-specific rendering. Empty object
  // ({}) for most rows; populated for type='child_progress' (see
  // ChildProgressMeta). Frontend type-checks via a runtime discriminator
  // on meta.kind, not on TypeScript narrowing alone.
  meta?: Record<string, unknown>;
  // PUL-164: pointer to issue_status_history.id for type='child_progress'
  // comments. NULL for everything else. Used only for debug / introspection
  // on the client.
  source_history_id?: number | null;
}
