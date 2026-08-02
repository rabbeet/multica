"use client";

import { useMemo, useState } from "react";
import {
  Bell,
  Check,
  ChevronRight,
  Clock3,
  Database,
  MessageSquare,
  Plus,
  RefreshCw,
  Search,
} from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { Switch } from "@multica/ui/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { cn } from "@multica/ui/lib/utils";
import type { AlertRule, AlertSeverity } from "../fixtures";
import { useT } from "../../i18n";

interface AlertsPageProps {
  alerts: AlertRule[];
  preview?: boolean;
}

type SeverityFilter = "all" | AlertSeverity;

export function AlertsPage({ alerts, preview = false }: AlertsPageProps) {
  const { t } = useT("alerts");
  const [search, setSearch] = useState("");
  const [severity, setSeverity] = useState<SeverityFilter>("all");
  const [selected, setSelected] = useState<AlertRule | null>(null);

  const filtered = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    return alerts.filter((alert) => {
      if (severity !== "all" && alert.severity !== severity) return false;
      return !query || `${alert.name} ${alert.description}`.toLocaleLowerCase().includes(query);
    });
  }, [alerts, search, severity]);

  const criticalCount = alerts.filter((alert) => alert.severity === "critical").length;
  const warningCount = alerts.filter((alert) => alert.severity === "warning").length;
  const firingCount = alerts.filter((alert) => alert.state === "firing").length;
  const enabledCount = alerts.filter((alert) => alert.enabled).length;

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto bg-muted/20">
      <header className="sticky top-0 z-10 border-b bg-background/95 px-4 py-3 backdrop-blur md:px-6">
        <div className="mx-auto flex w-full max-w-5xl items-center justify-between">
          <div className="flex min-w-0 items-center gap-2">
            <Bell className="size-4 shrink-0 text-muted-foreground" />
            <h1 className="text-base font-semibold tracking-tight">{t(($) => $.page.title)}</h1>
            <span className="font-mono text-xs tabular-nums text-muted-foreground">{alerts.length}</span>
          </div>
          <Button size="sm" className="min-h-9" disabled title={t(($) => $.page.new_alert_disabled)}>
            <Plus className="size-4" />
            <span className="hidden sm:inline">{t(($) => $.page.new_alert)}</span>
          </Button>
        </div>
      </header>

      <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-3 px-3 py-4 md:px-6">
        {preview ? (
          <div role="status" className="rounded-xl border border-warning/30 bg-warning/10 px-3 py-2.5 text-xs text-foreground">
            <p className="font-medium">{t(($) => $.page.preview_data)}</p>
            <p className="mt-1 text-muted-foreground">{t(($) => $.page.preview_controls_disabled)}</p>
          </div>
        ) : (
          <section className="grid grid-cols-2 gap-2" aria-label={t(($) => $.page.summary_label)}>
            <SummaryCard
              label={t(($) => $.page.current_status)}
              value={firingCount > 0 ? t(($) => $.page.firing_count, { count: firingCount }) : t(($) => $.page.all_quiet)}
              tone={firingCount === 0 ? "success" : undefined}
            />
            <SummaryCard label={t(($) => $.page.enabled_count_label)} value={t(($) => $.page.enabled_count, { enabled: enabledCount, total: alerts.length })} />
          </section>
        )}

        <div className="relative">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            aria-label={t(($) => $.page.search)}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder={t(($) => $.page.search)}
            className="h-11 bg-background pl-9"
          />
        </div>

        <div className="-mx-3 flex gap-2 overflow-x-auto px-3 pb-1 md:mx-0 md:px-0" aria-label={t(($) => $.page.severity_filters)}>
          <FilterChip active={severity === "all"} onClick={() => setSeverity("all")}>{t(($) => $.page.filter_all, { count: alerts.length })}</FilterChip>
          <FilterChip active={severity === "critical"} onClick={() => setSeverity("critical")}>{t(($) => $.page.filter_critical, { count: criticalCount })}</FilterChip>
          <FilterChip active={severity === "warning"} onClick={() => setSeverity("warning")}>{t(($) => $.page.filter_warning, { count: warningCount })}</FilterChip>
        </div>

        <section className="flex flex-col gap-2" aria-label={t(($) => $.page.rules_label)}>
          {filtered.map((alert) => (
            <AlertCard
              key={alert.id}
              alert={alert}
              onOpen={() => setSelected(alert)}
            />
          ))}
          {filtered.length === 0 && (
            <div className="rounded-xl border bg-background px-4 py-12 text-center text-sm text-muted-foreground">
              {t(($) => $.page.no_matches)}
            </div>
          )}
        </section>
      </main>

      <AlertInspector alert={selected} open={selected !== null} onOpenChange={(open) => !open && setSelected(null)} />
    </div>
  );
}

