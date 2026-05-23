import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/**
 * PUL-231 — Mission Control density preference.
 *
 * `compact` (default on mobile) shows 5-6 rows per first screen on
 * iPhone 13 — title + identifier + chip row in one ~56px row.
 * `expanded` (default on desktop) gives each row 4 lines with the full
 * delta-prose and metadata block.
 *
 * The choice is sticky per user across reloads but workspace-scoped so
 * power-users running two workspaces can keep different defaults.
 */
export type InboxDensity = "compact" | "expanded";

interface DensityStore {
  density: InboxDensity;
  setDensity: (d: InboxDensity) => void;
  toggle: () => void;
}

export const useDensityStore = create<DensityStore>()(
  persist(
    (set, get) => ({
      density: "compact",
      setDensity: (density) => set({ density }),
      toggle: () => set({ density: get().density === "compact" ? "expanded" : "compact" }),
    }),
    {
      name: "multica_inbox_density",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerForWorkspaceRehydration(() => useDensityStore.persist.rehydrate());
