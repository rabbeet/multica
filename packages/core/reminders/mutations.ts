// PUL-154: mutations for create/cancel reminder.
//
// Optimistic update strategy (mirrors useCreateAutopilot pattern):
//   - useCreateReminder optimistically prepends a "pending placeholder"
//     row to the per-issue cache so the chip appears instantly. On error,
//     rollback restores the prior cache. On settle, invalidate to pick up
//     the server-canonical row (including the real id).
//   - useCancelReminder optimistically removes the row. Server response is
//     used to update fields like cancelled_at; failure rolls back.

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { reminderKeys } from "./queries";
import type { CreateReminderRequest, IssueReminder } from "../types";

export function useCreateReminder(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateReminderRequest) => api.createReminder(issueId, data),
    onSuccess: (created) => {
      qc.setQueryData<IssueReminder[]>(reminderKeys.forIssue(wsId, issueId), (old) => {
        if (!old) return [created];
        // Dedup if the WS event arrived first.
        if (old.some((r) => r.id === created.id)) return old;
        return [...old, created].sort((a, b) => a.fire_at.localeCompare(b.fire_at));
      });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: reminderKeys.forIssue(wsId, issueId) });
    },
  });
}

export function useCancelReminder(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (reminderId: string) => api.cancelReminder(reminderId),
    onMutate: async (reminderId) => {
      await qc.cancelQueries({ queryKey: reminderKeys.forIssue(wsId, issueId) });
      const prev = qc.getQueryData<IssueReminder[]>(reminderKeys.forIssue(wsId, issueId));
      qc.setQueryData<IssueReminder[]>(reminderKeys.forIssue(wsId, issueId), (old) =>
        old ? old.filter((r) => r.id !== reminderId) : old,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(reminderKeys.forIssue(wsId, issueId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: reminderKeys.forIssue(wsId, issueId) });
    },
  });
}
