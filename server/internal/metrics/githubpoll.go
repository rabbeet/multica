package metrics

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/multica-ai/multica/server/internal/githubpoll"
)

// GithubPollCollector exposes the githubpoll.Metrics counters as
// Prometheus metrics. Same shape as RealtimeCollector / DBCollector
// — Describe declares the wire-format constants, Collect snapshots
// the atomics and emits one sample per label set.
//
// Counters: calls_total, events_total, sink_errors_total,
// panics_total. Gauges: rate_limit_remaining, cursor_age_seconds.
// All metrics prefixed `multica_github_poll_` so they sort next to
// the cascade ingress counters in the dashboards.
type GithubPollCollector struct {
	metrics *githubpoll.Metrics
	now     func() time.Time

	callsTotal         *prometheus.Desc
	eventsTotal        *prometheus.Desc
	sinkErrorsTotal    *prometheus.Desc
	panicsTotal        *prometheus.Desc
	rateLimitRemaining *prometheus.Desc
	cursorAgeSeconds   *prometheus.Desc
}

// NewGithubPollCollector wraps a githubpoll.Metrics. The pointer is
// retained — Collect calls Snapshot on every scrape and derives
// cursor_age_seconds against time.Now so a stuck poller surfaces as
// a growing gauge instead of a frozen one.
func NewGithubPollCollector(m *githubpoll.Metrics) *GithubPollCollector {
	return newGithubPollCollectorWithClock(m, time.Now)
}

// newGithubPollCollectorWithClock is the test-injectable constructor.
// Production NewGithubPollCollector binds time.Now; tests bind a
// deterministic clock so the cursor_age_seconds derivation is
// reproducible.
func newGithubPollCollectorWithClock(m *githubpoll.Metrics, now func() time.Time) *GithubPollCollector {
	return &GithubPollCollector{
		metrics:            m,
		now:                now,
		callsTotal:         newGithubPollDesc("calls_total", "Total /repos/{owner}/{repo}/events calls by HTTP status.", []string{"repo", "status"}),
		eventsTotal:        newGithubPollDesc("events_total", "Total events seen on the poll channel by classifier outcome.", []string{"repo", "event_type"}),
		sinkErrorsTotal:    newGithubPollDesc("sink_errors_total", "Total Sink.Submit failures (transient sink outage).", []string{"repo"}),
		panicsTotal:        newGithubPollDesc("panics_total", "Total panics caught by the supervisor. Steady-state expected value: 0.", nil),
		rateLimitRemaining: newGithubPollDesc("rate_limit_remaining", "Last observed X-RateLimit-Remaining for the GitHub PAT/App token.", []string{"repo"}),
		cursorAgeSeconds:   newGithubPollDesc("cursor_age_seconds", "Seconds since the last completed poll tick for the repo. Computed at scrape time so a stuck goroutine reads as a growing gauge. Alert at > 2 × tick interval.", []string{"repo"}),
	}
}

// Describe implements prometheus.Collector.
func (c *GithubPollCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.callsTotal
	ch <- c.eventsTotal
	ch <- c.sinkErrorsTotal
	ch <- c.panicsTotal
	ch <- c.rateLimitRemaining
	ch <- c.cursorAgeSeconds
}

// Collect implements prometheus.Collector.
func (c *GithubPollCollector) Collect(ch chan<- prometheus.Metric) {
	if c.metrics == nil {
		return
	}
	snap := c.metrics.Snapshot()

	ch <- prometheus.MustNewConstMetric(c.panicsTotal, prometheus.CounterValue, float64(snap.Panics))

	for key, v := range snap.Calls {
		repo, status, ok := splitLabels(key)
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.callsTotal, prometheus.CounterValue, float64(v), repo, status)
	}
	for key, v := range snap.Events {
		repo, eventType, ok := splitLabels(key)
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.eventsTotal, prometheus.CounterValue, float64(v), repo, eventType)
	}
	for repo, v := range snap.SinkErrors {
		ch <- prometheus.MustNewConstMetric(c.sinkErrorsTotal, prometheus.CounterValue, float64(v), repo)
	}
	for repo, v := range snap.RateLimitRemaining {
		ch <- prometheus.MustNewConstMetric(c.rateLimitRemaining, prometheus.GaugeValue, float64(v), repo)
	}
	// Derive cursor_age_seconds at scrape time. Storing the unix
	// timestamp of the last tick (instead of a precomputed age)
	// means a stuck or panicking goroutine reads as a growing
	// gauge that trips the staleness alert; a precomputed age
	// would freeze on death and stay silent.
	nowUnix := c.now().Unix()
	for repo, ts := range snap.LastTickUnix {
		age := nowUnix - ts
		if age < 0 {
			// Clock skew or someone wrote a future timestamp.
			// Clamp to 0 so the gauge stays interpretable.
			age = 0
		}
		ch <- prometheus.MustNewConstMetric(c.cursorAgeSeconds, prometheus.GaugeValue, float64(age), repo)
	}
}

func newGithubPollDesc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc("multica_github_poll_"+name, help, labels, nil)
}

// splitLabels parses the "repo|other" composite key the Metrics
// methods use internally. Returns ok=false if the key is malformed
// — defensive against a future Metrics method that forgets to
// include the pipe.
func splitLabels(key string) (string, string, bool) {
	pipe := strings.IndexByte(key, '|')
	if pipe < 0 {
		return "", "", false
	}
	return key[:pipe], key[pipe+1:], true
}
