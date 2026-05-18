package githubpoll

import (
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
	// is "ci_failure", "pr_merged", "pr_review_change",
	// "pr_title_edit", "skip", or "schema_mismatch". The "skip"
	// bucket bundles ErrSkip (success conclusion, non-merge close,
	// approval review, push, etc.) so the alert path has one
	// distinct bucket per real event-type.
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

	// CursorAgeSeconds stores the most recently observed
	// (now - last_polled_at) per repo. Gauge semantics. Alert at
	// > 120s — the tick is 30s, so two missed ticks is the
	// canary that the poller goroutine is stuck.
	cursorAgeSeconds sync.Map // map[string]*atomic.Int64
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
	incMapCounter(&m.callsTotal, repo+"|"+itoa(statusCode))
}

// IncEvent increments the per-(repo, event_type) event counter.
// eventType is one of the webhook constants (ci_failure, pr_merged,
// pr_review_change, pr_title_edit) for classified events, or
// "skip" / "schema_mismatch" for the negative paths.
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

// SetCursorAge records the latest (now - last_polled_at).
func (m *Metrics) SetCursorAge(repo string, seconds int64) {
	if m == nil {
		return
	}
	v, _ := m.cursorAgeSeconds.LoadOrStore(repo, new(atomic.Int64))
	v.(*atomic.Int64).Store(seconds)
}

// Snapshot returns a flat map view of all counters / gauges, used by
// the Prometheus collector and by integration tests. Map keys are
// the same composite strings the Inc* methods use as their map keys
// internally — the Prometheus side splits them back into label pairs.
type Snapshot struct {
	Panics             int64
	Calls              map[string]int64 // key: "repo|status_code"
	Events             map[string]int64 // key: "repo|event_type"
	SinkErrors         map[string]int64 // key: "repo"
	RateLimitRemaining map[string]int64 // key: "repo"
	CursorAgeSeconds   map[string]int64 // key: "repo"
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
		CursorAgeSeconds:   flattenMap(&m.cursorAgeSeconds),
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

// itoa avoids importing strconv just for this single call. Limited
// to small non-negative integers (HTTP status codes); negative input
// is normalized to 0.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 && i > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
