"use client";

import type { SkillState } from "@multica/core/types";
import { useT } from "../../i18n";
import { useTimeAgo } from "../hooks/use-time-ago";
import { labelForSkill } from "./skill-label";

// PUL-177 LastSkillChip — small chip showing the most recently
// applied skill on a ticket + its current state. Used in two places
// (per /plan-eng-review 2A "split per usage"):
//
//   1. Inbox row (packages/views/inbox/components/inbox-list-item.tsx)
//      — renders item.latest_skill from the ListInbox response,
//      hidden when null.
//
//   2. SkillHistory panel on the issue detail page (packages/views/
//      issues/components/skill-history.tsx) — renders every row in
//      the issue_skill_state table for that ticket.
//
// Styling intent:
//   - done    → solid brand-tinted background with a ✓ glyph and
//               the abbreviation. Reads as "this stage is checked
//               off" at a glance.
//   - in_progress → outline-only with a · glyph. Reads as "still
//               running" / pending.
//
// The tooltip carries the canonical /<slug> name and a relative
// timestamp (uses the shared useTimeAgo hook for parity with the
// existing inbox row time formatting).

export function LastSkillChip({ skill }: { skill: SkillState | null }) {
  const { t } = useT("common");
  const timeAgo = useTimeAgo();
  if (!skill) return null;

  const label = labelForSkill(skill.skill);
  const isDone = skill.status === "done";
  const marker = isDone ? "✓" : "·";
  const ts = isDone ? skill.completed_at ?? skill.updated_at : skill.started_at;
  const relTime = timeAgo(ts);
  const tooltip = isDone
    ? t(($) => $.skill_chip.done, { skill: skill.skill, time: relTime })
    : t(($) => $.skill_chip.in_progress, { skill: skill.skill, time: relTime });

  return (
    <span
      className={`inline-flex h-4 items-center gap-0.5 rounded px-1 text-[10px] font-medium ${
        isDone
          ? "bg-brand/15 text-brand"
          : "border border-muted-foreground/30 text-muted-foreground"
      }`}
      title={tooltip}
      aria-label={tooltip}
    >
      {marker} {label}
    </span>
  );
}
