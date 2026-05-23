"use client";

import { useEffect, useMemo } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronDown, ChevronUp, ArrowDown } from "lucide-react";
import {
  issueSkillStatesOptions,
  issueTimelineInfiniteOptions,
} from "@multica/core/issues/queries";
import {
  useLastVisitStore,
  useTldrCollapseStore,
} from "@multica/core/issues/stores";
import { useLastVisitSync } from "../../inbox/hooks/use-last-visit-sync";
import { Button } from "@multica/ui/components/ui/button";
import { extractAgentActions } from "../../editor/utils/preprocess-agent-actions";
import { collectAnsweredQuestionIds } from "../../editor/utils/answer-markers";
import { useT } from "../../i18n";
import { useTimeAgo } from "../../common/hooks/use-time-ago";
import type { TimelineEntry } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

/**
 * PUL-231 PR3 — per-ticket "Where we are" header.
 *
 * Renders under the description, before the activity timeline. Pulls
 * three signals into one glance-able panel:
 *
 *   1. Skill phases (✓ office-hours, ⏳ eng-review, ...) sourced from
 *      issueSkillStatesOptions — same query SkillHistory uses, no
 *      additional roundtrip.
 *   2. Open agent questions / commands across the latest agent comments
 *      in the timeline, computed via the same extractAgentActions()
 *      from PR1 + filtered against threaded child-replies in cache.
 *      Click the "Jump to questions" link and the page scrolls to the
 *      comment whose chips need answering.
 *   3. Last visit timestamp from useLastVisitStore — mounting the
 *      detail page itself marks the visit, so the timestamp shown is
 *      from the *previous* mount. Out of MVP scope per eng-review S2=c:
 *      cross-device sync is a Phase 2 follow-up.
 *
 * Auto-suppresses when there's nothing useful to show (no phases AND
 * no open actions) — the page stays uncluttered for fresh issues.
 */
export function IssueTldrHeader({
  workspaceId,
  issueId,
}: {
  /** PUL-239 — wires server-side last-visit sync. */
  workspaceId: string;
  issueId: string;
}) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { markVisited } = useLastVisitSync(workspaceId);
  const lastVisitMs = useLastVisitStore((s) => s.visits[issueId] ?? null);
  const isCollapsed = useTldrCollapseStore((s) => s.isCollapsed(issueId));
  const toggleCollapse = useTldrCollapseStore((s) => s.toggle);

  const { data: skillStates = [] } = useQuery(issueSkillStatesOptions(issueId));
  const { data: timelineData } = useInfiniteQuery(
    issueTimelineInfiniteOptions(issueId, null),
  );

  // Compute open agent actions across all visible timeline entries.
  // PUL-240 — replaces the prior crude "any child reply ⇒ all
  // answered" rule with per-question matching against the hidden
  // marker each chip-tap reply embeds (see editor/utils/answer-markers.ts).
  // Per-comment memoization happens inside extractAgentActions's call
  // sites elsewhere; here we accept the recompute on data change since
  // the timeline isn't expected to mutate during a single page visit.
  const openAgentEntries = useMemo(() => {
    const all: TimelineEntry[] = [];
    if (timelineData) {
      for (const page of timelineData.pages) {
        for (const entry of page.entries) all.push(entry);
      }
    }
    // Set of question ids that have a matching `<div data-pul240-answer="…">`
    // marker among the child replies — these are the chip-tap answered
    // questions. Command-block ack-sends use the same machinery, so an
    // ack-sent run counts as "answered" too.
    const answeredQuestionIds = collectAnsweredQuestionIds(
      all.map((e) => ({ content: e.content ?? null })),
    );
    const openByComment: { commentId: string; openCount: number }[] = [];
    for (const e of all) {
      if (e.type !== "comment" || e.actor_type !== "agent") continue;
      const actions = extractAgentActions(e.content ?? "", e.id);
      let openCount = 0;
      for (const q of actions.questions) {
        if (!answeredQuestionIds.has(q.id)) openCount++;
      }
      for (const c of actions.commands) {
        if (!answeredQuestionIds.has(c.id)) openCount++;
      }
      if (openCount > 0) openByComment.push({ commentId: e.id, openCount });
    }
    return openByComment;
  }, [timelineData]);

  // Mark the visit on first mount AFTER recording the prior timestamp.
  // The ref dance avoids re-marking on every re-render.
  useEffect(() => {
    markVisited(issueId);
  }, [issueId, markVisited]);

  const totalOpen = openAgentEntries.reduce((sum, e) => sum + e.openCount, 0);
  const hasContent = skillStates.length > 0 || totalOpen > 0;

  // PUL-231 §design: header auto-suppresses for fresh issues with no
  // signals to surface. Keeps the description-first reading flow intact.
  if (!hasContent) return null;

  const jumpToFirstOpen = () => {
    const first = openAgentEntries[0];
    if (!first) return;
    const el = typeof document !== "undefined"
      ? document.getElementById(`comment-${first.commentId}`)
      : null;
    el?.scrollIntoView({ behavior: "smooth", block: "start" });
  };

  return (
    <section
      data-testid="issue-tldr-header"
      aria-labelledby={`tldr-${issueId}`}
      className={cn(
        "my-4 rounded-lg border bg-card/50 px-4 py-3",
        isCollapsed && "py-2",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <h2
          id={`tldr-${issueId}`}
          className="text-sm font-semibold text-foreground"
        >
          📍 {t(($) => $.tldr_header.title)}
        </h2>
        <button
          type="button"
          onClick={() => toggleCollapse(issueId)}
          aria-label={t(($) => $.tldr_header.toggle_collapse)}
          aria-expanded={!isCollapsed}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          {isCollapsed ? (
            <ChevronDown className="size-4" aria-hidden />
          ) : (
            <ChevronUp className="size-4" aria-hidden />
          )}
        </button>
      </div>

      {!isCollapsed ? (
        <div className="mt-2 flex flex-col gap-1.5 text-sm">
          {skillStates.map((s) => (
            <div key={s.skill} className="flex items-center gap-2 text-foreground/80">
              <span>
                {s.status === "done"
                  ? t(($) => $.tldr_header.skill_done, { skill: s.skill })
                  : t(($) => $.tldr_header.skill_in_progress, { skill: s.skill })}
              </span>
            </div>
          ))}

          {totalOpen > 0 ? (
            <div className="mt-1 flex items-center gap-2">
              <span className="text-foreground">
                {t(($) => $.tldr_header.open_questions, { count: totalOpen })}
              </span>
              <Button size="xs" variant="outline" onClick={jumpToFirstOpen}>
                <ArrowDown className="size-3" aria-hidden />
                {t(($) => $.tldr_header.open_questions_jump)}
              </Button>
            </div>
          ) : null}

          <p className="mt-1 text-xs text-muted-foreground">
            {lastVisitMs !== null
              ? t(($) => $.tldr_header.last_visit, {
                  relative: timeAgo(new Date(lastVisitMs).toISOString()),
                })
              : t(($) => $.tldr_header.last_visit_unknown)}
          </p>
        </div>
      ) : null}
    </section>
  );
}
