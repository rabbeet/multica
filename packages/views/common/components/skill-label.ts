// PUL-177 skill chip labelling helper.
//
// Two-letter / three-letter abbreviations only — labels are visually
// scanned in a 25-row Inbox at small font size, and full slug names
// like "/plan-eng-review" wouldn't fit alongside the phase chip.
// Tooltip text carries the full slug; see LastSkillChip.
//
// Lives in `views/common/components/` (not inbox/) because the
// SkillHistory panel on the issue detail page reuses the same label
// algorithm. Per /plan-eng-review decision 2A, chips that cross
// inbox/issues live in common; phase-chip stays inbox-local.

// Priority skills — the four /office-hours, /plan-* skills that
// drive the planning ritual. They get human-curated short labels
// (OH/CEO/ENG/DES) instead of the algorithmic fallback so the four
// chips Vadim cares about most are readable at a glance.
export const PRIORITY_LABELS: Record<string, string> = {
  "office-hours": "OH",
  "plan-ceo-review": "CEO",
  "plan-eng-review": "ENG",
  "plan-design-review": "DES",
};

// fallbackSkillLabel builds a 1-3 character label for any slug that
// isn't in PRIORITY_LABELS. Algorithm:
//
//   - single-segment slug ("qa", "ship", "investigate") → first 3
//     letters uppercased: "QA", "SHI", "INV".
//   - multi-segment slug ("plan-and-implement", "design-review") →
//     first letter of each segment, up to 3 chars: "PAI", "DR".
//
// Collisions are possible (plan-and-implement / plan-and-investigate
// both abbreviate to PAI). Tooltip is the canonical disambiguator;
// PUL-177 v4 plan flags collision-aware suffixes (PAI/PAI2) as a
// future polish, not v1 scope.
export function fallbackSkillLabel(slug: string): string {
  if (!slug) return "?";
  const parts = slug.split("-").filter(Boolean);
  if (parts.length === 0) return "?";
  const first = parts[0];
  if (parts.length === 1 && first) {
    return first.slice(0, 3).toUpperCase();
  }
  return parts
    .slice(0, 3)
    .map((p) => (p[0] ?? "").toUpperCase())
    .join("");
}

export function labelForSkill(slug: string): string {
  return PRIORITY_LABELS[slug] ?? fallbackSkillLabel(slug);
}
