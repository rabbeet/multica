"use client";

import { useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  ChevronRight,
  Clock3,
  Cpu,
  Database,
  Gauge,
  MemoryStick,
  Network,
  Server,
} from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { cn } from "@multica/ui/lib/utils";
import type { LoadMetricKey, LoadNode, LoadSnapshot } from "../fixtures";
import { useT } from "../../i18n";

interface LoadPageProps {
  snapshot: LoadSnapshot;
  preview?: boolean;
  period?: string;
  onPeriodChange?: (period: string) => void;
}

function useMetricLabels(): Record<LoadMetricKey, string> {
  const { t } = useT("load");
  return {
    cpu: t(($) => $.metrics.cpu),
    memory: t(($) => $.metrics.memory),
    disk: t(($) => $.metrics.disk),
    network: t(($) => $.metrics.network),
  };
}

function usePreviewSnapshot(snapshot: LoadSnapshot, preview: boolean): LoadSnapshot {
  const { t } = useT("load");
  if (!preview) return snapshot;

  const bottleneckCopy: Record<string, { title: string; description: string; chips: string[] }> = {
    "cpu-api-03": {
      title: t(($) => $.preview_copy.cpu_title),
      description: t(($) => $.preview_copy.cpu_description),
      chips: ["CPU 91%", t(($) => $.preview_copy.cpu_duration), t(($) => $.preview_copy.cpu_endpoints)],
    },
    "checkout-queue": {
      title: t(($) => $.preview_copy.queue_title),
      description: t(($) => $.preview_copy.queue_description),
      chips: [t(($) => $.preview_copy.queue_jobs), t(($) => $.preview_copy.queue_growth)],
    },
  };
  const nodeCopy: Record<string, Pick<LoadNode, "summary" | "role" | "uptime">> = {
    "api-prod-03": { summary: t(($) => $.preview_copy.high_load), role: "API", uptime: t(($) => $.preview_copy.uptime_api_03) },
    "worker-prod-02": { summary: t(($) => $.preview_copy.queue_pressure), role: t(($) => $.preview_copy.worker), uptime: t(($) => $.preview_copy.uptime_worker_02) },
    "api-prod-02": { summary: t(($) => $.preview_copy.node_normal), role: "API", uptime: t(($) => $.preview_copy.uptime_api_02) },
    "api-prod-01": { summary: t(($) => $.preview_copy.node_normal), role: "API", uptime: t(($) => $.preview_copy.uptime_api_01) },
    "mysql-primary-01": { summary: t(($) => $.preview_copy.node_normal), role: "MySQL", uptime: t(($) => $.preview_copy.uptime_mysql_01) },
  };

  return {
    ...snapshot,
    updatedAgo: t(($) => $.preview_copy.updated_ago),
    status: t(($) => $.preview_copy.status),
    statusDetail: t(($) => $.preview_copy.status_detail),
    kpis: snapshot.kpis.map((kpi) => ({ ...kpi, trend: kpi.trend === "normal" ? t(($) => $.preview_copy.normal) : kpi.trend })),
    bottlenecks: snapshot.bottlenecks.map((item) => ({ ...item, ...(bottleneckCopy[item.id] ?? {}) })),
    nodes: snapshot.nodes.map((node) => ({ ...node, ...(nodeCopy[node.id] ?? {}) })),
  };
}

