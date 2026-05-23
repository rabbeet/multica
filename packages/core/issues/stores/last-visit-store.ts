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
 * Persistence: localStorage (workspace-scoped) is kept as a fast first-
 * paint cache. PUL-239 promoted the source-of-truth to the server-side
 * `user_issue_last_visit` table — Mission Control hydrates the map via
 * `hydrateFromServer()` on mount and POSTs every `mark()` to the new
 * `/api/issues/:id/last-visit` endpoint via a wrapper hook
 * (`useLastVisitSync`). The store itself stays HTTP-free so it can be
 * unit-tested as a pure zustand atom.
 *
 * Missing key = "never visited" (handled in delta-mode UI as
 * "Last visit: unknown", no 🆕 spam).
 */
interface LastVisitStore {
  visits: Record<string, number>;
  /** Mark an issue as visited now — local-only. The PUL-239 server
   *  upsert is fired by `useLastVisitSync` which calls this. */
  mark: (issueId: string) => void;
  /** Read the last-visit timestamp (ms-since-epoch) or null when unknown. */
  get: (issueId: string) => number | null;
  /** Merge a server-supplied map (issueId → ISO timestamp) into the
   *  local cache. Server wins on conflict, but local entries the server
   *  doesn't know about yet (e.g. queued offline marks) survive. */
  hydrateFromServer: (items: Array<{ issue_id: string; last_visited_at: string }>) => void;
}

export const useLastVisitStore = create<LastVisitStore>()(
  persist(
    (set, get) => ({
      visits: {},
      mark: (issueId) =>
        set((s) => ({ visits: { ...s.visits, [issueId]: Date.now() } })),
      get: (issueId) => get().visits[issueId] ?? null,
      hydrateFromServer: (items) =>
        set((s) => {
          const next: Record<string, number> = { ...s.visits };
          for (const it of items) {
            const ts = Date.parse(it.last_visited_at);
            if (!Number.isNaN(ts)) next[it.issue_id] = ts;
          }
          return { visits: next };
        }),
    }),
    {
      name: "multica_last_visit",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerForWorkspaceRehydration(() => useLastVisitStore.persist.rehydrate());
