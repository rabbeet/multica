import type { QueryClient } from "@tanstack/react-query";
import { inboxKeys, type InboxCacheData } from "./queries";
import type { InboxItem, InboxListPage, IssueStatus } from "../types";

export function onInboxNew(
  qc: QueryClient,
  wsId: string,
  _item: InboxItem,
) {
  // Use invalidateQueries instead of setQueryData — triggers a refetch that
  // reliably notifies all observers. The paginated list refetch is bounded
  // (INBOX_PAGE_SIZE) so this stays cheap even for active accounts (PUL-481).
  qc.invalidateQueries({ queryKey: inboxKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: inboxKeys.unreadCount(wsId) });
}

export function onInboxIssueStatusChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  status: IssueStatus,
) {
  qc.setQueryData<InboxCacheData>(inboxKeys.list(wsId), (old) =>
    old
      ? {
          ...old,
          pages: old.pages.map<InboxListPage>((page) => ({
            ...page,
            items: page.items.map((i) =>
              i.issue_id === issueId ? { ...i, issue_status: status } : i,
            ),
          })),
        }
      : old,
  );
}

// Mirrors the DB-level ON DELETE CASCADE on inbox_item.issue_id: when an issue
// is deleted, all inbox items that referenced it are gone server-side, so drop
// them from the cache too.
export function onInboxIssueDeleted(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  qc.setQueryData<InboxCacheData>(inboxKeys.list(wsId), (old) =>
    old
      ? {
          ...old,
          pages: old.pages.map<InboxListPage>((page) => ({
            ...page,
            items: page.items.filter((i) => i.issue_id !== issueId),
          })),
        }
      : old,
  );
  qc.invalidateQueries({ queryKey: inboxKeys.unreadCount(wsId) });
}

export function onInboxInvalidate(qc: QueryClient, wsId: string) {
  qc.invalidateQueries({ queryKey: inboxKeys.list(wsId) });
  qc.invalidateQueries({ queryKey: inboxKeys.unreadCount(wsId) });
}
