"use client";

import { useCallback, useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useLastVisitStore } from "@multica/core/issues/stores";

/**
 * PUL-239 — keep the local `useLastVisitStore` in sync with the
 * server-side `user_issue_last_visit` table.
 *
 * Hydrate behaviour:
 *   - On Mission Control mount, fetch `/api/last-visits?workspace_id=`
 *     once and merge the server-supplied timestamps into the store.
 *     Subsequent mounts within the same tab don't refetch (the cache
 *     is good enough — every mark POSTs through), but a hard refresh
 *     re-hydrates.
 *
 * Mark behaviour:
 *   - Wraps `useLastVisitStore.mark` so callers POST to
 *     `/api/issues/:id/last-visit` after updating the local store.
 *     Failed POSTs are logged but don't block UI — local cache wins for
 *     this device until the next online hydrate reconciles.
 *
 * Cross-device convergence target: a visit on the phone shows up on
 * Mac within one mount cycle (i.e. opening Mission Control on the Mac
 * triggers hydrate, which sees the phone-side timestamp).
 */
const hydratedWorkspaces = new Set<string>();

export function useLastVisitSync(workspaceId: string): {
  markVisited: (issueId: string) => void;
} {
  const qc = useQueryClient();
  const markLocal = useLastVisitStore((s) => s.mark);
  const hydrate = useLastVisitStore((s) => s.hydrateFromServer);
  const inFlightRef = useRef(false);

  // Hydrate once per (workspace, tab). Re-hydrate is safe but wasteful
  // because every mark already POSTs through.
  useEffect(() => {
    if (!workspaceId) return;
    if (hydratedWorkspaces.has(workspaceId)) return;
    if (inFlightRef.current) return;
    inFlightRef.current = true;
    api
      .listLastVisits(workspaceId)
      .then((resp) => {
        hydrate(resp.items);
        hydratedWorkspaces.add(workspaceId);
      })
      .catch(() => {
        // Soft-fail. Local cache stays authoritative for this device;
        // delta-mode UI will say "Last visit: unknown" if the user
        // hits an issue for the first time without server data.
      })
      .finally(() => {
        inFlightRef.current = false;
      });
  }, [workspaceId, hydrate]);

  const markVisited = useCallback(
    (issueId: string) => {
      markLocal(issueId);
      if (!workspaceId) return;
      api.markIssueVisited(workspaceId, issueId).catch(() => {
        // Silent fail — local mark already landed. Next hydrate may
        // overwrite with a slightly older server timestamp; acceptable
        // because the user just refreshed it.
      });
      // Invalidate any react-query cache that depends on the last-visit
      // map. There is none today; reserved for PUL-240's per-question
      // answered detection if it consumes the timestamp.
      void qc;
    },
    [markLocal, qc, workspaceId],
  );

  return { markVisited };
}

/** Test helper — reset the per-tab hydration guard between tests. */
export function _resetLastVisitSyncForTests(): void {
  hydratedWorkspaces.clear();
}