export function LoadPage({ snapshot, preview = false, period = "6h", onPeriodChange }: LoadPageProps) {
  const { t } = useT("load");
  const metricLabels = useMetricLabels();
  const viewSnapshot = usePreviewSnapshot(snapshot, preview);
  const [metric, setMetric] = useState<LoadMetricKey>("cpu");
  const [selected, setSelected] = useState<LoadNode | null>(null);

  const rankedNodes = useMemo(
    () => [...viewSnapshot.nodes].sort((left, right) => right.metrics[metric] - left.metrics[metric]),
    [metric, viewSnapshot.nodes],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto bg-muted/20">
      <header className="sticky top-0 z-10 border-b bg-background/95 px-4 py-3 backdrop-blur md:px-6">
        <div className="mx-auto w-full max-w-5xl">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Activity className="size-4 text-muted-foreground" />
              <h1 className="text-base font-semibold">{t(($) => $.page.title)}</h1>
              <span className="font-mono text-xs text-muted-foreground">{viewSnapshot.nodeCount}</span>
            </div>
            <span className="flex items-center gap-1.5 text-[10px] text-success">
              <span className="size-1.5 rounded-full bg-success" />
              {viewSnapshot.updatedAgo}
            </span>
          </div>
          <div className="mt-3 flex gap-1.5 overflow-x-auto">
            {["1h", "6h", "24h", "7d"].map((value) => (
              <Button
                key={value}
                type="button"
                size="sm"
                variant={period === value ? "default" : "outline"}
                onClick={() => onPeriodChange?.(value)}
                disabled={!onPeriodChange}
                className="min-h-9 shrink-0 rounded-full px-3 text-xs"
              >
                {value}
              </Button>
            ))}
          </div>
        </div>
      </header>

      <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-3 px-3 py-4 md:px-6">
        {preview && (
          <div role="status" className="rounded-xl border border-warning/30 bg-warning/10 px-3 py-2.5 text-xs">
            <p className="font-medium">{t(($) => $.page.preview_data)}</p>
            <p className="mt-1 text-muted-foreground">{t(($) => $.page.preview_detail)}</p>
          </div>
        )}

        <Tabs defaultValue="overview" className="gap-3">
          <TabsList className="grid h-10 w-full grid-cols-2">
            <TabsTrigger value="overview">{t(($) => $.page.tab_overview)}</TabsTrigger>
            <TabsTrigger value="servers">{t(($) => $.page.tab_servers)}</TabsTrigger>
          </TabsList>

          <TabsContent value="overview" className="space-y-4">
            <HealthCard snapshot={viewSnapshot} />
            <section aria-labelledby="bottlenecks-heading">
              <SectionHeading id="bottlenecks-heading" title={t(($) => $.overview.bottlenecks)} meta={t(($) => $.overview.period, { period })} />
              <div className="space-y-2">
                {viewSnapshot.bottlenecks.map((item) => {
                  const content = <BottleneckContent item={item} />;
                  const className = "flex min-h-16 w-full items-start gap-3 rounded-xl border border-warning/30 bg-background p-3 text-left shadow-xs";
                  if (!item.nodeId) return <article key={item.id} className={className}>{content}</article>;
                  return (
                    <button key={item.id} type="button" onClick={() => setSelected(viewSnapshot.nodes.find((node) => node.id === item.nodeId) ?? null)} className={className}>
                      {content}
                      <ChevronRight className="mt-2 size-4 shrink-0 text-muted-foreground" />
                    </button>
                  );
                })}
              </div>
            </section>

            <section aria-labelledby="overview-nodes-heading">
              <SectionHeading id="overview-nodes-heading" title={t(($) => $.overview.nodes)} meta={t(($) => $.overview.nodes_shown, { shown: 2, total: viewSnapshot.nodeCount })} />
              <div className="space-y-2">
                {rankedNodes.slice(0, 2).map((node) => <NodeCard key={node.id} node={node} onOpen={() => setSelected(node)} />)}
              </div>
            </section>
          </TabsContent>

          <TabsContent value="servers" className="space-y-4">
            <div className="-mx-3 flex gap-2 overflow-x-auto px-3 md:mx-0 md:px-0" aria-label={t(($) => $.servers.metric_filters)}>
              {(Object.keys(metricLabels) as LoadMetricKey[]).map((key) => (
                <Button key={key} type="button" size="sm" variant={metric === key ? "default" : "outline"} onClick={() => setMetric(key)} className="min-h-9 shrink-0 rounded-full px-3 text-xs">
                  {metricLabels[key]}
                </Button>
              ))}
            </div>
            <NodeHeatmap nodes={viewSnapshot.nodes} />
            <section aria-labelledby="ranked-nodes-heading">
              <SectionHeading id="ranked-nodes-heading" title={t(($) => $.servers.by_load)} meta={`${metricLabels[metric]} ↓`} />
              <div className="space-y-2">
                {rankedNodes.map((node) => <NodeCard key={node.id} node={node} onOpen={() => setSelected(node)} rank={rankedNodes.indexOf(node) + 1} />)}
              </div>
            </section>
          </TabsContent>
        </Tabs>
      </main>

      <NodeInspector node={selected} open={selected !== null} onOpenChange={(open) => !open && setSelected(null)} period={period} />
    </div>
  );
}

