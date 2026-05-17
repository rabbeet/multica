"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { skillListOptions } from "@multica/core/workspace/queries";
import type { SkillSummary } from "@multica/core/types";
import {
  getSkillRecencyMap,
  sortSkillsByRecency,
} from "../../editor/extensions/skill-recency";

interface PopularSkillsBarProps {
  workspaceId: string;
  onPick: (skill: SkillSummary) => void;
  /** Max number of pills shown. Pills past this cap fall off; the dropdown
   *  (`/`-autocomplete) is the catch-all surface for the long tail. */
  limit?: number;
}

const DEFAULT_LIMIT = 5;

/**
 * Compact strip of clickable pills under the comment input showing the
 * user's most-recently-used skills in this workspace (PUL-161). Tap a pill →
 * `/skill-name ` is inserted at the editor cursor.
 *
 * Returns `null` when the workspace has zero skills. No empty state, no
 * placeholder height — keeps the input area tight on mobile.
 */
export function PopularSkillsBar({
  workspaceId,
  onPick,
  limit = DEFAULT_LIMIT,
}: PopularSkillsBarProps) {
  const { data: allSkills } = useQuery({
    ...skillListOptions(workspaceId),
    enabled: !!workspaceId,
  });

  const ranked = useMemo(() => {
    if (!allSkills || allSkills.length === 0) return [];
    const recency = getSkillRecencyMap(workspaceId);
    return sortSkillsByRecency(allSkills, recency).slice(0, limit);
  }, [allSkills, workspaceId, limit]);

  if (ranked.length === 0) return null;

  return (
    <div
      role="toolbar"
      aria-label="Popular skills"
      className="mt-2 flex items-center gap-1.5 overflow-x-auto"
    >
      {ranked.map((skill) => (
        <button
          key={skill.id}
          type="button"
          onClick={() => onPick(skill)}
          aria-label={`Insert /${skill.name}`}
          title={skill.description || undefined}
          className="shrink-0 cursor-pointer rounded-full border border-transparent bg-secondary px-2.5 py-1 text-xs font-medium text-secondary-foreground transition-colors hover:bg-accent focus-visible:border-ring focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          /{skill.name}
        </button>
      ))}
    </div>
  );
}
