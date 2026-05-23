"use client";

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  actionInboxKey,
  actionInboxOptions,
} from "@multica/core/issues/queries";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import type {
  CommentCreatedPayload,
  CommentUpdatedPayload,
  CommentDeletedPayload,
  IssueUpdatedPayload,
} from "@multica/core/types/events";
import { extractAgentActions } from "../../editor/utils/preprocess-agent-actions";
import type { ActionInboxItem, AgentActions } from "@multica/core/types";

/**
 * PUL-231 Mission Control — workspace-level inbox feed.
 *
 * Wraps the actionInboxOptions react-query with derived view-state the
 * components need:
 *   - items split into "needs action" vs "in progress" buckets
 *   - extractAgentActions() pre-computed per item so chip rows can
 *     render synchronously without re-parsing markdown on every render
 *
 * The split lives in the hook (not the SQL) so the categorisation
 * rule can evolve without a server roundtrip — today: "needs action"
 * = any open agent question OR any ack-gated command block in the
 * latest agent comment. Tomorrow we might add status hints (e.g.
 * `waiting` status always counts as "needs action").
 *
 * PUL-238 — replaces the original 15s `refetchInterval` polling with
 * WS-event-driven invalidation. Subscribes to comment + issue events
 * filtered by `payload.*.workspace_id === wsId`. Updates are debounced
 * (200ms trailing) so a multi-agent burst — 20 events in 100ms during
 * an active task — collapses to one cache invalidation instead of 20.
 * `useWSReconnect` re-fetches once on every reconnect to recover from
 * any events missed during a disconnect window.
 */
export interface ActionInboxRow {
  item: ActionInboxItem;
  actions: AgentActions;
}

export interface UseActionInboxResult {
  isLoading: boolean;
  isError: boolean;
  refetch: () => void;
  /** Total of items the server returned, regardless of bucket. */
  total: number;
  /** Items with at least one open question or ack-gated command. */
  needsAction: ActionInboxRow[];
  /** Everything else, still active. */
  inProgress: ActionInboxRow[];
}

/**
 * Trailing-edge debounce — fires once, 200ms after the most recent
 * scheduled call. We only need the LAST event in a burst because the
 * action is "invalidate the inbox query," which is idempotent.
 *
 * Implementation note: kept inline (not lodash) to keep the inbox
 * bundle stable — react-query is already heavy enough.
 */
const WS_INVALIDATE_DEBOUNCE_MS = 200;

function useDebouncedFn(fn: () => void, wait: number): () => void {
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);
  return useCallback(() => {
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      fnRef.current();
    }, wait);
  }, [wait]);
}

export function useActionInbox(workspaceId: string): UseActionInboxResult {
  const qc = useQueryClient();
  const query = useQuery({
    ...actionInboxOptions(workspaceId),
    enabled: !!workspaceId,
  });

  // Single invalidation entrypoint shared by every WS handler. Debounced
  // so a WS-storm scenario fires the actual refetch once at the tail.
  const invalidate = useDebouncedFn(
    useCallback(() => {
      if (!workspaceId) return;
      qc.invalidateQueries({ queryKey: actionInboxKey(workspaceId) });
    }, [qc, workspaceId]),
    WS_INVALIDATE_DEBOUNCE_MS,
  );

  // --- WS subscriptions -----------------------------------------------------

  useWSEvent(
    "comment:created",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentCreatedPayload;
        // Issue WS payloads don't carry workspace_id on the comment
        // itself — but the inbox feed is keyed off our workspaceId
        // anyway. Conservatively invalidate on any comment:created; the
        // debounce keeps the cost flat regardless of cross-workspace
        // noise. If a multi-workspace deployment proves this too noisy
        // we'll thread workspace_id through the comment payload server-
        // side and filter here.
        if (!comment) return;
        invalidate();
      },
      [invalidate],
    ),
  );

  useWSEvent(
    "comment:updated",
    useCallback(
      (payload: unknown) => {
        const { comment } = payload as CommentUpdatedPayload;
        if (!comment) return;
        invalidate();
      },
      [invalidate],
    ),
  );

  useWSEvent(
    "comment:deleted",
    useCallback(
      (payload: unknown) => {
        const { issue_id } = payload as CommentDeletedPayload;
        if (!issue_id) return;
        invalidate();
      },
      [invalidate],
    ),
  );

  useWSEvent(
    "issue:updated",
    useCallback(
      (payload: unknown) => {
        const { issue } = payload as IssueUpdatedPayload;
        if (!issue) return;
        // Status change can flip an issue in/out of the active set, so
        // re-fetch — but only when the issue is in OUR workspace.
        if (issue.workspace_id !== workspaceId) return;
        invalidate();
      },
      [invalidate, workspaceId],
    ),
  );

  // After a WS reconnect we can't trust anything we held — drop the
  // cached payload and re-fetch from scratch. Matches the existing
  // pattern in use-issue-timeline.ts.
  useWSReconnect(
    useCallback(() => {
      if (!workspaceId) return;
      qc.invalidateQueries({ queryKey: actionInboxKey(workspaceId) });
    }, [qc, workspaceId]),
  );

  return useMemo(() => {
    const items = query.data?.items ?? [];
    const needsAction: ActionInboxRow[] = [];
    const inProgress: ActionInboxRow[] = [];

    for (const item of items) {
      // No agent comment yet → can't have open questions; route to
      // in-progress so it still shows up but doesn't grab attention.
      const content = item.latest_agent_comment?.content ?? "";
      const parentId = item.latest_agent_comment?.id ?? item.id;
      const actions = extractAgentActions(content, parentId);
      const row: ActionInboxRow = { item, actions };
      if (actions.questions.length > 0 || actions.commands.length > 0) {
        needsAction.push(row);
      } else {
        inProgress.push(row);
      }
    }

    return {
      isLoading: query.isLoading,
      isError: query.isError,
      refetch: () => query.refetch(),
      total: items.length,
      needsAction,
      inProgress,
    };
  }, [query.data, query.isLoading, query.isError, query.refetch]);
}