function HealthCard({ snapshot }: { snapshot: LoadSnapshot }) {
  const { t } = useT("load");
  return (
    <section className="rounded-2xl bg-foreground p-4 text-background shadow-lg" aria-label="Infrastructure health">
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="text-[10px] uppercase tracking-wider text-background/55">{t(($) => $.page.overall_health, { count: snapshot.nodeCount })}</p>
          <h2 className="mt-1 text-xl font-semibold">{snapshot.status}</h2>
          <p className="mt-1 text-xs text-background/60">{snapshot.statusDetail}</p>
        </div>
        <div className="grid size-14 shrink-0 place-items-center rounded-full border-[5px] border-success border-l-background/20 text-sm font-semibold">{snapshot.score}</div>
      </div>
      <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        {snapshot.kpis.map((kpi) => (
          <div key={kpi.label} className="rounded-lg border border-background/10 bg-background/5 p-2.5">
            <p className="text-[9px] text-background/50">{kpi.label}</p>
            <p className="mt-1 text-sm font-semibold">{kpi.value}</p>
            <p className={cn("mt-1 text-[9px]", kpi.tone === "critical" ? "text-destructive" : kpi.tone === "warning" ? "text-warning" : "text-success")}>{kpi.trend}</p>
          </div>
        ))}
      </div>
    </section>
  );
}

function BottleneckContent({ item }: { item: LoadSnapshot["bottlenecks"][number] }) {
  return (
    <>
      <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-warning/10 text-warning"><AlertTriangle className="size-4" /></span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium">{item.title}</span>
        <span className="mt-1 block text-xs text-muted-foreground">{item.description}</span>
        <span className="mt-2 flex flex-wrap gap-1.5">{item.chips.map((chip) => <Badge key={chip} variant="outline" className="text-[10px] font-normal">{chip}</Badge>)}</span>
      </span>
    </>
  );
}

function SectionHeading({ id, title, meta }: { id: string; title: string; meta: string }) {
  return <div className="mb-2 flex items-center justify-between px-1"><h2 id={id} className="text-sm font-semibold">{title}</h2><span className="text-[10px] text-muted-foreground">{meta}</span></div>;
}

function toneClass(value: number) {
  if (value >= 85) return "bg-destructive/15 text-destructive";
  if (value >= 65) return "bg-warning/15 text-warning";
  return "bg-success/10 text-success";
}

function NodeHeatmap({ nodes }: { nodes: LoadNode[] }) {
  const { t } = useT("load");
  const metricLabels = useMetricLabels();
  return (
    <div className="overflow-hidden rounded-xl border bg-background">
      <table aria-label={t(($) => $.servers.map_label)} className="w-full table-fixed text-[10px]">
        <thead className="bg-muted/60 text-muted-foreground"><tr><th className="w-[34%] px-3 py-2 text-left font-medium">{t(($) => $.page.node)}</th><th className="font-medium">{metricLabels.cpu}</th><th className="font-medium">{metricLabels.memory}</th><th className="font-medium">{t(($) => $.page.io)}</th></tr></thead>
        <tbody>{nodes.map((node) => <tr key={node.id} className="border-t"><th className="truncate px-3 py-2.5 text-left font-medium">{node.name}</th>{(["cpu", "memory", "disk"] as LoadMetricKey[]).map((key) => <td key={key} className="px-1 py-1.5 text-center"><span className={cn("block rounded-md px-1 py-1.5 font-medium", toneClass(node.metrics[key]))}>{node.metrics[key]}%</span></td>)}</tr>)}</tbody>
      </table>
    </div>
  );
}

