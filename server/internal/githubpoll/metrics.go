package githubpoll

import (
	"strconv"
	"sync"
	"sync/atomic"
)

// Metrics is the atomic-counter surface the poller updates on every
// tick. Mirrors the convention used by internal/realtime.Metrics and
// internal/daemonws.Metrics — stdlib atomics for cheap hot-path
// writes; a Prometheus collector (in internal/metrics) reads the
// counters and emits the wire-format metrics on /metrics scrape.
//
// Per-repo and per-event-type counters live in sync.Maps keyed by
// label values. Lookups happen once per tick (4-10 repos in any
// realistic config) so the map overhead is negligible compared to
// the HTTP round-trip the metric measures.
type Metrics struct {
	// PanicsTotal counts supervisor-recovered panics. Should stay
	// at 0 in steady state; alert at rate > 0/h.
	PanicsTotal atomic.Int64

	// CallsTotal keyed by "{repo}|{status_code}". Captures HTTP
	// response status from /events. 200 = batch fetched; 304 = no
	// change (steady state); 4xx/5xx surface latent issues.
	callsTotal sync.Map // map[string]*atomic.Int64

	// EventsTotal keyed by "{repo}|{event_type}" where event_type
	// is "ci_failure", "pr_merged", "pr_review_change", "skip", or
	// "schema_mismatch". The "skip" bucket bundles ErrSkip (success
	// conclusion, non-merge close, approval review, push, etc.) so
	// the alert path has one distinct bucket per real event-type.
	eventsTotal sync.Map // map[string]*atomic.Int64

	// SinkErrorsTotal keyed by repo. Each entry is a count of Sink
	// submit failures (any non-nil error from Sink.Submit). Alerts
	// fire on rate > 0/min: a sink outage means the cursor stops
	// advancing and the next tick re-runs the same events.
	sinkErrorsTotal sync.Map // map[string]*atomic.Int64

	// RateLimitRemaining stores the most recently observed
	// X-RateLimit-Remaining header value per repo. Gauge semantics.
	// Alert at < 500 (10% of the 5000/h PAT budget) — the poll
	// budget is ≤ 600/h in steady state so a sustained dip means
	// some other code path is burning the budget.
	rateLimitRemaining sync.Map // map[string]*atomic.Int64

	// lastTickUnix stores the wall-clock Unix seconds of the most
	// recent successful tickOne per repo. The Prometheus collector
	// derives cursor_age_seconds = now() - lastTickUnix at scrape
	// time so a stuck goroutine surfaces as a growing gauge (the
	// silent-death case). Storing a snapshot-time computed delta
	// would freeze on death and never alert.
	lastTickUnix sync.Map // map[string]*atomic.Int64
}

// NewMetrics returns a fresh metrics instance. Pointer receiver
// throughout — Prometheus collector takes the same pointer at
// startup so changes to the counters are visible on next scrape.
func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) IncPanic() {
	if m == nil {
		return
	}
	m.PanicsTotal.Add(1)
}

// IncCall increments the per-(repo, status) call counter. statusCode
// is the HTTP status from the /events response — 200, 304, 403,
// 5xx, or 0 if the request failed before a response (DNS, timeout).
func (m *Metrics) IncCall(repo string, statusCode int) {
	if m == nil {
		return
	}
	if statusCode < 0 {
		statusCode = 0
	}
	incMapCounter(&m.callsTotal, repo+"|"+strconv.Itoa(statusCode))
}

// IncEvent increments the per-(repo, event_type) event counter.
// eventType is one of the webhook constants (ci_failure, pr_merged,
// pr_review_change) for classified events, or "skip" /
// "schema_mismatch" for the negative paths.
func (m *Metrics) IncEvent(repo, eventType string) {
	if m == nil {
		return
	}
	incMapCounter(&m.eventsTotal, repo+"|"+eventType)
}

// IncSinkError counts Sink.Submit failures for the given repo.
func (m *Metrics) IncSinkError(repo string) {
	if m == nil {
		return
	}
	incMapCounter(&m.sinkErrorsTotal, repo)
}

// SetRateLimitRemaining records the latest X-RateLimit-Remaining
// observed for the repo. Gauge — overwrites the previous value.
func (m *Metrics) SetRateLimitRemaining(repo string, remaining int) {
	if m == nil {
		return
	}
	v, _ := m.rateLimitRemaining.LoadOrStore(repo, new(atomic.Int64))
	v.(*atomic.Int64).Store(int64(remaining))
}

// MarkTickComplete records the wall-clock unix seconds of a
// completed (or steady-state 304) tickOne for the repo. The
// Prometheus collector reads this and emits
// cursor_age_seconds = now() - lastTickUnix at scrape time, so a
// stuck or panicking goroutine surfaces as a growing gauge that
// trips the staleness alert. Persisting a precomputed delta would
// freeze on death and never fire.
func (m *Metrics) MarkTickComplete(repo string, t int64) {
	if m == nil {
		return
	}
	v, _ := m.lastTickUnix.LoadOrStore(repo, new(atomic.Int64))
	v.(*atomic.Int64).Store(t)
}

// Snapshot returns a flat map view of all counters / gauges, used by
// the Prometheus collector and by integration tests. Map keys are
// the same composite strings the Inc* methods use as their map keys
// internally — the Prometheus side splits them back into label pairs.
//
// LastTickUnix holds raw unix seconds per repo; cursor_age_seconds
// is derived at scrape time as `now() - LastTickUnix[repo]` so a
// dead poller produces a growing gauge.
type Snapshot struct {
	Panics             int64
	Calls              map[string]int64 // key: "repo|status_code"
	Events             map[string]int64 // key: "repo|event_type"
	SinkErrors         map[string]int64 // key: "repo"
	RateLimitRemaining map[string]int64 // key: "repo"
	LastTickUnix       map[string]int64 // key: "repo"
}

// Snapshot is concurrency-safe and lock-free; readers see a
// consistent point-in-time view per atomic, though across atomics
// the view may have torn writes (acceptable for observability).
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	out := Snapshot{
		Panics:             m.PanicsTotal.Load(),
		Calls:              flattenMap(&m.callsTotal),
		Events:             flattenMap(&m.eventsTotal),
		SinkErrors:         flattenMap(&m.sinkErrorsTotal),
		RateLimitRemaining: flattenMap(&m.rateLimitRemaining),
		LastTickUnix:       flattenMap(&m.lastTickUnix),
	}
	return out
}

// incMapCounter creates or increments an atomic counter in the
// sync.Map. Avoids the read+CAS race that a naive Load-then-Store
// would have under concurrent first-use.
func incMapCounter(m *sync.Map, key string) {
	v, _ := m.LoadOrStore(key, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

func flattenMap(m *sync.Map) map[string]int64 {
	out := map[string]int64{}
	m.Range(func(k, v any) bool {
		if c, ok := v.(*atomic.Int64); ok {
			out[k.(string)] = c.Load()
		}
		return true
	})
	return out
}

