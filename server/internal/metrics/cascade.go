package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/multica-ai/multica/server/internal/cascade"
)

// CascadeCollector exposes cascade.Metrics counters as Prometheus
// series. Same shape as GithubPollCollector / RealtimeCollector /
// DBCollector — Describe declares the wire-format constants, Collect
// snapshots the underlying atomics and emits one sample per label
// set. Empty / nil metrics produce no samples (Prometheus convention:
// don't emit zero-value series for label sets we've never observed).
//
// Metric inventory:
//   - multica_cascade_worker_legacy_event_dropped_total{event_type=…}
//     counter. PUL-220 — fires every time the worker.processOne CQ2
//     guard short-circuits a legacy ci_failure / pr_review_change
//     row. Steady-state expected trajectory: monotonic decline to
//     zero. When the rate is flat at zero for ~7 days, PUL-217 (drop
//     the CQ2 guard + the cascade_retrigger CHECK arm) is safe.
//     Always query via rate() not increase(): server restarts wipe
//     the in-process counter; Prometheus handles counter resets
//     transparently for rate but not for increase over the reset
//     window.
type CascadeCollector struct {
	metrics *cascade.Metrics

	legacyEventDroppedTotal *prometheus.Desc
}

// NewCascadeCollector wraps a cascade.Metrics. The pointer is
// retained — Collect calls Snapshot on every scrape so increments on
// the worker side are visible on the next /metrics request.
func NewCascadeCollector(m *cascade.Metrics) *CascadeCollector {
	return &CascadeCollector{
		metrics: m,
		legacyEventDroppedTotal: prometheus.NewDesc(
			"multica_cascade_worker_legacy_event_dropped_total",
			"Cascade rows short-circuited by the PUL-212 CQ2 guard for dropped event_types (ci_failure, pr_review_change). Steady-state expected trajectory: monotonic decline to zero. When rate() is flat at zero for ~7 days, PUL-217 (drop the guard + cascade_retrigger CHECK arm) is safe. Always query via rate(); server restart resets the in-process counter (Prometheus handles counter resets for rate but not for increase).",
			[]string{"event_type"},
			nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *CascadeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.legacyEventDroppedTotal
}

// Collect implements prometheus.Collector. Iterates the Snapshot's
// per-event-type map; an empty map (no Inc calls yet) yields no
// samples — which is the right /metrics shape because emitting
// `…_total{event_type="ci_failure"} 0` would still register a series
// with the scraper, hiding the "fresh process, nothing has fired
// yet" distinction.
func (c *CascadeCollector) Collect(ch chan<- prometheus.Metric) {
	if c.metrics == nil {
		return
	}
	snap := c.metrics.Snapshot()
	for eventType, v := range snap.LegacyEventDropped {
		ch <- prometheus.MustNewConstMetric(
			c.legacyEventDroppedTotal,
			prometheus.CounterValue,
			float64(v),
			eventType,
		)
	}
}