function NodeCard({ node, onOpen, rank }: { node: LoadNode; onOpen: () => void; rank?: number }) {
  const { t } = useT("load");
  const metricLabels = useMetricLabels();
  return (
    <article className="overflow-hidden rounded-xl border bg-background shadow-xs">
      <button type="button" aria-label={`Open ${node.name}`} onClick={onOpen} className="flex min-h-16 w-full items-start gap-3 p-3 text-left">
        {rank && <span className="pt-0.5 font-mono text-xs text-muted-foreground">{String(rank).padStart(2, "0")}</span>}
        <span className={cn("mt-1.5 size-2 rounded-full", node.tone === "critical" ? "bg-destructive" : node.tone === "warning" ? "bg-warning" : "bg-success")} />
        <span className="min-w-0 flex-1"><span className="block text-sm font-medium">{node.name}</span><span className="mt-1 block text-[10px] text-muted-foreground">{node.role} · {node.capacity} · {node.region}</span></span>
        <span className="text-right"><span className="block text-xs font-medium">{node.summary}</span><span className="mt-1 block text-[9px] text-muted-foreground">{t(($) => $.page.uptime, { value: node.uptime })}</span></span>
      </button>
      <div className="grid grid-cols-4 gap-1 border-t px-3 py-2">
        {(Object.keys(metricLabels) as LoadMetricKey[]).map((key) => <div key={key} className="rounded-md bg-muted/50 px-1.5 py-1 text-center"><span className="block text-[8px] text-muted-foreground">{metricLabels[key]}</span><span className="mt-0.5 block text-[10px] font-medium">{node.metrics[key]}%</span></div>)}
      </div>
      <MiniChart values={node.series} />
    </article>
  );
}

function MiniChart({ values }: { values: number[] }) {
  const points = values.map((value, index) => `${(index / (values.length - 1)) * 100},${100 - value}`).join(" ");
  return <div className="h-9 border-t px-3 py-1"><svg viewBox="0 0 100 100" preserveAspectRatio="none" className="size-full" aria-hidden="true"><polyline points={points} fill="none" stroke="currentColor" strokeWidth="3" vectorEffect="non-scaling-stroke" className="text-primary" /></svg></div>;
}

function NodeInspector({ node, open, onOpenChange, period }: { node: LoadNode | null; open: boolean; onOpenChange: (open: boolean) => void; period: string }) {
  const { t } = useT("load");
  if (!node) return <Sheet open={false} onOpenChange={onOpenChange}><SheetContent /></Sheet>;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" aria-label={node.name} className="w-full gap-0 bg-muted/20 p-0 sm:max-w-lg">
        <SheetHeader className="border-b bg-background px-4 pb-4 pt-4 pr-14"><SheetTitle>{node.name}</SheetTitle><SheetDescription className="flex items-center gap-1.5 text-xs text-success"><span className="size-1.5 rounded-full bg-success" />{t(($) => $.page.online, { role: node.role, region: node.region })}</SheetDescription></SheetHeader>
        <div className="flex-1 space-y-3 overflow-y-auto p-3">
          <section className="rounded-xl border bg-background p-3"><h3 className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">{t(($) => $.page.chart_title, { period })}</h3><div className="mt-3 h-40"><MiniChart values={node.series} /></div></section>
          <InspectorBlock title={t(($) => $.inspector.resources)}><DataRow icon={Cpu} label={t(($) => $.inspector.cpu_usage)} value={`${node.metrics.cpu}%`} /><DataRow icon={Gauge} label={t(($) => $.inspector.cpu_throttling)} value={node.cpuThrottling} /><DataRow icon={Activity} label={t(($) => $.inspector.load_average)} value={node.loadAverage} /><DataRow icon={MemoryStick} label={t(($) => $.inspector.memory)} value={`${node.metrics.memory}%`} /><DataRow icon={Database} label={t(($) => $.inspector.disk_io)} value={`${node.metrics.disk}%`} /><DataRow icon={Network} label={t(($) => $.inspector.network)} value={node.networkLabel} /></InspectorBlock>
          <InspectorBlock title={t(($) => $.inspector.impact)}><DataRow icon={Clock3} label={t(($) => $.inspector.api_p95)} value={node.apiP95} /><DataRow icon={Server} label={t(($) => $.inspector.errors_5xx)} value={node.errorRate} /></InspectorBlock>
        </div>
      </SheetContent>
    </Sheet>
  );
}

function InspectorBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return <section className="rounded-xl border bg-background p-3"><h3 className="mb-1 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">{title}</h3>{children}</section>;
}

function DataRow({ icon: Icon, label, value }: { icon: typeof Cpu; label: string; value: string }) {
  return <div className="flex items-center justify-between gap-3 border-t py-2.5 first:border-0"><span className="flex items-center gap-2 text-xs text-muted-foreground"><Icon className="size-3.5" />{label}</span><strong className="text-xs">{value}</strong></div>;
}
