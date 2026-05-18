"use client";

import type { PhaseSlug } from "@multica/core/types";
import { useT } from "../../i18n";

// PUL-177 PhaseChip — the always-on first chip in the Inbox row.
// Maps the 7 PhaseSlug values to a 3-letter label + Tailwind color
// token. Labels are locale-independent (BAK/PLN/COD/REV/DON/BLK/CAN)
// because they're abbreviations of well-known English workflow
// vocabulary that translates poorly without making the chip wider;
// the tooltip carries the localized name (common.phase.tooltip.*).
//
// Lives in `inbox/components/` rather than `common/` — per
// /plan-eng-review 2A, this component is only consumed by the Inbox
// row, so over-generalizing it to common/ would be premature.
const PHASE_CONFIG: Record<PhaseSlug, { label: string; cls: string }> = {
  backlog: {
    label: "BAK",
    cls: "bg-muted/40 text-muted-foreground",
  },
  planning: {
    label: "PLN",
    cls: "bg-yellow-500/20 text-yellow-700 dark:text-yellow-300",
  },
  coding: {
    label: "COD",
    cls: "bg-blue-500/20 text-blue-700 dark:text-blue-300",
  },
  review: {
    label: "REV",
    cls: "bg-purple-500/20 text-purple-700 dark:text-purple-300",
  },
  done: {
    label: "DON",
    cls: "bg-green-500/20 text-green-700 dark:text-green-300",
  },
  blocked: {
    label: "BLK",
    cls: "bg-red-500/20 text-red-700 dark:text-red-300",
  },
  cancelled: {
    label: "CAN",
    cls: "bg-muted/30 text-muted-foreground/60 line-through",
  },
};

export function PhaseChip({ phase }: { phase: PhaseSlug }) {
  const { t } = useT("common");
  const cfg = PHASE_CONFIG[phase] ?? PHASE_CONFIG.backlog;
  const tooltip = t(($) => $.phase.tooltip[phase]);
  return (
    <span
      className={`inline-flex h-4 items-center rounded px-1 text-[10px] font-medium ${cfg.cls}`}
      title={tooltip}
      aria-label={tooltip}
    >
      {cfg.label}
    </span>
  );
}