function SummaryCard({ label, value, tone }: { label: string; value: string; tone?: "success" }) {
  return (
    <div className="rounded-xl border bg-background px-3 py-2.5">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className={cn("mt-1 text-sm font-semibold", tone === "success" && "text-success")}>{value}</p>
    </div>
  );
}

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button
      type="button"
      variant={active ? "default" : "outline"}
      size="sm"
      aria-pressed={active}
      onClick={onClick}
      className="min-h-9 shrink-0 rounded-full px-3 text-xs"
    >
      {children}
    </Button>
  );
}

function AlertCard({
  alert,
  onOpen,
}: {
  alert: AlertRule;
  onOpen: () => void;
}) {
  const { t } = useT("alerts");
  const healthy = alert.state === "healthy" && alert.enabled;
  const firing = alert.state === "firing";
  return (
    <article className="rounded-xl border bg-background shadow-xs">
      <div className="flex items-start gap-3 p-3.5">
        <div className={cn(
          "flex size-9 shrink-0 items-center justify-center rounded-lg",
          healthy && "bg-success/10 text-success",
          firing && "bg-destructive/10 text-destructive",
          !healthy && !firing && "bg-muted text-muted-foreground",
        )}>
          {firing ? <Bell className="size-4" /> : <Check className="size-4" />}
        </div>
        <button
          type="button"
          aria-label={t(($) => $.card.open, { name: alert.name })}
          onClick={onOpen}
          className="min-h-11 min-w-0 flex-1 text-left focus-visible:rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <h2 className="truncate text-sm font-medium">{alert.name}</h2>
          <p className="mt-1 truncate text-xs text-muted-foreground">{alert.description}</p>
        </button>
        <Switch
          checked={alert.enabled}
          disabled
          aria-label={alert.enabled ? t(($) => $.card.pause, { name: alert.name }) : t(($) => $.card.enable, { name: alert.name })}
          title={t(($) => $.card.controls_disabled)}
          className="mt-1 after:-inset-y-3"
        />
      </div>
      <div className="flex flex-wrap gap-1.5 px-3.5 pb-3">
        <Badge variant="outline" className="gap-1.5 rounded-md px-2 py-1 text-[10px] font-normal text-muted-foreground">
          <Database className="size-3" />
          {alert.source}
        </Badge>
        <Badge variant="outline" className="gap-1.5 rounded-md px-2 py-1 text-[10px] font-normal text-muted-foreground">
          <RefreshCw className="size-3" />
          {alert.schedule.cadence}
        </Badge>
        <Badge variant="outline" className="gap-1.5 rounded-md px-2 py-1 text-[10px] font-normal text-muted-foreground">
          <Clock3 className="size-3" />
          {alert.schedule.dataWindow}
        </Badge>
      </div>
      <button
        type="button"
        onClick={onOpen}
        aria-label={t(($) => $.card.open_details, { name: alert.name })}
        className="flex min-h-12 w-full items-center gap-2 border-t px-3.5 text-left text-[11px] text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        <span className="flex min-w-0 flex-1 items-center gap-1.5 truncate"><MessageSquare className="size-3.5 shrink-0" />{alert.delivery.channel}</span>
        <ChevronRight className="size-4 shrink-0" />
      </button>
      <div className="sr-only">{alert.severity}</div>
    </article>
  );
}

