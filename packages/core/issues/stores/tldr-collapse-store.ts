import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/**
 * PUL-231 — TL;DR-header collapsed state, keyed by issue id.
 *
 * Mirror of useCommentCollapseStore — only collapsed issue ids are
 * stored; expanded is the default state. Persisted workspace-scoped so
 * "I want this section collapsed on PUL-100" survives reloads but
 * doesn't bleed across workspaces.
 */
interface TldrCollapseStore {
  collapsedIssues: string[];
  isCollapsed: (issueId: string) => boolean;
  toggle: (issueId: string) => void;
}

export const useTldrCollapseStore = create<TldrCollapseStore>()(
  persist(
    (set, get) => ({
      collapsedIssues: [],
      isCollapsed: (issueId) => get().collapsedIssues.includes(issueId),
      toggle: (issueId) =>
        set((s) => {
          const has = s.collapsedIssues.includes(issueId);
          return {
            collapsedIssues: has
              ? s.collapsedIssues.filter((id) => id !== issueId)
              : [...s.collapsedIssues, issueId],
          };
        }),
    }),
    {
      name: "multica_tldr_collapse",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerForWorkspaceRehydration(() => useTldrCollapseStore.persist.rehydrate());
