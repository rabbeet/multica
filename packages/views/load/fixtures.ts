export type LoadTone = "healthy" | "warning" | "critical";
export type LoadMetricKey = "cpu" | "memory" | "disk" | "network";

export interface LoadNode {
  id: string;
  name: string;
  role: string;
  region: string;
  capacity: string;
  tone: LoadTone;
  summary: string;
  uptime: string;
  metrics: Record<LoadMetricKey, number>;
  loadAverage: string;
  cpuThrottling: string;
  apiP95: string;
  errorRate: string;
  networkLabel: string;
  series: number[];
}

export interface LoadSnapshot {
  updatedAgo: string;
  score: number;
  nodeCount: number;
  status: string;
  statusDetail: string;
  kpis: Array<{ label: string; value: string; trend: string; tone: LoadTone }>;
  bottlenecks: Array<{
    id: string;
    title: string;
    description: string;
    chips: string[];
    nodeId?: string;
  }>;
  nodes: LoadNode[];
}

export const previewLoadSnapshot: LoadSnapshot = {
  updatedAgo: "20 seconds ago",
  score: 92,
  nodeCount: 12,
  status: "System stable",
  statusDetail: "1 bottleneck needs attention",
  kpis: [
    { label: "CPU", value: "62%", trend: "normal", tone: "healthy" },
    { label: "RAM", value: "71%", trend: "+8%", tone: "warning" },
    { label: "API p95", value: "182 ms", trend: "−12 ms", tone: "healthy" },
    { label: "Queue", value: "1,240", trend: "+38%", tone: "warning" },
  ],
  bottlenecks: [
    { id: "cpu-api-03", title: "api-prod-03 is CPU constrained", description: "Throttling 12% · p95 increased to 410 ms", chips: ["CPU 91%", "18 minutes", "3 endpoints"], nodeId: "api-prod-03" },
    { id: "checkout-queue", title: "Checkout queue is growing", description: "Workers are processing slower than the incoming rate", chips: ["1,240 jobs", "+38% / hour"] },
  ],
  nodes: [
    { id: "api-prod-03", name: "api-prod-03", role: "API", region: "eu-1", capacity: "8 vCPU · 16 GB", tone: "warning", summary: "High load", uptime: "18d 4h", metrics: { cpu: 91, memory: 74, disk: 3, network: 78 }, loadAverage: "7.4 / 6.9 / 6.1", cpuThrottling: "12%", apiP95: "410 ms", errorRate: "0.7%", networkLabel: "284 ↓ / 91 ↑ Mbps", series: [34, 39, 37, 52, 48, 65, 61, 76, 72, 91, 79, 93, 87] },
    { id: "worker-prod-02", name: "worker-prod-02", role: "Worker", region: "eu-1", capacity: "8 vCPU · 16 GB", tone: "warning", summary: "Queue pressure", uptime: "11d 9h", metrics: { cpu: 73, memory: 52, disk: 4, network: 45 }, loadAverage: "5.1 / 4.8 / 4.2", cpuThrottling: "2%", apiP95: "—", errorRate: "0.1%", networkLabel: "146 ↓ / 123 ↑ Mbps", series: [43, 45, 48, 51, 49, 56, 59, 61, 66, 70, 68, 73, 72] },
    { id: "api-prod-02", name: "api-prod-02", role: "API", region: "eu-1", capacity: "8 vCPU · 16 GB", tone: "healthy", summary: "Normal", uptime: "21d 2h", metrics: { cpu: 68, memory: 61, disk: 2, network: 51 }, loadAverage: "4.7 / 4.4 / 4.0", cpuThrottling: "0%", apiP95: "176 ms", errorRate: "0.2%", networkLabel: "198 ↓ / 72 ↑ Mbps", series: [51, 48, 55, 58, 52, 61, 64, 60, 66, 63, 69, 65, 68] },
    { id: "api-prod-01", name: "api-prod-01", role: "API", region: "eu-1", capacity: "8 vCPU · 16 GB", tone: "healthy", summary: "Normal", uptime: "23d 6h", metrics: { cpu: 54, memory: 58, disk: 2, network: 48 }, loadAverage: "3.6 / 3.4 / 3.1", cpuThrottling: "0%", apiP95: "169 ms", errorRate: "0.2%", networkLabel: "185 ↓ / 65 ↑ Mbps", series: [45, 48, 44, 52, 50, 54, 51, 56, 53, 55, 52, 57, 54] },
    { id: "mysql-primary-01", name: "mysql-primary-01", role: "MySQL", region: "eu-1", capacity: "16 vCPU · 64 GB", tone: "healthy", summary: "Normal", uptime: "42d 7h", metrics: { cpu: 48, memory: 68, disk: 12, network: 38 }, loadAverage: "5.0 / 4.8 / 4.6", cpuThrottling: "0%", apiP95: "—", errorRate: "0%", networkLabel: "112 ↓ / 205 ↑ Mbps", series: [42, 45, 41, 47, 44, 49, 46, 51, 47, 50, 46, 49, 48] },
  ],
};
