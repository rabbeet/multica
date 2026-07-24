import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { inboxKeys, type InboxCacheData } from "./queries";
import { useWorkspaceId } from "../hooks";
import type { InboxItem, InboxListPage } from "../types";

/**
 * PUL-481: mutations optimistically update every loaded page. `updater` runs
 * over each `InboxItem` in every page; return the same reference to leave the
 * page untouched. Filter-style edits use `filterInboxCacheItems`.
 */
function mapInboxCacheItems(
  qc: QueryClient,
  wsId: string,
  updater: (item: InboxItem) => InboxItem,
): InboxCacheData | undefined {
  const prev = qc.getQueryData<InboxCacheData>(inboxKeys.list(wsId));
  qc.setQueryData<InboxCacheData>(inboxKeys.list(wsId), (old) =>
    old
      ? {
          ...old,
          pages: old.pages.map<InboxListPage>((page) => ({
            ...page,
            items: page.items.map(updater),
          })),
        }
      : old,
  );
  return prev;
}

function restoreInboxCache(
  qc: QueryClient,
  wsId: string,
  prev: InboxCacheData | undefined,
) {
  if (prev) qc.setQueryData(inboxKeys.list(wsId), prev);
}

/**
 * Invalidate both the paginated list and the server-derived unread count.
 * The two caches drift independently — a mark-read mutation flips list items
 * optimistically but the unread badge is served from `/api/inbox/unread-count`
 * (PUL-481), so both keys must be re-fetched.
 */
function invalidateInboxCaches(qc: QueryClient, wsId: string) {
  qc.invalidateQueries({ queryKey: inboxKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: inboxKeys.unreadCount(wsId) });
}

export function useMarkInboxRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.markInboxRead(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.list(wsId) });
      const prev = mapInboxCacheItems(qc, wsId, (item) =>
        item.id === id ? { ...item, read: true } : item,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => restoreInboxCache(qc, wsId, ctx?.prev),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}

export function useArchiveInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.archiveInbox(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: inboxKeys.list(wsId) });
      // Look up the sibling issue_id first so the optimistic pass can
      // archive every item that belongs to the same issue — the server
      // does the same in ArchiveInboxByIssue (see queries/inbox.sql).
      const cache = qc.getQueryData<InboxCacheData>(inboxKeys.list(wsId));
      let issueId: string | null | undefined;
      if (cache) {
        for (const page of cache.pages) {
          const hit = page.items.find((i) => i.id === id);
          if (hit) {
            issueId = hit.issue_id;
            break;
          }
        }
      }
      const prev = mapInboxCacheItems(qc, wsId, (item) =>
        item.id === id || (issueId && item.issue_id === issueId)
          ? { ...item, archived: true }
          : item,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => restoreInboxCache(qc, wsId, ctx?.prev),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}

export function useMarkAllInboxRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.markAllInboxRead(),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: inboxKeys.list(wsId) });
      const prev = mapInboxCacheItems(qc, wsId, (item) =>
        !item.archived ? { ...item, read: true } : item,
      );
      return { prev };
    },
    onError: (_err, _vars, ctx) => restoreInboxCache(qc, wsId, ctx?.prev),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}

export function useArchiveAllInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveAllInbox(),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}

export function useArchiveAllReadInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveAllReadInbox(),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}

export function useArchiveCompletedInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: () => api.archiveCompletedInbox(),
    onSettled: () => invalidateInboxCaches(qc, wsId),
  });
}
