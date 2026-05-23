/**
 * Carries comment-author metadata to the readonly renderer.
 *
 * `ReadonlyContent` doesn't know who wrote the markdown — it just sees
 * a string. The agent-action preprocessor (see
 * `utils/preprocess-agent-actions.ts`) needs to know whether the author
 * was an agent (to inject chip markers) or a member (to leave the
 * content alone). Likewise, `<AgentQuestionChips/>` needs the comment id
 * so it can thread its reply via `parent_id`.
 *
 * Comment-card wraps each agent-authored `<ReadonlyContent/>` in a
 * provider; the renderer reads the values out of context. Member
 * comments and other non-comment markdown surfaces (issue description,
 * autopilot detail) get the default (`isAgent=false`, `commentId=null`)
 * and the preprocessor skips immediately.
 */

"use client";

import { createContext, useContext } from "react";

export interface AuthorContextValue {
  /** True when the markdown was authored by an agent. Drives chip injection. */
  isAgent: boolean;
  /** The id of the comment whose content is being rendered, when applicable.
   *  Required by `<AgentQuestionChips/>` to populate `parent_id` on the
   *  threaded reply it posts. `null` for non-comment surfaces. */
  commentId: string | null;
  /** The issue id the comment belongs to. Required by the
   *  `useCreateComment` hook that powers the chip-tap mutation. */
  issueId: string | null;
}

const DEFAULT_AUTHOR_CONTEXT: AuthorContextValue = {
  isAgent: false,
  commentId: null,
  issueId: null,
};

const AuthorContext = createContext<AuthorContextValue>(DEFAULT_AUTHOR_CONTEXT);

export const AuthorContextProvider = AuthorContext.Provider;

export function useAuthorContext(): AuthorContextValue {
  return useContext(AuthorContext);
}
