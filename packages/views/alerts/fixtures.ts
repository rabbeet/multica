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

type AlertSpec = Pick<AlertRule, "id" | "name" | "description" | "source" | "metric" | "dimension" | "expression" | "conditionTokens"> & {
  cadence: string;
  window: string;
  pending?: string;
  cron: string;
  severity?: AlertSeverity;
  baseline?: AlertRule["baseline"];
};

const delivery: AlertRule["delivery"] = {
  platform: "Mattermost",
  channel: "Что-то пошло так",
  channelId: "98zombrwq7dp8g3ndwmqii6aqo",
  mention: "No @channel",
  repeat: "Do not repeat",
  grouping: "Incident key",
  transport: "Deterministic Hermes monitor",
  recoveryInThread: true,
  chart: "Operations Dark chart · 1600×900",
  nativeGrafanaSuppressed: true,
};

const defaultBaseline: NonNullable<AlertRule["baseline"]> = {
  method: "Median + MAD",
  history: "Validated historical slots",
  comparison: "Same time of day / week",
};

function alertRule(spec: AlertSpec): AlertRule {
  return {
    id: spec.id,
    name: spec.name,
    description: spec.description,
    severity: spec.severity ?? "warning",
    state: "healthy",
    enabled: true,
    source: spec.source,
    metric: spec.metric,
    dimension: spec.dimension,
    expression: spec.expression,
    conditionTokens: spec.conditionTokens,
    baseline: spec.baseline === undefined ? defaultBaseline : spec.baseline,
    schedule: {
      cadence: spec.cadence,
      dataWindow: spec.window,
      pending: spec.pending ?? "2 consecutive checks",
      timezone: "Europe/Moscow",
      cron: spec.cron,
    },
    handling: { noData: "Keep state", queryError: "Error" },
    expectedFrequency: "Historical replay recorded in the rule version",
    delivery,
    lastEvaluation: "Preview · live run status is not connected",
  };
}

/**
 * Reconciled preview inventory as of 2026-08-02.
 *
 * This is intentionally read-only. The future control-plane API must expose the
 * same AlertRule contract so newly registered rules appear without editing this
 * fixture.
 */
