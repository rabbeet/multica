"use client";

import { useState } from "react";
import { Inbox, RotateCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { useDensityStore } from "@multica/core/issues/stores";
import { useActionInbox } from "../hooks/use-action-inbox";
import { ActionInboxRow } from "./action-inbox-row";
import { useT } from "../../i18n";
import { cn } from "@multica/ui/lib/utils";

/**
 * PUL-231 Mission Control — workspace-level action inbox.
 *
 * Sectioned feed: "Needs your action" (issues whose latest agent
 * comment carries open questions or an ack-gated command block)
 * always renders open at the top; "In progress" (everything else
 * still active) collapses by default to keep the first screen tight.
 *
 * Density toggle lives in the header bar; per-row density override
 * arrives in PR2.5 if needed (single-density today keeps the surface
 * coherent for a mobile owner-only user).
 *
 * Polling cadence: every 15s via react-query refetchInterval (see
 * actionInboxOptions). WS subscription is a planned PR2.5 swap.
 */
export function MissionControl() {
  const { t } = useT("inbox");
  const wsId = useWorkspaceId();
  const density = useDensityStore((s) => s.density);
  const toggleDensity = useDensityStore((s) => s.toggle);
  const [showInProgress, setShowInProgress] = useState(false);

  const { isLoading, isError, refetch, needsAction, inProgress, total } =
    useActionInbox(wsId);

  return (
    <main
      className="mx-auto flex w-full max-w-4xl flex-col gap-4 px-4 py-6 md:px-6"
      aria-label={t(($) => $.mission_control.title)}
    >
      <header className="flex items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">
          {t(($) => $.mission_control.title)}
        </h1>
        <Button
          variant="ghost"
          size="sm"
          onClick={toggleDensity}
          aria-label={t(($) => $.mission_control.density_toggle_aria)}
          data-density-toggle={density}
        >
          {density === "compact"
            ? t(($) => $.mission_control.density_compact)
            : t(($) => $.mission_control.density_expanded)}
        </Button>
      </header>

      {isLoading ? (
        <MissionControlSkeleton />
      ) : isError ? (
        <MissionControlError onRetry={refetch} />
      ) : total === 0 ? (
        <MissionControlEmpty hasActive={false} activeCount={0} />
      ) : (
        <>
          {needsAction.length > 0 ? (
            <section
              aria-label={t(($) => $.mission_control.needs_action, {
                count: needsAction.length,
              })}
              className="rounded-lg border bg-card"
            >
              <h2 className="border-b border-border/40 px-4 py-2 text-sm font-semibold">
                {t(($) => $.mission_control.needs_action, {
                  count: needsAction.length,
                })}
              </h2>
              <div className={cn("divide-y divide-border/30")}>
                {needsAction.map((row) => (
                  <ActionInboxRow key={row.item.id} row={row} density={density} />
                ))}
              </div>
            </section>
          ) : (
            <MissionControlEmpty hasActive={inProgress.length > 0} activeCount={inProgress.length} />
          )}

          {inProgress.length > 0 ? (
            <section
              aria-label={t(($) => $.mission_control.in_progress_collapsed, {
                count: inProgress.length,
              })}
              className="rounded-lg border bg-card"
            >
              <button
                type="button"
                onClick={() => setShowInProgress((v) => !v)}
                className="w-full border-b border-border/40 px-4 py-2 text-left text-sm font-medium text-muted-foreground hover:text-foreground"
                aria-expanded={showInProgress}
              >
                {t(($) => $.mission_control.in_progress_collapsed, {
                  count: inProgress.length,
                })}
              </button>
              {showInProgress ? (
                <div className="divide-y divide-border/30">
                  {inProgress.map((row) => (
                    <ActionInboxRow key={row.item.id} row={row} density={density} />
                  ))}
                </div>
              ) : null}
            </section>
          ) : null}
        </>
      )}
    </main>
  );
}

function MissionControlSkeleton() {
  return (
    <div className="rounded-lg border bg-card divide-y divide-border/30">
      {Array.from({ length: 6 }, (_, i) => (
        <div key={i} className="flex items-center gap-3 px-4 py-3">
          <Skeleton className="size-4" />
          <Skeleton className="size-4" />
          <Skeleton className="h-3 w-16" />
          <Skeleton className="h-4 flex-1" />
        </div>
      ))}
    </div>
  );
}

function MissionControlEmpty({
  hasActive,
  activeCount,
}: {
  hasActive: boolean;
  activeCount: number;
}) {
  const { t } = useT("inbox");
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed bg-card/50 px-6 py-10 text-center">
      <Inbox className="size-8 text-muted-foreground" aria-hidden />
      <p className="text-base font-medium">{t(($) => $.mission_control.inbox_zero_headline)}</p>
      <p className="text-sm text-muted-foreground">
        {hasActive
          ? t(($) => $.mission_control.inbox_zero_subtitle_with_active, {
              count: activeCount,
            })
          : t(($) => $.mission_control.inbox_zero_subtitle_no_active)}
      </p>
    </div>
  );
}

function MissionControlError({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("inbox");
  return (
    <div className="flex flex-col items-center justify-center gap-3 rounded-lg border border-dashed border-destructive/40 bg-destructive/5 px-6 py-10 text-center">
      <p className="text-sm font-medium">{t(($) => $.mission_control.error_title)}</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        <RotateCw className="size-3.5" />
        {t(($) => $.mission_control.error_retry)}
      </Button>
    </div>
  );
}
