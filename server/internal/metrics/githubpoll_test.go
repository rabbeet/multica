package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/multica-ai/multica/server/internal/githubpoll"
)

// TestGithubPollCollector_CursorAgeDerivedAtScrape locks in the
// design contract that motivated the review fix: a stuck poller
// surfaces as a growing cursor_age_seconds gauge, NOT a frozen
// "we last computed age = 0" reading. The test simulates a poller
// that marked one successful tick, then never ran again; the
// collector's scrape-time derivation must read the growing delta.
func TestGithubPollCollector_CursorAgeDerivedAtScrape(t *testing.T) {
	m := githubpoll.NewMetrics()
	// Poller successfully ticked at unix 1_700_000_000.
	m.MarkTickComplete("rabbeet/Pulse", 1700000000)

	// Scrape happens 200s later. Poller hasn't ticked again — the
	// "stuck goroutine" condition the alert needs to catch.
	scrapeAt := time.Unix(1700000200, 0)
	c := newGithubPollCollectorWithClock(m, func() time.Time { return scrapeAt })

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `# HELP multica_github_poll_cursor_age_seconds Seconds since the last completed poll tick for the repo. Computed at scrape time so a stuck goroutine reads as a growing gauge. Alert at > 2 × tick interval.
# TYPE multica_github_poll_cursor_age_seconds gauge
multica_github_poll_cursor_age_seconds{repo="rabbeet/Pulse"} 200
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "multica_github_poll_cursor_age_seconds"); err != nil {
		t.Errorf("unexpected collection result:\n%s", err)
	}

	// A later scrape sees a larger gauge. Confirms the derivation
	// is recomputed every Collect.
	scrapeAt = time.Unix(1700000500, 0)
	expected = `# HELP multica_github_poll_cursor_age_seconds Seconds since the last completed poll tick for the repo. Computed at scrape time so a stuck goroutine reads as a growing gauge. Alert at > 2 × tick interval.
# TYPE multica_github_poll_cursor_age_seconds gauge
multica_github_poll_cursor_age_seconds{repo="rabbeet/Pulse"} 500
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "multica_github_poll_cursor_age_seconds"); err != nil {
		t.Errorf("unexpected second-scrape result:\n%s", err)
	}
}

func TestGithubPollCollector_CursorAgeNegativeClampedToZero(t *testing.T) {
	// Clock skew — tick wrote a future timestamp. Defensive
	// clamping keeps the gauge interpretable (no negative ages).
	m := githubpoll.NewMetrics()
	m.MarkTickComplete("rabbeet/Pulse", 1700000500)

	c := newGithubPollCollectorWithClock(m, func() time.Time { return time.Unix(1700000000, 0) })
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := `# HELP multica_github_poll_cursor_age_seconds Seconds since the last completed poll tick for the repo. Computed at scrape time so a stuck goroutine reads as a growing gauge. Alert at > 2 × tick interval.
# TYPE multica_github_poll_cursor_age_seconds gauge
multica_github_poll_cursor_age_seconds{repo="rabbeet/Pulse"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "multica_github_poll_cursor_age_seconds"); err != nil {
		t.Errorf("unexpected clamp result:\n%s", err)
	}
}

func TestGithubPollCollector_CountersAndGaugesEmitted(t *testing.T) {
	m := githubpoll.NewMetrics()
	m.IncPanic()
	m.IncCall("rabbeet/Pulse", 200)
	m.IncCall("rabbeet/Pulse", 200)
	m.IncEvent("rabbeet/Pulse", "ci_failure")
	m.IncSinkError("rabbeet/Pulse")
	m.SetRateLimitRemaining("rabbeet/Pulse", 4321)
	m.MarkTickComplete("rabbeet/Pulse", 1700000000)

	c := newGithubPollCollectorWithClock(m, func() time.Time { return time.Unix(1700000010, 0) })
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	// Spot-check each metric is emitted. Exact label-value
	// formatting is verified above for cursor_age — here we just
	// confirm the Describe / Collect plumbing is right.
	for _, name := range []string{
		"multica_github_poll_panics_total",
		"multica_github_poll_calls_total",
		"multica_github_poll_events_total",
		"multica_github_poll_sink_errors_total",
		"multica_github_poll_rate_limit_remaining",
		"multica_github_poll_cursor_age_seconds",
	} {
		if got := testutil.CollectAndCount(c, name); got == 0 {
			t.Errorf("metric %q not emitted (count = 0)", name)
		}
	}
}
