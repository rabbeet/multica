"use client";

import { ChevronRight, MessageCircleQuestion, Terminal } from "lucide-react";
import { AppLink } from "../../navigation";
import { useWorkspacePaths } from "@multica/core/paths";
import { useTimeAgo } from "../../common/hooks/use-time-ago";
import { useT } from "../../i18n";
import { useWorkspaceId } from "@multica/core/hooks";
import { StatusIcon, PriorityIcon } from "../../issues/components";
import {
  AgentQuestionChips,
  AgentCommandButton,
} from "../../issues/components/agent-action-chips";
import { AuthorContextProvider } from "../../editor/context/author-context";
import { useLastVisitSync } from "../hooks/use-last-visit-sync";
import type { ActionInboxRow as ActionInboxRowData } from "../hooks/use-action-inbox";
import type { InboxDensity } from "@multica/core/issues/stores";
import { cn } from "@multica/ui/lib/utils";

/**
 * One row in the Mission Control feed.
 *
 * Two density modes (PUL-231 plan rev3 §design):
 *  - compact: 2 lines, identifier + title + chip row inline, 5-6 rows
 *    visible on iPhone 13 in the first screen.
 *  - expanded: 4-line layout with full delta-prose.
 *
 * The chip row reuses the same primitives as the per-ticket chips
 * (PR1) — but wraps each in an `AuthorContextProvider` carrying the
 * comment's id + issueId, so chip taps post replies to the correct
 * parent comment in the correct issue.
 *
 * Row body tap navigates to issue detail; chip taps are isolated by
 * the chip-zone's own stopPropagation guard.
 */
export function ActionInboxRow({
  row,
  density,
}: {
  row: ActionInboxRowData;
  density: InboxDensity;
}) {
  const { t } = useT("inbox");
  const timeAgo = useTimeAgo();
  const paths = useWorkspacePaths();
  const wsId = useWorkspaceId();
  const { markVisited } = useLastVisitSync(wsId);

  const { item, actions } = row;
  const commentId = item.latest_agent_comment?.id ?? null;
  const issueId = item.id;

  return (
    <article
      data-testid="action-inbox-row"
      data-density={density}
      data-needs-action={actions.questions.length + actions.commands.length > 0 ? "true" : "false"}
      className={cn(
        "group flex flex-col gap-2 border-b border-border/40 last:border-b-0 px-4 py-3 transition-colors hover:bg-accent/30",
        density === "compact" && "py-2.5",
      )}
    >
      <div className="flex items-center gap-3">
        <StatusIcon
          status={item.status}
          className="h-4 w-4 shrink-0"
        />
        <PriorityIcon priority={item.priority} className="h-4 w-4 shrink-0 opacity-70" />
        <span className="font-mono tabular-nums w-[8ch] shrink-0 text-xs text-muted-foreground">
          {item.identifier}
        </span>
        <AppLink
          href={paths.issueDetail(item.id)}
          onClick={() => markVisited(item.id)}
          aria-label={t(($) => $.mission_control.open_details_aria, {
            identifier: item.identifier,
          })}
          className={cn(
            "min-w-0 flex-1 truncate text-sm font-medium text-foreground hover:underline",
          )}
        >
          {item.title}
        </AppLink>
        {item.latest_agent_comment ? (
          <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
            {timeAgo(item.latest_agent_comment.created_at)}
          </span>
        ) : null}
        <ChevronRight className="size-4 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
      </div>

      {/* Chip rows — reuse PR1 primitives with the per-row AuthorContext. */}
      {commentId && issueId && (actions.questions.length > 0 || actions.commands.length > 0) ? (
        <AuthorContextProvider
          value={{
            isAgent: true,
            commentId,
            issueId,
          }}
        >
          <div className={cn("flex flex-col gap-1.5", density === "compact" ? "pl-7" : "pl-9")}>
            {actions.questions.map((q) => (
              <div key={q.id} className="flex items-start gap-2">
                <MessageCircleQuestion
                  className="size-3.5 shrink-0 text-muted-foreground translate-y-1.5"
                  aria-hidden
                />
                {density === "expanded" ? (
                  <div className="min-w-0 flex-1">
                    <p className="text-xs text-muted-foreground mb-1">{q.title}</p>
                    <AgentQuestionChips
                      ordinal={q.ordinal}
                      title={q.title}
                      defaultVariant={q.default}
                      variants={q.variants}
                    />
                  </div>
                ) : (
                  <AgentQuestionChips
                    ordinal={q.ordinal}
                    title={q.title}
                    defaultVariant={q.default}
                    variants={q.variants}
                  />
                )}
              </div>
            ))}
            {actions.commands.map((c) => (
              <div key={c.id} className="flex items-start gap-2">
                <Terminal
                  className="size-3.5 shrink-0 text-muted-foreground translate-y-1.5"
                  aria-hidden
                />
                <AgentCommandButton
                  ordinal={c.ordinal}
                  commands={c.commands}
                  groupLabel={c.groupLabel}
                />
              </div>
            ))}
          </div>
        </AuthorContextProvider>
      ) : null}
    </article>
  );
}
