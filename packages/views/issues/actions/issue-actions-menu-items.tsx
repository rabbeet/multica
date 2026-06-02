"use client";

import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ArrowDown,
  ArrowUp,
  Calendar,
  Clock,
  FileDown,
  FolderOpen,
  Link2,
  MoreHorizontal,
  Pin,
  PinOff,
  Plus,
  Trash2,
  UserMinus,
} from "lucide-react";
import type { AgentTask, Issue } from "@multica/core/types";
import { api } from "@multica/core/api";
import {
  ALL_STATUSES,
  PRIORITY_ORDER,
  PRIORITY_CONFIG,
} from "@multica/core/issues/config";
import { issueKeys } from "@multica/core/issues/queries";
import { presetsForNow, useCreateReminder } from "@multica/core/reminders";
import { StatusIcon } from "../components/status-icon";
import { PriorityIcon } from "../components/priority-icon";
import { ActorAvatar } from "../../common/actor-avatar";
import {
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ContextMenuItem,
  ContextMenuSub,
  ContextMenuSubTrigger,
  ContextMenuSubContent,
  ContextMenuSeparator,
} from "@multica/ui/components/ui/context-menu";
import type { UseIssueActionsResult } from "./use-issue-actions";
import { useT } from "../../i18n";

// Both Dropdown and Context menu wrappers expose an API-compatible surface
// (variant, inset, onClick, etc.). We bundle the primitives we need into a
// single object so `IssueActionsMenuItems` can render the same JSX for both.
export interface MenuPrimitives {
  Item: typeof DropdownMenuItem;
  Sub: typeof DropdownMenuSub;
  SubTrigger: typeof DropdownMenuSubTrigger;
  SubContent: typeof DropdownMenuSubContent;
  Separator: typeof DropdownMenuSeparator;
}

export const dropdownPrimitives: MenuPrimitives = {
  Item: DropdownMenuItem,
  Sub: DropdownMenuSub,
  SubTrigger: DropdownMenuSubTrigger,
  SubContent: DropdownMenuSubContent,
  Separator: DropdownMenuSeparator,
};

// Context primitives are API-compatible with Dropdown primitives, but their
// TypeScript identities differ. Cast once here and call it a day — this is the
// single bridge between the two primitive sets.
export const contextPrimitives: MenuPrimitives = {
  Item: ContextMenuItem as unknown as typeof DropdownMenuItem,
  Sub: ContextMenuSub as unknown as typeof DropdownMenuSub,
  SubTrigger: ContextMenuSubTrigger as unknown as typeof DropdownMenuSubTrigger,
  SubContent: ContextMenuSubContent as unknown as typeof DropdownMenuSubContent,
  Separator: ContextMenuSeparator as unknown as typeof DropdownMenuSeparator,
};

interface IssueActionsMenuItemsProps {
  issue: Issue;
  actions: UseIssueActionsResult;
  primitives: MenuPrimitives;
  /** If set, navigate here after the issue is deleted (used by the detail page). */
  onDeletedNavigateTo?: string;
}

