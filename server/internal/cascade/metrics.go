package cascade

import (
	"sync"
	"sync/atomic"
)

// Metrics is the atomic-counter surface the cascade worker bumps as
// it processes rows. Mirrors the convention used by
// internal/githubpoll.Metrics and internal/daemonws.Metrics: domain
// package owns lock-free atomic counters; a Prometheus collector in
// internal/metrics snapshots them on scrape and emits the wire-format
// metrics on /metrics. Keeping prometheus/client_golang out of this
// package means cascade unit tests stay cheap (no registry wiring)
// and the dependency direction stays cascade → metrics, never the
// reverse.
//
// Today the only counter recorded here is legacyEventDropped — see
// IncLegacyEventDropped. Future cascade observability hooks should
// add fields onto this struct rather than introducing a parallel
// Metrics type, so the Prometheus collector has one surface to
// snapshot.
type Metrics struct {
	// legacyEventDropped keys the per-event-type CQ2-drop count.
	// sync.Map is used (rather than a fixed-size array indexed off
	// a switch) for symmetry with githubpoll.Metrics — the cost
	// difference at the CQ2 hot path's "a few per minute" rate is
	// invisible, and the consistent shape lets the Snapshot/collect
	// glue stay near-identical across all four metrics packages.
	legacyEventDropped sync.Map // map[eventType string]*atomic.Int64
}

// NewMetrics returns a fresh Metrics instance. Pointer receiver
// throughout — the Prometheus collector takes the same pointer at
// startup so increments on the worker side are visible on the next
// /metrics scrape.
func NewMetrics() *Metrics { return &Metrics{} }

// IncLegacyEventDropped bumps the per-event-type counter for the
// PUL-212 CQ2 guard in worker.processOne. eventType is whitelisted
// here even though the only current caller (worker.processOne) has
// already gated on the same two values — the duplicate check is
// cheap cardinality defence: a future caller (a metric-replay tool,
// a refactor that moves the Inc out of processOne, a test bench)
// can't accidentally explode the time-series count for the
// multica_cascade_worker_legacy_event_dropped_total{event_type=…}
// vector. Unknown event types are silently dropped — the counter is
// observability for the planned drain, not a strict input validator.
//
// Receiver is nil-safe: callers wired before this metric existed
// (tests, partial startups where the metrics surface wasn't
// constructed) keep working with no `if w.metrics != nil` clutter
// at the call site.
func (m *Metrics) IncLegacyEventDropped(eventType string) {
	if m == nil {
		return
	}
	switch eventType {
	case "ci_failure", "pr_review_change":
		// ok — fall through to bump.
	default:
		return
	}
	v, _ := m.legacyEventDropped.LoadOrStore(eventType, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

// Snapshot is a point-in-time view of every counter on m, used by
// the Prometheus collector at scrape time and by tests. Map keys are
// the same eventType strings IncLegacyEventDropped accepts.
//
// Concurrency: lock-free; per-key reads are atomic, but across keys
// the view may be torn (acceptable observability slop — counters
// monotonically increase, so the worst case is "you saw ci_failure=N
// and pr_review_change=M sampled from slightly different instants").
//
// Nil-safe receiver returns a zero-value Snapshot with a nil map.
// Ranging a nil Go map yields zero iterations, so the collector's
// per-key loop is safe without an explicit nil-check.
type Snapshot struct {
	LegacyEventDropped map[string]int64
}

func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	out := Snapshot{}
	m.legacyEventDropped.Range(func(k, v any) bool {
		if c, ok := v.(*atomic.Int64); ok {
			if out.LegacyEventDropped == nil {
				out.LegacyEventDropped = map[string]int64{}
			}
			out.LegacyEventDropped[k.(string)] = c.Load()
		}
		return true
	})
	return out
}
