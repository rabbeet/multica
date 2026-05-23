"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { actionInboxOptions } from "@multica/core/issues/queries";
import { extractAgentActions } from "../../editor/utils/preprocess-agent-actions";
import type { ActionInboxItem, AgentActions } from "@multica/core/types";

/**
 * PUL-231 Mission Control — workspace-level inbox feed.
 *
 * Wraps the actionInboxOptions react-query (single HTTP fetch, polled
 * every 15s) with derived view-state the components need:
 *   - items split into "needs action" vs "in progress" buckets
 *   - extractAgentActions() pre-computed per item so chip rows can
 *     render synchronously without re-parsing markdown on every render
 *
 * The split lives in the hook (not the SQL) so the categorisation
 * rule can evolve without a server roundtrip — today: "needs action"
 * = any open agent question OR any ack-gated command block in the
 * latest agent comment. Tomorrow we might add status hints (e.g.
 * `waiting` status always counts as "needs action").
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

export function useActionInbox(workspaceId: string): UseActionInboxResult {
  const query = useQuery({
    ...actionInboxOptions(workspaceId),
    enabled: !!workspaceId,
  });

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