function AlertInspector({ alert, open, onOpenChange }: { alert: AlertRule | null; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useT("alerts");
  const status = alert?.state === "firing"
    ? t(($) => $.inspector.status_firing, { evaluation: alert.lastEvaluation })
    : alert?.state === "paused" || alert?.enabled === false
      ? t(($) => $.inspector.status_paused, { evaluation: alert.lastEvaluation })
      : alert
        ? t(($) => $.inspector.status_active, { evaluation: alert.lastEvaluation })
        : "";
  const statusTone = alert?.state === "firing"
    ? "text-destructive"
    : alert?.state === "paused" || alert?.enabled === false
      ? "text-muted-foreground"
      : "text-success";
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      {alert && (
        <SheetContent
          side="right"
          aria-label={alert.name}
          className="w-full gap-0 bg-muted/20 p-0 sm:max-w-lg"
        >
          <SheetHeader className="shrink-0 border-b bg-background px-4 pb-3 pt-4 pr-14">
            <div className="flex items-center gap-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
              <span>{alert.id}</span>
              <span>·</span>
              <span>{alert.severity}</span>
            </div>
            <SheetTitle className="mt-2 text-lg leading-tight">{alert.name}</SheetTitle>
            <SheetDescription className={cn("mt-1 flex items-center gap-1.5 text-xs", statusTone)}>
              <span className="size-1.5 rounded-full bg-current" />
              {status}
            </SheetDescription>
          </SheetHeader>

          <Tabs defaultValue="condition" className="min-h-0 flex-1 gap-0 overflow-hidden">
            <TabsList variant="line" className="h-11 w-full shrink-0 justify-start gap-0 overflow-x-auto border-b bg-background px-2">
              <TabsTrigger value="condition" className="min-h-10 px-3 text-xs">{t(($) => $.inspector.tab_condition)}</TabsTrigger>
              <TabsTrigger value="schedule" className="min-h-10 px-3 text-xs">{t(($) => $.inspector.tab_schedule)}</TabsTrigger>
              <TabsTrigger value="delivery" className="min-h-10 px-3 text-xs">{t(($) => $.inspector.tab_delivery)}</TabsTrigger>
              <TabsTrigger value="history" className="min-h-10 px-3 text-xs">{t(($) => $.inspector.tab_history)}</TabsTrigger>
            </TabsList>
            <div className="min-h-0 flex-1 overflow-y-auto p-3 pb-24">
              <TabsContent value="condition" className="space-y-3">
                <InspectorBlock title="Signal">
                  <DataRow label="Source" value={alert.source} />
                  <DataRow label="Metric" value={alert.metric} />
                  <DataRow label="Dimension" value={alert.dimension} />
                </InspectorBlock>
                <InspectorBlock title="Query">
                  <code className="block overflow-x-auto rounded-lg bg-foreground px-3 py-2.5 text-[11px] leading-relaxed text-background">{alert.expression}</code>
                </InspectorBlock>
                <InspectorBlock title="Fires when">
                  <div className="flex flex-wrap gap-1.5">
                    {alert.conditionTokens.map((token) => <Badge key={token} variant="outline" className="text-[11px]">{token}</Badge>)}
                  </div>
                </InspectorBlock>
                {alert.baseline && (
                  <InspectorBlock title="Baseline">
                    <DataRow label="Method" value={alert.baseline.method} />
                    <DataRow label="History" value={alert.baseline.history} />
                    <DataRow label="Comparison" value={alert.baseline.comparison} />
                    {alert.baseline.minimum && <DataRow label="Minimum" value={alert.baseline.minimum} />}
                  </InspectorBlock>
                )}
                <InspectorBlock title="Behavior">
                  <DataRow label="Pending" value={alert.schedule.pending} />
                  <DataRow label="No data" value={alert.handling.noData} />
                  <DataRow label="Query error" value={alert.handling.queryError} />
                </InspectorBlock>
              </TabsContent>

              <TabsContent value="schedule" className="space-y-3">
                <InspectorBlock title="Evaluation schedule">
                  <DataRow label="Cadence" value={alert.schedule.cadence} />
                  <DataRow label="Data window" value={alert.schedule.dataWindow} />
                  <DataRow label="Timezone" value={alert.schedule.timezone} />
                  <DataRow label="Cron" value={alert.schedule.cron} mono />
                </InspectorBlock>
                <InspectorBlock title="Expected frequency">
                  <p className="text-sm leading-relaxed">{alert.expectedFrequency}</p>
                </InspectorBlock>
              </TabsContent>

              <TabsContent value="delivery" className="space-y-3">
                <InspectorBlock title="Channel">
                  <div className="flex items-center gap-3">
                    <div className="flex size-9 items-center justify-center rounded-lg bg-primary text-xs font-bold text-primary-foreground">M</div>
                    <div><p className="text-sm font-medium">{alert.delivery.channel}</p><p className="text-xs text-muted-foreground">{t(($) => $.inspector.new_incident_thread, { platform: alert.delivery.platform })}</p></div>
                  </div>
                </InspectorBlock>
                <InspectorBlock title="Notification contract">
                  {alert.delivery.recoveryInThread && <ContractRow>{t(($) => $.inspector.recovery_thread)}</ContractRow>}
                  <ContractRow>{alert.delivery.chart}</ContractRow>
                  {alert.delivery.nativeGrafanaSuppressed && <ContractRow>{t(($) => $.inspector.grafana_suppressed)}</ContractRow>}
                </InspectorBlock>
                <InspectorBlock title="Routing">
                  <DataRow label="Mention" value={alert.delivery.mention} />
                  <DataRow label="Repeat" value={alert.delivery.repeat} />
                  <DataRow label="Grouping" value={alert.delivery.grouping} />
                  <DataRow label="Transport" value={alert.delivery.transport} />
                </InspectorBlock>
              </TabsContent>

              <TabsContent value="history">
                <InspectorBlock title="Latest evaluation">
                  <DataRow label="Status" value="Successful" />
                  <DataRow label="Evaluated" value={alert.lastEvaluation} />
                  <DataRow label="Incidents today" value="0" />
                </InspectorBlock>
              </TabsContent>
            </div>
          </Tabs>

          <SheetFooter className="absolute inset-x-0 bottom-0 grid grid-cols-2 gap-2 border-t bg-background/95 p-3 backdrop-blur">
            <Button variant="outline" className="min-h-11" disabled>{t(($) => $.inspector.run_check)}</Button>
            <Button className="min-h-11" disabled>{t(($) => $.inspector.edit_alert)}</Button>
          </SheetFooter>
        </SheetContent>
      )}
    </Sheet>
  );
}

function InspectorBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border bg-background p-3.5">
      <h3 className="mb-3 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{title}</h3>
      <div>{children}</div>
    </section>
  );
}

function DataRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-h-9 items-center justify-between gap-4 border-b py-2 text-xs last:border-b-0 last:pb-0 first:pt-0">
      <span className="text-muted-foreground">{label}</span>
      <span className={cn("max-w-[62%] text-right font-medium", mono && "font-mono")}>{value}</span>
    </div>
  );
}

function ContractRow({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-9 items-center gap-2 border-b py-2 text-xs last:border-b-0 last:pb-0 first:pt-0">
      <span className="flex size-4 shrink-0 items-center justify-center rounded bg-primary text-primary-foreground"><Check className="size-3" /></span>
      {children}
    </div>
  );
}