export function IssueActionsMenuItems({
  issue,
  actions,
  primitives: P,
  onDeletedNavigateTo,
}: IssueActionsMenuItemsProps) {
  const { t } = useT("issues");
  const {
    members,
    agents,
    isPinned,
    updateField,
    togglePin,
    copyLink,
    openCreateSubIssue,
    openSetParent,
    openAddChild,
    openDeleteConfirm,
  } = actions;

  const now = () => new Date();
  const inDays = (days: number) => {
    const d = new Date();
    d.setDate(d.getDate() + days);
    return d.toISOString();
  };

  // Subscribe to the issue's task list so the cache is warm by the time the
  // user clicks "Copy local workdir path". The query only fires while the
  // menu is open (Base UI portals the menu content lazily) — list views
  // that wrap every row in IssueActionsContextMenu pay nothing until the
  // menu actually opens.
  //
  // The query shares its key with ExecutionLogSection, so navigating from
  // the issue detail page is a free cache hit.
  const { data: tasks } = useQuery({
    queryKey: issueKeys.tasks(issue.id),
    queryFn: () => api.listTasksByIssue(issue.id),
    staleTime: 30_000,
  });

  // PUL-154: smart-preset list is recomputed each render so the labels
  // ("Tomorrow 09:00", etc.) stay correct as the user keeps the menu open
  // across the midnight rollover. The presets themselves are pure
  // (presetsForNow does no IO), so this is cheap.
  const createReminder = useCreateReminder(issue.id);
  const reminderPresets = presetsForNow(new Date());

  // Synchronous click handler — the awaited fetch in the previous version
  // dropped the browser's transient user activation, which made
  // navigator.clipboard.writeText() reject from the menu when the cache
  // was cold. We now read straight from the cached query result and write
  // to the clipboard inside the same task as the click.
  const handleCopyWorkdirPath = useCallback(() => {
    const latestWorkDir = pickLatestWorkDir(tasks);
    if (!latestWorkDir) {
      toast.error(t(($) => $.detail.workdir_path_unavailable));
      return;
    }
    navigator.clipboard.writeText(latestWorkDir).then(
      () => toast.success(t(($) => $.detail.workdir_path_copied)),
      () => toast.error(t(($) => $.detail.workdir_path_copy_failed)),
    );
  }, [tasks, t]);

  // PUL-266: PDF export. The fetch is fire-and-forget — we show a
  // "generating" toast immediately so the user knows the click was
  // received (gotenberg + Chromium can take a few seconds on long
  // tickets), then swap it for a success / error toast when the
  // promise settles. Download is triggered via a synthesised
  // <a download> element so the browser saves under the filename
  // the server picked in Content-Disposition.
  const handleExportPdf = useCallback(() => {
    const pendingId = toast.loading(t(($) => $.actions.export_pdf_pending));
    api
      .exportIssuePdf(issue.id)
      .then(({ blob, filename }) => {
        triggerDownload(blob, filename);
        toast.success(t(($) => $.actions.export_pdf_success, { filename }), {
          id: pendingId,
        });
      })
      .catch((err) => {
        const message =
          err instanceof Error
            ? err.message
            : t(($) => $.actions.export_pdf_failed);
        toast.error(message, { id: pendingId });
      });
  }, [issue.id, t]);

  return (
    <>
      {/* Status */}
      <P.Sub>
        <P.SubTrigger>
          <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
          {t(($) => $.actions.status)}
        </P.SubTrigger>
        <P.SubContent>
          {ALL_STATUSES.map((s) => (
            <P.Item key={s} onClick={() => updateField({ status: s })}>
              <StatusIcon status={s} className="h-3.5 w-3.5" />
              {t(($) => $.status[s])}
              {issue.status === s && (
                <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
              )}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* Priority */}
      <P.Sub>
        <P.SubTrigger>
          <PriorityIcon priority={issue.priority} />
          {t(($) => $.actions.priority)}
        </P.SubTrigger>
        <P.SubContent>
          {PRIORITY_ORDER.map((p) => (
            <P.Item key={p} onClick={() => updateField({ priority: p })}>
              <span
                className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${PRIORITY_CONFIG[p].badgeBg} ${PRIORITY_CONFIG[p].badgeText}`}
              >
                <PriorityIcon priority={p} className="h-3 w-3" inheritColor />
                {t(($) => $.priority[p])}
              </span>
              {issue.priority === p && (
                <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
              )}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* Assignee */}
      <P.Sub>
        <P.SubTrigger>
          <UserMinus className="h-3.5 w-3.5" />
          {t(($) => $.actions.assignee)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item
            onClick={() =>
              updateField({ assignee_type: null, assignee_id: null })
            }
          >
            <UserMinus className="h-3.5 w-3.5 text-muted-foreground" />
            {t(($) => $.actions.unassigned)}
            {!issue.assignee_type && (
              <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
            )}
          </P.Item>
          {members.map((m) => (
            <P.Item
              key={m.user_id}
              onClick={() =>
                updateField({ assignee_type: "member", assignee_id: m.user_id })
              }
            >
              <ActorAvatar actorType="member" actorId={m.user_id} size={16} />
              {m.name}
              {issue.assignee_type === "member" &&
                issue.assignee_id === m.user_id && (
                  <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
                )}
            </P.Item>
          ))}
          {agents.map((a) => (
            <P.Item
              key={a.id}
              onClick={() =>
                updateField({ assignee_type: "agent", assignee_id: a.id })
              }
            >
              <ActorAvatar actorType="agent" actorId={a.id} size={16} />
              {a.name}
              {issue.assignee_type === "agent" && issue.assignee_id === a.id && (
                <span className="ml-auto text-xs text-muted-foreground">{"✓"}</span>
              )}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* Due date */}
      <P.Sub>
        <P.SubTrigger>
          <Calendar className="h-3.5 w-3.5" />
          {t(($) => $.actions.due_date)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item onClick={() => updateField({ due_date: now().toISOString() })}>
            {t(($) => $.actions.due_today)}
          </P.Item>
          <P.Item onClick={() => updateField({ due_date: inDays(1) })}>
            {t(($) => $.actions.due_tomorrow)}
          </P.Item>
          <P.Item onClick={() => updateField({ due_date: inDays(7) })}>
            {t(($) => $.actions.due_next_week)}
          </P.Item>
          {issue.due_date && (
            <>
              <P.Separator />
              <P.Item onClick={() => updateField({ due_date: null })}>
                {t(($) => $.actions.due_clear)}
              </P.Item>
            </>
          )}
        </P.SubContent>
      </P.Sub>

      <P.Separator />

      <P.Item onClick={togglePin}>
        {isPinned ? (
          <PinOff className="h-3.5 w-3.5" />
        ) : (
          <Pin className="h-3.5 w-3.5" />
        )}
        {isPinned ? t(($) => $.actions.unpin_from_sidebar) : t(($) => $.actions.pin_to_sidebar)}
      </P.Item>
      <P.Item onClick={copyLink}>
        <Link2 className="h-3.5 w-3.5" />
        {t(($) => $.actions.copy_link)}
      </P.Item>
      <P.Item onClick={handleCopyWorkdirPath}>
        <FolderOpen className="h-3.5 w-3.5" />
        {t(($) => $.actions.copy_workdir_path)}
      </P.Item>
      {/* PUL-266: PDF export. Whole-ticket variant; the thread
          variant lives in the comment-card overflow menu so users
          reach for it where the thread is, not where the ticket
          actions live. */}
      <P.Item onClick={handleExportPdf}>
        <FileDown className="h-3.5 w-3.5" />
        {t(($) => $.actions.export_pdf)}
      </P.Item>

      <P.Separator />

      {/* PUL-154: «Wake up in N» — one-shot reminders. The submenu shows
          four smart presets keyed to the local time of day; the absolute
          fire_at is computed client-side and sent to the server as UTC ISO.
          TODO(follow-up): "Custom..." row that opens a date+time picker
          modal, plus an i18n bundle for the labels. Hardcoded English for
          v1 because the dropdown ships with English defaults elsewhere. */}
      <P.Sub>
        <P.SubTrigger>
          <Clock className="h-3.5 w-3.5" />
          {"Wake up in..."}
        </P.SubTrigger>
        <P.SubContent>
          {reminderPresets.map((p) => (
            <P.Item
              key={p.key}
              onClick={() => {
                createReminder.mutate(
                  { fire_at: p.fireAt.toISOString() },
                  {
                    onSuccess: () => {
                      toast.success(`Wake-up set for ${p.label.toLowerCase()}`);
                    },
                    onError: (err) => {
                      toast.error(
                        err instanceof Error ? err.message : "Failed to set wake-up",
                      );
                    },
                  },
                );
              }}
            >
              <Clock className="h-3.5 w-3.5" />
              {p.label}
            </P.Item>
          ))}
        </P.SubContent>
      </P.Sub>

      {/* Relationship actions live under "More" — they're lower-frequency and
          will grow (blocks, duplicates, related) as we add more relation types. */}
      <P.Sub>
        <P.SubTrigger>
          <MoreHorizontal className="h-3.5 w-3.5" />
          {t(($) => $.actions.more)}
        </P.SubTrigger>
        <P.SubContent>
          <P.Item onClick={openCreateSubIssue}>
            <Plus className="h-3.5 w-3.5" />
            {t(($) => $.actions.create_sub_issue)}
          </P.Item>
          <P.Item onClick={openSetParent}>
            <ArrowUp className="h-3.5 w-3.5" />
            {t(($) => $.actions.set_parent_issue)}
          </P.Item>
          <P.Item onClick={openAddChild}>
            <ArrowDown className="h-3.5 w-3.5" />
            {t(($) => $.actions.add_sub_issue)}
          </P.Item>
        </P.SubContent>
      </P.Sub>

      <P.Separator />

      <P.Item
        variant="destructive"
        onClick={() => openDeleteConfirm({ onDeletedNavigateTo })}
      >
        <Trash2 className="h-3.5 w-3.5" />
        {t(($) => $.actions.delete_issue)}
      </P.Item>
    </>
  );
}

/**
 * PUL-266: trigger a browser download for a Blob. We synthesise an
 * <a download> anchor instead of opening a new tab so iOS Safari's
 * Files app receives the file directly under the server-picked
 * filename. The blob URL is revoked on the next microtask so we
 * don't leak memory across many exports in one session.
 *
 * Lives here rather than in @multica/core/utils because no other
 * call site needs it yet — extract on the second consumer.
 */
export function triggerDownload(blob: Blob, filename: string): void {
  if (typeof window === "undefined") return; // SSR safety
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  document.body.removeChild(anchor);
  // Defer revoke so the browser has time to start the download.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function pickLatestWorkDir(tasks: AgentTask[] | undefined): string | undefined {
  if (!tasks?.length) return undefined;
  let latest: AgentTask | undefined;
  for (const task of tasks) {
    if (!task.work_dir) continue;
    if (!latest || task.created_at > latest.created_at) {
      latest = task;
    }
  }
  return latest?.work_dir;
}