export const previewAlertFixtures: AlertRule[] = [
  alertRule({ id: "ALT-0081", name: "Пустые результаты у поставщика", description: "Supplier · относительно базовой линии", source: "Grafana / Prometheus", metric: "Доля пустых результатов", dimension: "Поставщик", expression: "empty_results_30m / all_queries_30m", conditionTokens: ["> 90% empty", "yield < 25% baseline", "queries ≥ 300"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0082", name: "Обработка поисков остановилась", description: "Global supplier pipeline", source: "Grafana / Prometheus", metric: "Supplier query observations", dimension: "Global", expression: "increase(supplier_query_results_count_count[15m]) < 1", conditionTokens: ["No observations"], cadence: "Every 5 minutes", window: "15 minutes", pending: "10 minutes", cron: "*/5 * * * *", severity: "critical", baseline: null }),
  alertRule({ id: "ALT-0083", name: "Падение трафика поставщика", description: "Supplier · динамический список", source: "Grafana / Prometheus", metric: "Completed-call traffic", dimension: "Поставщик", expression: "traffic_current < traffic_baseline * 0.30", conditionTokens: ["Traffic < 30% baseline"], cadence: "Every 5 minutes", window: "60 minutes", pending: "60 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0084", name: "Рост HTTP-ошибок поставщика", description: "Supplier HTTP outcomes", source: "Grafana / Prometheus", metric: "Non-2xx/3xx share", dimension: "Поставщик", expression: "http_errors / all_http_requests", conditionTokens: ["Absolute + relative + volume gates"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0085", name: "Рост задержки поставщика", description: "Supplier mean latency", source: "Grafana / Prometheus", metric: "Mean API-call latency", dimension: "Поставщик", expression: "duration_sum / duration_count", conditionTokens: ["Above same-time baseline"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0086", name: "Падение success rate продукта", description: "Product processing outcomes", source: "Grafana / Prometheus", metric: "Product-stage success ratio", dimension: "Продукт", expression: "success / all outcomes", conditionTokens: ["Success ratio < 70% baseline"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0087", name: "Падение output продукта", description: "Product output volume", source: "Grafana / Prometheus", metric: "products_count_total{mark=out}", dimension: "Продукт", expression: "product_output_current < baseline band", conditionTokens: ["Negative deviation only"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0088", name: "Hub-oneways: пустые результаты", description: "Hub-oneways supplier search", source: "Grafana / Prometheus", metric: "Empty-results share", dimension: "Поставщик", expression: "hub_empty / hub_queries", conditionTokens: ["Above historical band"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0089", name: "Hub-oneways: падение scheduled", description: "Hub-oneways scheduled share", source: "Grafana / Prometheus", metric: "Scheduled route share", dimension: "Поставщик", expression: "scheduled / all_schedule_results", conditionTokens: ["Below historical band"], cadence: "Every 5 minutes", window: "30 minutes", pending: "60 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0090", name: "Hub-oneways: плохие маршруты", description: "Hub-oneways bad-route outcomes", source: "Grafana / Prometheus", metric: "bad_route share", dimension: "Поставщик", expression: "bad_routes / all_schedule_results", conditionTokens: ["Above historical band"], cadence: "Every 5 minutes", window: "30 minutes", pending: "30 minutes", cron: "*/5 * * * *" }),
  alertRule({ id: "ALT-0091", name: "Обработка product pipeline остановилась", description: "Global product pipeline", source: "Grafana / Prometheus", metric: "Product-stage observations", dimension: "Global", expression: "increase(product_stage_result_total[15m]) < 1", conditionTokens: ["No observations"], cadence: "Every 5 minutes", window: "15 minutes", pending: "10 minutes", cron: "*/5 * * * *", baseline: null }),
  alertRule({ id: "ALT-0092", name: "Деградация 3-D Secure", description: "Платежи · по терминалам", source: "PostgreSQL", metric: "3-D Secure success rate", dimension: "Платёжный терминал", expression: "checkout events · same-time weekly baseline", conditionTokens: ["Drop + z-score + volume gates"], cadence: "Hourly at :05", window: "6 hours", cron: "5 * * * *" }),
  alertRule({ id: "ALT-0093", name: "Деградация подтверждений СБП", description: "IPS synced vs failed", source: "PostgreSQL", metric: "SBP confirmation success rate", dimension: "Global", expression: "payment_ips_synced / (synced + failed)", conditionTokens: ["Drop ≥ 15 p.p.", "failures ≥ 10", "z ≤ -3"], cadence: "Hourly at :10", window: "3 hours", cron: "10 * * * *" }),
  alertRule({ id: "ALT-0094", name: "UNAVAIL по заказам", description: "Offer/order stages · supplier and airline", source: "PostgreSQL", metric: "UNAVAIL share", dimension: "Overall / supplier / airline", expression: "offer_unavailable + order_invalid UNAVAIL", conditionTokens: ["Impact + baseline gates"], cadence: "Hourly at :10", window: "6 hours", cron: "10 * * * *" }),
  alertRule({ id: "ALT-0095", name: "Нет заказов", description: "Заказы · адаптивный порог по времени суток", source: "MySQL / MariaDB", metric: "Age of latest orders.created_at", dimension: "Global", expression: "UTC_TIMESTAMP() - MAX(created_at)", conditionTokens: ["Age above active profile threshold"], cadence: "Every minute", window: "Adaptive window", cron: "* * * * *" }),
  alertRule({ id: "ALT-0096", name: "Нет кликов", description: "Клики · адаптивный порог по времени суток", source: "MySQL / MariaDB", metric: "Age of latest click", dimension: "Global", expression: "UTC_TIMESTAMP() - latest click timestamp", conditionTokens: ["Age above active profile threshold"], cadence: "Every minute", window: "Adaptive window", cron: "* * * * *" }),
  alertRule({ id: "ALT-0097", name: "Всплеск PRICE_CHANGED", description: "Order invalid status", source: "PostgreSQL", metric: "PRICE_CHANGED rate", dimension: "Overall / supplier / airline", expression: "PRICE_CHANGED / checkout order events", conditionTokens: ["Rate + z-score + excess gates"], cadence: "Hourly at :20", window: "6 hours", cron: "20 * * * *" }),
  alertRule({ id: "ALT-0098", name: "Всплеск INVALID", description: "Order invalid status", source: "PostgreSQL", metric: "INVALID rate", dimension: "Overall / supplier / airline", expression: "INVALID / checkout order events", conditionTokens: ["Rate + z-score + excess gates"], cadence: "Hourly at :20", window: "6 hours", cron: "20 * * * *" }),
  alertRule({ id: "ALT-0099", name: "Всплеск ERROR", description: "Order invalid status", source: "PostgreSQL", metric: "ERROR rate", dimension: "Overall / supplier / airline", expression: "ERROR / checkout order events", conditionTokens: ["Rate + z-score + excess gates"], cadence: "Hourly at :20", window: "6 hours", cron: "20 * * * *" }),
  alertRule({ id: "ALT-0100", name: "Отсутствует op_status", description: "Order data-quality status", source: "PostgreSQL", metric: "Missing op_status rate", dimension: "Overall / supplier / airline", expression: "missing op_status / checkout order events", conditionTokens: ["Rate + z-score + excess gates"], cadence: "Hourly at :20", window: "6 hours", cron: "20 * * * *" }),
  alertRule({ id: "ALT-0101", name: "СБП: рост отмен", description: "Additional SBP signal", source: "PostgreSQL", metric: "Cancellation share", dimension: "Global", expression: "cancelled / (outcomes + cancelled)", conditionTokens: ["Above same-time baseline"], cadence: "Hourly at :15", window: "3 hours", cron: "15 * * * *" }),
  alertRule({ id: "ALT-0102", name: "СБП: деградация создания", description: "Additional SBP signal", source: "PostgreSQL", metric: "Creation rate", dimension: "Global", expression: "created / creating", conditionTokens: ["Below same-time baseline"], cadence: "Hourly at :15", window: "3 hours", cron: "15 * * * *" }),
  alertRule({ id: "ALT-0103", name: "Аномалии фильтрации продукта", description: "Filtered outcomes and output collapse", source: "ClickHouse", metric: "Product filtering outcome share", dimension: "Продукт / mark", expression: "Pulse.product_filtering_baseline", conditionTokens: ["Filter surge", "Output collapse", "volume ≥ 10,000"], cadence: "Every 5 minutes", window: "30 minutes", cron: "*/5 * * * *" }),
];
