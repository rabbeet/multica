import type { IssuePriority } from "@multica/core/types";
import { PRIORITY_CONFIG } from "@multica/core/issues/config";

export function PriorityIcon({
  priority,
  className = "",
  inheritColor = false,
}: {
  priority: IssuePriority;
  className?: string;
  inheritColor?: boolean;
}) {
  // PUL-199 follow-up: mirror the StatusIcon defensive fallback shipped
  // in #46. Without it, an out-of-union `priority` (e.g. via `as
  // IssuePriority` on activity-log `details.to`) would make `cfg.bars` /
  // `cfg.color` throw, and IssueDetail's ErrorBoundary would swallow the
  // surrounding section. The known call-site casts are guarded at the
  // call-site, so there is no in-the-wild repro for the central component
  // — but defense-in-depth applies symmetrically. The map declaration
  // stays `Record<IssuePriority, ...>`, so adding a new priority still
  // triggers a compile error when its config entry is missing.
  const cfg = PRIORITY_CONFIG[priority] ?? PRIORITY_CONFIG.none;

  // "none" — simple horizontal dashes
  if (cfg.bars === 0) {
    return (
      <svg
        viewBox="0 0 16 16"
        className={`h-3.5 w-3.5 ${inheritColor ? "" : "text-muted-foreground"} shrink-0 ${className}`}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      >
        <line x1="3" y1="8" x2="13" y2="8" />
      </svg>
    );
  }

  const isUrgent = priority === "urgent";

  return (
    <svg
      viewBox="0 0 16 16"
      className={`h-3.5 w-3.5 ${inheritColor ? "" : cfg.color} shrink-0 ${className}`}
      fill="currentColor"
      style={isUrgent ? { animation: "priority-pulse 2s ease-in-out infinite" } : undefined}
    >
      {[0, 1, 2, 3].map((i) => (
        <rect
          key={i}
          x={1 + i * 4}
          width="3"
          rx="0.5"
          style={{
            y: 12 - (i + 1) * 3,
            height: (i + 1) * 3,
            opacity: i < cfg.bars ? 1 : 0.2,
            transition: "y 0.2s ease, height 0.2s ease, opacity 0.2s ease",
          }}
        />
      ))}
      {isUrgent && (
        <style>{`@keyframes priority-pulse{0%,100%{transform:scale(1)}50%{transform:scale(1.08)}}`}</style>
      )}
    </svg>
  );
}
