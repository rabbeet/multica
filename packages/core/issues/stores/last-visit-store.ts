import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/**
 * PUL-231 — per-issue last-visit timestamps (ms-since-epoch).
 *
 * Mission Control and the per-ticket TL;DR header (PR3) compute deltas
 * against this map: "since you were last here, agent posted 2 new
 * comments, PR #585 merged." Set on drill-down tap from Mission Control
 * AND on issue-detail page mount.
 *
 * Persistence: localStorage (workspace-scoped) — single-device for the
 * MVP per eng-review S2=c. Cross-device sync arrives in a Phase 2
 * follow-up ticket with a `user_issue_last_visit` server table.
 *
 * Missing key = "never visited" (handled in delta-mode UI as
 * "Last visit: unknown", no 🆕 spam).
 */
interface LastVisitStore {
  visits: Record<string, number>;
  /** Mark an issue as visited now. */
  mark: (issueId: string) => void;
  /** Read the last-visit timestamp (ms-since-epoch) or null when unknown. */
  get: (issueId: string) => number | null;
}

export const useLastVisitStore = create<LastVisitStore>()(
  persist(
    (set, get) => ({
      visits: {},
      mark: (issueId) =>
        set((s) => ({ visits: { ...s.visits, [issueId]: Date.now() } })),
      get: (issueId) => get().visits[issueId] ?? null,
    }),
    {
      name: "multica_last_visit",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerForWorkspaceRehydration(() => useLastVisitStore.persist.rehydrate());
