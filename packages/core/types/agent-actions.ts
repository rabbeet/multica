// PUL-231 — Inline agent-action chip primitives shared types.
//
// Agents post markdown comments that include numbered questions or
// ack-gated command blocks. The editor preprocessor (see
// packages/views/editor/utils/preprocess-agent-actions.ts) detects those
// patterns via regex on the raw markdown and injects HTML markers that
// the readonly renderer maps to <AgentQuestionChips/> and
// <AgentCommandButton/> components.
//
// These types are the contract between the preprocessor, the chips
// component, and the workspace-level Mission Control (PR2). Mission
// Control re-runs the same extractAgentActions() pure function to surface
// open questions across all issues without rendering markdown.

/**
 * A numbered question detected in an agent-authored comment.
 *
 * Source pattern: `N. **Title**: tail-text-with-variants` as the first
 * line of an ordered-list item. The tail is parsed for variants in one
 * of three forms (in order of priority):
 *   1. `default \`X\`. Альтернативы: A / B / C` → [X(default), A, B, C]
 *   2. `[a] X [b] Y [c] Z` → [X, Y, Z]
 *   3. Nested bullet list directly after the title — each bullet a variant
 *
 * `id` uniquely identifies the question within the issue and is derived
 * deterministically from the parent comment id + the question's ordinal,
 * so a re-render after a refetch matches the same question back to its
 * answer comment.
 */
export interface AgentQuestion {
  /** `${parentCommentId}:${ordinal}` — stable across renders. */
  id: string;
  /** Ordinal as it appears in the markdown (1-based). */
  ordinal: number;
  /** Text between `**...**`. */
  title: string;
  /** Tail text after the title — kept for context display. */
  tail: string;
  /** The variant marked as default, if any. Always also present in `variants`. */
  default: string | null;
  /** All choices the user can tap. Always >= 2 entries; otherwise the
   *  detector skips the item to avoid rendering a one-button "question". */
  variants: string[];
  /** The comment that contains this question. Used as `parent_id` for
   *  the threaded reply when the user taps a chip. */
  parentCommentId: string;
}

/**
 * An actionable command block detected in an agent-authored comment.
 *
 * Source pattern: fenced code block (any language) followed within the
 * same comment by a paragraph containing one of the ack sentinels:
 *   - "Запускать сам не буду"
 *   - "нужен ваш ack"
 *   - "нужен ack"
 *   - "explicit go-ahead"
 *
 * When matched, the chip renders as a single button — `[Запустить все N]`
 * for N > 1, otherwise `[Запустить: <command>]`. Tap posts a canonical
 * "запусти" reply threaded to the parent comment; the agent's next
 * pickup reads the threaded reply as the ack.
 */
export interface AgentCommand {
  /** `${parentCommentId}:cmd-${ordinal}` — stable across renders. */
  id: string;
  /** Ordinal of the command block within the comment (1-based). */
  ordinal: number;
  /** One entry per non-empty line of the code block. */
  commands: string[];
  /** Nearest `## Header` above the block, used as the button group label.
   *  Empty string if no header found. */
  groupLabel: string;
  /** The comment that contains this block. */
  parentCommentId: string;
}

/**
 * Result of running the detector on a single agent comment.
 *
 * Both arrays may be empty (most comments contain neither pattern). The
 * caller decides whether to render anything based on `questions.length +
 * commands.length > 0`.
 */
export interface AgentActions {
  questions: AgentQuestion[];
  commands: AgentCommand[];
}
