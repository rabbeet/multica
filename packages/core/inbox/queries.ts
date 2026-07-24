import {
  infiniteQueryOptions,
  queryOptions,
  useQuery,
} from "@tanstack/react-query";
import { api } from "../api";
import type { InboxItem, InboxListPage } from "../types";

export const inboxKeys = {
  all: (wsId: string) => ["inbox", wsId] as const,
  list: (wsId: string) => [...inboxKeys.all(wsId), "list"] as const,
  unreadCount: (wsId: string) => [...inboxKeys.all(wsId), "unread-count"] as const,
};

/**
 * PUL-481: default page size for the inbox list. Deliberately smaller than the
 * server's 200-cap so the initial paint stays fast even for accounts with
 * dozens of active issues — infinite scroll pulls the rest on demand.
 */
export const INBOX_PAGE_SIZE = 100;

/**
 * Cache shape TanStack Query stores for the inbox infinite query. Callers that
 * update the cache (mutations, ws-updaters) map over `pages` — see
 * `mutations.ts` / `ws-updaters.ts` for the walk helpers.
 */
export type InboxCacheData = {
  pages: InboxListPage[];
  pageParams: (string | undefined)[];
};

/**
 * Cursor-paginated inbox list. All consumers (sidebar, chat anchor, inbox
 * page) share this key so a single cache backs the whole workspace-wide inbox
 * view — mutations + ws-updaters only need to touch one entry.
 *
 * Historically the endpoint returned every non-archived item unbounded and
 * the client deduped. See PUL-481 for the payload-size incident that drove
 * this migration to server-side dedup + keyset cursor.
 */
export function inboxInfiniteListOptions(wsId: string) {
  return infiniteQueryOptions<
    InboxListPage,
    Error,
    InboxCacheData,
    readonly unknown[],
    string | undefined
  >({
    queryKey: inboxKeys.list(wsId),
    initialPageParam: undefined,
    queryFn: ({ pageParam }) =>
      api.listInbox({ limit: INBOX_PAGE_SIZE, before: pageParam }),
    getNextPageParam: (lastPage) =>
      lastPage.has_more && lastPage.next_cursor ? lastPage.next_cursor : undefined,
  });
}

/**
 * Server-derived unread count, dedup'd by COALESCE(issue_id, id) to match the
 * inbox UI's list semantics (one row per issue). This replaces the previous
 * client-side count that had to walk the entire inbox and would have broken
 * once we bounded the page size (PUL-481).
 */
export function inboxUnreadCountOptions(wsId: string) {
  return queryOptions({
    queryKey: inboxKeys.unreadCount(wsId),
    queryFn: () => api.getUnreadInboxCount(),
    select: (data: { count: number }) => data.count,
  });
}

export function useInboxUnreadCount(wsId: string | null | undefined): number {
  const { data } = useQuery({
    ...inboxUnreadCountOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  return data ?? 0;
}

/**
 * Flatten cache pages into the legacy `InboxItem[]` shape. Used by consumers
 * (chat context anchor, inbox page) that render the currently-loaded window
 * as a single list. Server dedup means items are already unique by
 * `(issue_id ?? id)`; no client-side pass required.
 */
export function flattenInboxPages(
  data: InboxCacheData | undefined,
): InboxItem[] {
  if (!data) return [];
  const out: InboxItem[] = [];
  for (const page of data.pages) {
    for (const item of page.items) out.push(item);
  }
  return out;
}
