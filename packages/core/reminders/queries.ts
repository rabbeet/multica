// PUL-154: React Query keys + options for issue-scoped reminders.
//
// Reminders are issue-scoped on the read path (the UI never lists
// across-workspace pending reminders in v1 — that's the deferred "Reminders
// today" inbox). Keys are therefore (wsId, issueId) and cache invalidation
// happens via the WS events wired in use-realtime-sync.ts.

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const reminderKeys = {
  all: (wsId: string) => ["reminders", wsId] as const,
  forIssue: (wsId: string, issueId: string) =>
    ["reminders", wsId, "issue", issueId] as const,
};

export function pendingRemindersOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: reminderKeys.forIssue(wsId, issueId),
    queryFn: () => api.listPendingReminders(issueId),
    // Background refetch is fine; the WS handler invalidates on every fire/
    // create/cancel so this only kicks in if the WS connection drops.
    staleTime: 60_000,
  });
}
