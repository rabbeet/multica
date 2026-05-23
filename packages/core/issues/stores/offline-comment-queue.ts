import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/**
 * Queue of chip-tap intents the user produced while offline.
 *
 * When `navigator.onLine === false` the chip components (PUL-231) push
 * their intended threaded reply here instead of calling the API. As
 * soon as `online` fires again the store's drain loop replays the
 * queued comments in FIFO order against the live API, surfacing
 * progress via sonner toasts.
 *
 * Persisted (localStorage / workspace-scoped storage) so a queued
 * intent survives a hard refresh while the network is still down. The
 * drain loop also runs on rehydrate so anything left over from a
 * previous session is replayed if we're already online again.
 */

export interface QueuedComment {
  /** Stable id within this client — used for dedupe on drain and as React key. */
  id: string;
  issueId: string;
  /** Markdown body of the reply (typically just the chip variant text). */
  content: string;
  parentId: string;
  /** Wall-clock ms at enqueue time, for UI surfacing of "queued 3m ago". */
  enqueuedAt: number;
}

interface OfflineCommentQueueState {
  pending: QueuedComment[];
  /** Push a new intent. Generates id + timestamp. */
  enqueue: (input: Omit<QueuedComment, "id" | "enqueuedAt">) => string;
  /** Remove a successfully-drained intent. */
  dismiss: (id: string) => void;
  /** Test-only: wipe queue (avoids leakage between describe blocks). */
  _reset: () => void;
}

function generateId(): string {
  // crypto.randomUUID is available in modern browsers + node 18+; in
  // pathological older environments we fall back to a timestamp-and-rng id
  // so the offline queue never crashes the surrounding markdown render.
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `q_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`;
}

export const useOfflineCommentQueue = create<OfflineCommentQueueState>()(
  persist(
    (set) => ({
      pending: [],
      enqueue: (input) => {
        const id = generateId();
        set((s) => ({
          pending: [
            ...s.pending,
            { ...input, id, enqueuedAt: Date.now() },
          ],
        }));
        return id;
      },
      dismiss: (id) =>
        set((s) => ({ pending: s.pending.filter((p) => p.id !== id) })),
      _reset: () => set({ pending: [] }),
    }),
    {
      name: "multica_offline_comment_queue",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

registerForWorkspaceRehydration(() => useOfflineCommentQueue.persist.rehydrate());

/**
 * Install a single global `online` event listener that drains the queue
 * when connectivity returns. Idempotent — safe to call from multiple
 * mount points; subsequent calls are no-ops.
 *
 * The actual drain work (POST per queued comment) is parameterised so
 * core/ doesn't depend on a specific http client. The api package wires
 * it up at app boot.
 */
let drainListenerInstalled = false;
type DrainHandler = (item: QueuedComment) => Promise<void>;

export function installOfflineCommentDrain(handler: DrainHandler): void {
  if (drainListenerInstalled) return;
  if (typeof window === "undefined") return;
  drainListenerInstalled = true;

  const drain = async () => {
    const { pending, dismiss } = useOfflineCommentQueue.getState();
    if (pending.length === 0) return;
    for (const item of pending) {
      try {
        await handler(item);
        dismiss(item.id);
      } catch {
        // Stop on the first failure so we don't blast the network with
        // failing requests when the server is down even though
        // navigator.onLine flipped true. The next online event will
        // retry from the surviving queue head.
        break;
      }
    }
  };

  window.addEventListener("online", () => {
    void drain();
  });

  // If we boot while already online with leftover items, drain immediately.
  if (typeof navigator !== "undefined" && navigator.onLine !== false) {
    void drain();
  }
}
