"use client";

import { Bot, Clock, User } from "lucide-react";
import type { OwnershipMeta, OwnershipSlug } from "@multica/core/types";
import { useT } from "../../i18n";
import { useTimeAgo } from "../../common/hooks/use-time-ago";

// PUL-180 OwnershipChip — the third Inbox slot (right of PhaseChip
// and LastSkillChip in `inbox-list-item.tsx`). Answers "who's holding
// the ball right now" — the question /office-hours Q4-reframe in
// PUL-177 identified as the user's real decision when scanning the
// Inbox: "should I open this ticket now".
//
// Three values, all server-derived (see deriveOwnership in
// server/internal/handler/inbox.go):
//
//   me      — assignee=member, OR agent task completed and the ball
//             is back with you. Brand color, User icon, "ME" label.
//   agent   — agent_task_queue has a queued/dispatched/running row
//             on this issue. Blue, Bot icon, "AI" label. Tooltip
//             names the specific agent.
//   waiting — issue.status=waiting → reason="review" (PR/awaiting
//             reviewer) or issue.status=blocked → reason="approval".
//             Amber, Clock icon, "WAIT" label.
//
// Hide rules — chip returns null when:
//   - ownership === null   (server-side: agent recipient, or
//                          phase=done/cancelled, both surfaced as a
//                          null pair to the wire).
//
// File placement: lives in `inbox/components/` (not `common/`)
// because it's only consumed by the Inbox row, matching the PhaseChip
// scoping decision from PUL-177 /plan-eng-review 2A. The tooltip
// hook is inlined per /plan-eng-review C1 — LastSkillChip already
// keeps its tooltip in-file, so this file matches that discipline.
//
// Tooltip discipline: the {{since}} placeholder is appended-inline
// inside each localized message (e.g. "Your move{{since}}") so the
// position of the relative-time suffix is locale-controllable. The
// hook fills it with ", 5m ago" or "" depending on whether
// meta.since is set.

const OWNERSHIP_CONFIG: Record<OwnershipSlug, { label: string; cls: string; Icon: typeof User }> = {
  me: {
    label: "ME",
    cls: "bg-brand/20 text-brand",
    Icon: User,
  },
  agent: {
    label: "AI",
    cls: "bg-blue-500/20 text-blue-700 dark:text-blue-300",
    Icon: Bot,
  },
  waiting: {
    label: "WAIT",
    cls: "bg-amber-500/20 text-amber-700 dark:text-amber-300",
    Icon: Clock,
  },
};

function useOwnershipTooltip(ownership: OwnershipSlug, meta: OwnershipMeta | null): string {
  const { t } = useT("common");
  const timeAgo = useTimeAgo();
  const since = meta?.since ? `, ${timeAgo(meta.since)}` : "";

  switch (ownership) {
    case "me":
      return t(($) => $.ownership.tooltip.me, { since });
    case "agent": {
      const name = meta?.agent_name ?? t(($) => $.ownership.tooltip.agent_unknown);
      return t(($) => $.ownership.tooltip.agent_working, { name, since });
    }
    case "waiting": {
      if (meta?.reason === "approval") {
        return t(($) => $.ownership.tooltip.waiting_approval, { since });
      }
      return t(($) => $.ownership.tooltip.waiting_review, { since });
    }
  }
}

export function OwnershipChip({
  ownership,
  meta,
}: {
  ownership: OwnershipSlug | null;
  meta: OwnershipMeta | null;
}) {
  // Hooks must run unconditionally — we always invoke
  // useOwnershipTooltip with a non-null fallback and discard the
  // result when ownership is null. Cheap (one lookup + a slice of
  // string concatenation).
  const tooltip = useOwnershipTooltip(ownership ?? "me", meta);
  if (!ownership) return null;

  const cfg = OWNERSHIP_CONFIG[ownership];
  return (
    <span
      className={`inline-flex h-4 items-center gap-0.5 rounded px-1 text-[10px] font-medium ${cfg.cls}`}
      title={tooltip}
      aria-label={tooltip}
    >
      <cfg.Icon className="h-2.5 w-2.5" />
      {cfg.label}
    </span>
  );
}
