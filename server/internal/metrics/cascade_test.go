package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/multica-ai/multica/server/internal/cascade"
)

// TestCascadeCollector_ExposesLegacyEventDropped is the round-trip
// happy path: bump the cascade.Metrics, register the collector into
// a fresh prometheus.Registry, scrape via testutil, assert the
// expected wire-format series + values are present.
func TestCascadeCollector_ExposesLegacyEventDropped(t *testing.T) {
	m := cascade.NewMetrics()
	m.IncLegacyEventDropped("ci_failure")
	m.IncLegacyEventDropped("ci_failure")
	m.IncLegacyEventDropped("pr_review_change")

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCascadeCollector(m))

	want := `
# HELP multica_cascade_worker_legacy_event_dropped_total Cascade rows short-circuited by the PUL-212 CQ2 guard for dropped event_types (ci_failure, pr_review_change). Steady-state expected trajectory: monotonic decline to zero. When rate() is flat at zero for ~7 days, PUL-217 (drop the guard + cascade_retrigger CHECK arm) is safe. Always query via rate(); server restart resets the in-process counter (Prometheus handles counter resets for rate but not for increase).
# TYPE multica_cascade_worker_legacy_event_dropped_total counter
multica_cascade_worker_legacy_event_dropped_total{event_type="ci_failure"} 2
multica_cascade_worker_legacy_event_dropped_total{event_type="pr_review_change"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"multica_cascade_worker_legacy_event_dropped_total"); err != nil {
		t.Errorf("scrape mismatch: %v", err)
	}
}

// TestCascadeCollector_NilMetrics_NoSamples: a collector with a nil
// metrics pointer must register cleanly (Describe still works) AND
// emit zero samples on Collect. Defends against a partial-startup
// scenario where the registry is wired before cascade.NewMetrics()
// runs.
func TestCascadeCollector_NilMetrics_NoSamples(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCascadeCollector(nil))

	n, err := testutil.GatherAndCount(reg, "multica_cascade_worker_legacy_event_dropped_total")
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if n != 0 {
		t.Errorf("nil-metrics collector emitted %d samples, want 0", n)
	}
}

// TestCascadeCollector_FreshMetrics_NoSamples confirms a freshly
// constructed cascade.Metrics with no Inc calls produces zero
// samples. The /metrics endpoint must NOT publish
// `…_total{event_type="ci_failure"} 0` on a process that has never
// dropped a legacy row — that would erase the "process restarted
// cleanly, nothing has happened" distinction operators rely on.
func TestCascadeCollector_FreshMetrics_NoSamples(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewCascadeCollector(cascade.NewMetrics()))

	n, err := testutil.GatherAndCount(reg, "multica_cascade_worker_legacy_event_dropped_total")
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if n != 0 {
		t.Errorf("fresh-metrics collector emitted %d samples, want 0", n)
	}
}
