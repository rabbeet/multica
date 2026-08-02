export type AlertSeverity = "warning" | "critical";
export type AlertState = "healthy" | "firing" | "paused";

export interface AlertRule {
  id: string;
  name: string;
  description: string;
  severity: AlertSeverity;
  state: AlertState;
  enabled: boolean;
  source: string;
  metric: string;
  dimension: string;
  expression: string;
  conditionTokens: string[];
  baseline: {
    method: string;
    history: string;
    comparison: string;
    minimum?: string;
  } | null;
  schedule: {
    cadence: string;
    dataWindow: string;
    pending: string;
    timezone: string;
    cron: string;
  };
  handling: {
    noData: string;
    queryError: string;
  };
  expectedFrequency: string;
  delivery: {
    platform: "Mattermost";
    channel: string;
    channelId: string;
    mention: string;
    repeat: string;
    grouping: string;
    transport: string;
    recoveryInThread: boolean;
    chart: string;
    nativeGrafanaSuppressed: boolean;
  };
  lastEvaluation: string;
}

export const previewAlertFixtures: AlertRule[] = [
  {
    id: "ALT-0081",
    name: "Пустые результаты у поставщика",
    description: "Supplier · относительно базовой линии",
    severity: "warning",
    state: "healthy",
    enabled: true,
    source: "Prometheus",
    metric: "Доля пустых результатов",
    dimension: "Поставщик",
    expression: "empty_results_30m / all_queries_30m",
    conditionTokens: ["> 90% empty", "yield < 25% baseline", "queries ≥ 300"],
    baseline: { method: "Median", history: "3 weeks", comparison: "Same time of week", minimum: "5% yield" },
    schedule: { cadence: "Every 5 minutes", dataWindow: "30 minutes", pending: "30 minutes", timezone: "Europe/Moscow", cron: "*/5 * * * *" },
    handling: { noData: "OK", queryError: "Error" },
    expectedFrequency: "2 incidents in 21 days · about 2.9/month",
    delivery: { platform: "Mattermost", channel: "Что-то пошло так", channelId: "98zombrwq7dp8g3ndwmqii6aqo", mention: "No @channel", repeat: "Do not repeat", grouping: "Supplier", transport: "Hermes bridge", recoveryInThread: true, chart: "Operations Dark chart · 1600×900", nativeGrafanaSuppressed: true },
    lastEvaluation: "Successful · 20 seconds ago",
  },
  {
    id: "ALT-0082", name: "Обработка поисков остановилась", description: "Global · без базовой линии", severity: "critical", state: "healthy", enabled: true, source: "Prometheus", metric: "Global supplier query observations", dimension: "Global", expression: "increase(supplier_query_results_count_count[15m]) < 1", conditionTokens: ["No observations for 15 minutes"], baseline: null, schedule: { cadence: "Every 5 minutes", dataWindow: "15 minutes", pending: "10 minutes", timezone: "Europe/Moscow", cron: "*/5 * * * *" }, handling: { noData: "OK", queryError: "Error" }, expectedFrequency: "No representative replay available", delivery: { platform: "Mattermost", channel: "Что-то пошло так", channelId: "98zombrwq7dp8g3ndwmqii6aqo", mention: "@channel", repeat: "Do not repeat", grouping: "Global", transport: "Hermes bridge", recoveryInThread: true, chart: "Operations Dark chart · 1600×900", nativeGrafanaSuppressed: true }, lastEvaluation: "Successful · 20 seconds ago",
  },
  {
    id: "ALT-0083", name: "Падение трафика поставщика", description: "Supplier · динамический список", severity: "warning", state: "healthy", enabled: true, source: "Prometheus", metric: "Completed-call traffic", dimension: "Поставщик", expression: "traffic_current < traffic_baseline * 0.30", conditionTokens: ["Traffic < 30% baseline", "baseline > 1 req/s"], baseline: { method: "Mean", history: "3 weeks", comparison: "Same time of week" }, schedule: { cadence: "Every 5 minutes", dataWindow: "60 minutes", pending: "60 minutes", timezone: "Europe/Moscow", cron: "*/5 * * * *" }, handling: { noData: "OK", queryError: "Error" }, expectedFrequency: "Historical replay recorded in rule version", delivery: { platform: "Mattermost", channel: "Что-то пошло так", channelId: "98zombrwq7dp8g3ndwmqii6aqo", mention: "No @channel", repeat: "Do not repeat", grouping: "Supplier", transport: "Hermes bridge", recoveryInThread: true, chart: "Operations Dark chart · 1600×900", nativeGrafanaSuppressed: true }, lastEvaluation: "Successful · 20 seconds ago",
  },
  ...([
    ["ALT-0084", "Рост HTTP-ошибок поставщика", "Supplier · относительно базовой линии", "Every 5 minutes", "30 minutes"],
    ["ALT-0085", "Деградация 3-D Secure", "Платежи · по терминалам", "Hourly at :05", "1 hour"],
    ["ALT-0086", "Деградация подтверждений СБП", "Платежи · по терминалам", "Hourly at :10", "1 hour"],
    ["ALT-0087", "UNAVAIL по поставщикам", "Заказы · поставщики и авиакомпании", "Hourly at :10", "1 hour"],
    ["ALT-0088", "Нет заказов", "Заказы · адаптивный порог", "Every minute", "Adaptive"],
  ] satisfies Array<[string, string, string, string, string]>).map(([id, name, description, cadence, dataWindow]): AlertRule => ({
    id, name, description, severity: "warning", state: "healthy", enabled: true,
    source: name.includes("заказ") || name.includes("СБП") || name.includes("Secure") || name.includes("UNAVAIL") ? "ClickHouse" : "Prometheus",
    metric: description, dimension: "Business entity", expression: "Managed deterministic query", conditionTokens: ["Threshold from current rule version"], baseline: { method: "Rule-specific", history: "Validated history", comparison: "Canonical business window" },
    schedule: { cadence, dataWindow, pending: "Rule-specific", timezone: "Europe/Moscow", cron: "Managed schedule" }, handling: { noData: "Keep state", queryError: "Error" }, expectedFrequency: "Historical replay recorded in rule version",
    delivery: { platform: "Mattermost", channel: "Что-то пошло так", channelId: "98zombrwq7dp8g3ndwmqii6aqo", mention: "No @channel", repeat: "Do not repeat", grouping: "Incident key", transport: "Hermes bridge", recoveryInThread: true, chart: "Operations Dark chart · 1600×900", nativeGrafanaSuppressed: true }, lastEvaluation: "Successful · less than one hour ago",
  })),
];
