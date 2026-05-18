"use client";

import { useQuery } from "@tanstack/react-query";
import { issueSkillStatesOptions } from "@multica/core/issues/queries";
import { LastSkillChip } from "../../common/components/last-skill-chip";
import { useTimeAgo } from "../../common/hooks/use-time-ago";
import { useT } from "../../i18n";

// PUL-177 SkillHistory — full per-(issue, skill) state matrix on
// the issue detail page. Complements the Inbox row which only
// shows the latest applied skill; this section shows the whole
// history so a user can see "office-hours done, eng-review done,
// design-review in progress" at a glance.
//
// Empty state is rendered explicitly — the section is always on
// the issue detail page (no conditional mount), so an issue with
// no history still gets a one-line "no skills yet" placeholder.
//
// Mounted by issue-detail.tsx between the description and the
// activity timeline.

export function SkillHistory({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { data: states = [] } = useQuery(issueSkillStatesOptions(issueId));

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-semibold text-foreground">
        {t(($) => $.skill_history.title)}
      </h3>
      {states.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {t(($) => $.skill_history.empty)}
        </p>
      ) : (
        <ul className="space-y-1">
          {states.map((s) => {
            const isDone = s.status === "done";
            const ts = isDone ? s.completed_at ?? s.updated_at : s.started_at;
            const subtitle = isDone
              ? t(($) => $.skill_history.done_at, { time: timeAgo(ts) })
              : t(($) => $.skill_history.in_progress_started, { time: timeAgo(ts) });
            return (
              <li
                key={s.skill}
                className="flex items-center justify-between gap-2 text-sm"
              >
                <div className="flex items-center gap-2">
                  <LastSkillChip skill={s} />
                  <span className="text-muted-foreground">/{s.skill}</span>
                </div>
                <span
                  className="shrink-0 text-xs text-muted-foreground"
                  title={s.updated_at}
                >
                  {subtitle}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
