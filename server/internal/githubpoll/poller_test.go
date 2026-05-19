package githubpoll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/webhooks"
)

// captureSink records every Submit call. Concurrency-safe for the
// parallel tick subtests.
type captureSink struct {
	mu     sync.Mutex
	events []captured
	err    error
}

type captured struct {
	Repo  string
	Event webhooks.TriggerEvent
}

func (s *captureSink) Submit(_ context.Context, repo string, e webhooks.TriggerEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, captured{Repo: repo, Event: e})
	return nil
}

func (s *captureSink) Events() []captured {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]captured, len(s.events))
	copy(out, s.events)
	return out
}

// newFakeGitHub returns an httptest server that responds to
// /repos/{repo}/events with the supplied JSON body on the first
// call, and `[]` afterwards. Useful for one-tick poller tests.
func newFakeGitHub(body string) *httptest.Server {
	served := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if served {
			w.Write([]byte(`[]`))
			return
		}
		served = true
		w.Header().Set("ETag", `W/"abc"`)
		w.Header().Set("X-RateLimit-Remaining", "4900")
		fmt.Fprint(w, body)
	}))
}

// newFakeGitHubStateful returns an httptest server that serves the
// i-th response body on the i-th TICK, then `[]` after the slice is
// exhausted. Each response gets a unique ETag (W/"v0", W/"v1", ...).
//
// Pagination handling: the poller's client paginates while the last
// item on a page has id > sinceID (up to pagesLimit). To model "one
// /events page per tick" without leaking responses[1] into tick 1's
// paginated walk, page=1 (or missing) serves responses[tick] and
// any higher page serves `[]` — which terminates pagination cleanly.
// The tick index is advanced once per page=1 request.
//
// Used by first-run-then-second-tick tests where the body must
// change across ticks — newFakeGitHub returns the same body once
// and then `[]`, which is the wrong shape for those tests.
func newFakeGitHubStateful(responses []string) *httptest.Server {
	var tick atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page != "" && page != "1" {
			// Pagination beyond page 1 within the same tick: serve
			// empty so the client's pagination loop terminates.
			w.Header().Set("X-RateLimit-Remaining", "4900")
			fmt.Fprint(w, `[]`)
			return
		}
		n := int(tick.Add(1)) - 1
		if n >= len(responses) {
			w.Header().Set("X-RateLimit-Remaining", "4900")
			fmt.Fprint(w, `[]`)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`W/"v%d"`, n))
		w.Header().Set("X-RateLimit-Remaining", "4900")
		fmt.Fprint(w, responses[n])
	}))
}

// errorOnceCursorStore wraps memCursorStore and returns a synthetic
// Save error on the first call only. Used to prove the first-run
// path is idempotent across Save failures: tick 1 seeds + Save
// fails + nothing forwarded → tick 2 re-seeds + Save succeeds.
type errorOnceCursorStore struct {
	inner    *memCursorStore
	failNext atomic.Bool
}

func newErrorOnceCursorStore() *errorOnceCursorStore {
	s := &errorOnceCursorStore{inner: NewMemCursorStore()}
	s.failNext.Store(true)
	return s
}

func (s *errorOnceCursorStore) Load(ctx context.Context, repo string) (Cursor, error) {
	return s.inner.Load(ctx, repo)
}

func (s *errorOnceCursorStore) Save(ctx context.Context, c Cursor) error {
	if s.failNext.CompareAndSwap(true, false) {
		return errors.New("synthetic save error")
	}
	return s.inner.Save(ctx, c)
}

func TestPoller_TickOne_DryRunEndToEnd(t *testing.T) {
	// REST /events shape: action="merged" (not webhook's "closed"
	// + merged=true), no html_url / title / merged on pull_request.
	// See PUL-185.
	//
	// PUL-186: This test exercises the steady-state classify-and-
	// submit path. Cursor is pre-seeded so cursor.LastPolledAt is
	// non-zero — discriminator reads firstRun=false and the
	// first-run seed branch is bypassed. The dedicated first-run
	// behaviour is covered by TestPoller_TickOne_FirstRun_*.
	body := `[
		{"id":"200","type":"PullRequestEvent","repo":{"name":"rabbeet/Pulse"},"payload":{
			"action":"merged","number":99,
			"pull_request":{
				"number":99,
				"url":"https://api.github.com/repos/rabbeet/Pulse/pulls/99",
				"head":{"sha":"abc123","ref":"agent-1/pul-99-x"}
			}
		}},
		{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}
	]`
	srv := newFakeGitHub(body)
	defer srv.Close()

	cur := NewMemCursorStore()
	// Pre-seed: a prior tick already happened (LastPolledAt non-zero),
	// so this tick takes the steady-state path. LastEventID below the
	// fixture's max means the events still pass the id > sinceID
	// filter in client.FetchEvents.
	_ = cur.Save(context.Background(), Cursor{
		Repo:         "rabbeet/Pulse",
		LastEventID:  50,
		LastPolledAt: time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC),
		ETag:         `W/"prev"`,
	})
	sink := &captureSink{}
	cfg := Config{
		Repos:    []string{"rabbeet/Pulse"},
		Interval: time.Second,
		Now:      func() time.Time { return time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	p := NewPoller(cfg, client, Classifier{}, cur, sink)
	p.tickOne(context.Background(), "rabbeet/Pulse")

	got := sink.Events()
	if len(got) != 1 {
		t.Fatalf("Submit count = %d, want 1 (pr_merged only; PushEvent skipped)", len(got))
	}
	if got[0].Event.EventType != webhooks.EventTypePRMerged {
		t.Errorf("EventType = %q, want %q", got[0].Event.EventType, webhooks.EventTypePRMerged)
	}
	if got[0].Event.PRNumber != 99 {
		t.Errorf("PRNumber = %d, want 99", got[0].Event.PRNumber)
	}

	// PUL-201: cursor advances per-event-type, not as a single scalar.
	// PullRequestEvent should be at 200, PushEvent at 100 — covers
	// both classify-and-submit and skip paths advancing their type's
	// position.
	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PullRequestEvent"] != 200 {
		t.Errorf("CursorByType[PullRequestEvent] = %d, want 200", saved.CursorByType["PullRequestEvent"])
	}
	if saved.CursorByType["PushEvent"] != 100 {
		t.Errorf("CursorByType[PushEvent] = %d, want 100", saved.CursorByType["PushEvent"])
	}
	if saved.ETag != `W/"abc"` {
		t.Errorf("ETag = %q, want %q", saved.ETag, `W/"abc"`)
	}
	if !saved.LastPolledAt.Equal(time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("LastPolledAt = %v, want injected Now", saved.LastPolledAt)
	}
}

func TestPoller_TickOne_NotModified(t *testing.T) {
	// Pre-populate cursor with an ETag so the client sends
	// If-None-Match. Server replies 304 → cursor.LastPolledAt
	// updates, LastEventID stays.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "" {
			t.Errorf("expected If-None-Match header on conditional request")
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	cur := NewMemCursorStore()
	_ = cur.Save(context.Background(), Cursor{
		Repo: "rabbeet/Pulse", LastEventID: 500, ETag: `W/"prev"`,
		LastPolledAt: time.Date(2026, 5, 18, 7, 0, 0, 0, time.UTC),
		CursorByType: map[string]int64{"PushEvent": 500},
	})
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/Pulse"},
		Now:   func() time.Time { return time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PushEvent"] != 500 {
		t.Errorf("CursorByType[PushEvent] = %d, want unchanged 500 on 304", saved.CursorByType["PushEvent"])
	}
	if !saved.LastPolledAt.Equal(time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("LastPolledAt = %v, want bumped to injected Now", saved.LastPolledAt)
	}
	if len(sink.Events()) != 0 {
		t.Errorf("Submit called %d times on 304, want 0", len(sink.Events()))
	}
}

func TestPoller_TickOne_RateLimitedDoesNotAdvance(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", reset))
		http.Error(w, "rate", http.StatusForbidden)
	}))
	defer srv.Close()

	cur := NewMemCursorStore()
	_ = cur.Save(context.Background(), Cursor{
		Repo:         "rabbeet/Pulse",
		LastEventID:  1000,
		CursorByType: map[string]int64{"PushEvent": 1000},
	})
	sink := &captureSink{}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(Config{Repos: []string{"rabbeet/Pulse"}}, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PushEvent"] != 1000 {
		t.Errorf("CursorByType[PushEvent] = %d, want unchanged 1000 on rate limit", saved.CursorByType["PushEvent"])
	}
}

func TestPoller_TickOne_SinkErrorStopsAdvance(t *testing.T) {
	// Sink failing on the first classified event → cursor does NOT
	// advance past that event, so the next tick reprocesses it.
	// Critical for idempotency: a slow downstream must not cause
	// silent drops.
	// REST /events shape — see PUL-185.
	body := `[
		{"id":"200","type":"PullRequestEvent","repo":{"name":"rabbeet/Pulse"},"payload":{
			"action":"merged","number":99,
			"pull_request":{"number":99,"url":"https://api.github.com/repos/rabbeet/Pulse/pulls/99",
				"head":{"sha":"abc","ref":"agent-1/x"}}
		}}
	]`
	srv := newFakeGitHub(body)
	defer srv.Close()

	cur := NewMemCursorStore()
	// PUL-186: pre-seed cursor so this tick takes the steady-state
	// classify-and-submit path (not first-run seed). PUL-201:
	// CursorByType seeded below the fixture event id so it passes
	// the per-type filter in client.FetchEvents.
	_ = cur.Save(context.Background(), Cursor{
		Repo:         "rabbeet/Pulse",
		LastEventID:  50,
		LastPolledAt: time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC),
		ETag:         `W/"prev"`,
		CursorByType: map[string]int64{"PullRequestEvent": 50},
	})
	sink := &captureSink{err: errors.New("downstream down")}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(Config{Repos: []string{"rabbeet/Pulse"}}, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PullRequestEvent"] != 50 {
		t.Errorf("CursorByType[PullRequestEvent] = %d, want unchanged 50 (cursor must not advance past failed submit)",
			saved.CursorByType["PullRequestEvent"])
	}
}

func TestPoller_Run_TicksUntilCancel(t *testing.T) {
	// Two ticks: first sees event 100 (PushEvent → skip), second
	// sees nothing (304 path is avoided here by serving [] on
	// subsequent calls — same as newFakeGitHub).
	//
	// PUL-186: pre-seed cursor so the first tick takes the
	// steady-state classify path (not first-run seed which would
	// skip the PushEvent without invoking the classifier).
	srv := newFakeGitHub(`[{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}]`)
	defer srv.Close()

	cur := NewMemCursorStore()
	_ = cur.Save(context.Background(), Cursor{
		Repo:         "rabbeet/Pulse",
		LastEventID:  50,
		LastPolledAt: time.Date(2026, 5, 18, 8, 0, 0, 0, time.UTC),
		ETag:         `W/"prev"`,
		CursorByType: map[string]int64{"PushEvent": 50},
	})
	sink := &captureSink{}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	p := NewPoller(Config{
		Repos:    []string{"rabbeet/Pulse"},
		Interval: 10 * time.Millisecond,
	}, client, Classifier{}, cur, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := p.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v, want context error", err)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PushEvent"] != 100 {
		t.Errorf("CursorByType[PushEvent] = %d, want 100 after first tick", saved.CursorByType["PushEvent"])
	}
}

func TestPoller_Run_EmptyReposExitsImmediately(t *testing.T) {
	p := NewPoller(Config{}, nil, Classifier{}, nil, nil)
	err := p.Run(context.Background())
	if err != nil {
		t.Errorf("Run with empty Repos err = %v, want nil", err)
	}
}

func TestParseRepos(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"commas only", ",,,", nil},
		{"single valid", "rabbeet/Pulse", []string{"rabbeet/Pulse"}},
		{"two valid with whitespace", " rabbeet/Pulse , rabbeet/multica ", []string{"rabbeet/Pulse", "rabbeet/multica"}},
		{"short names valid", "a/b,,c/d", []string{"a/b", "c/d"}},
		{"dot and underscore valid", "my-org/.github_repo", []string{"my-org/.github_repo"}},
		{"reject no slash", "rabbeet:Pulse", nil},
		{"reject extra slash", "rabbeet/Pulse/foo", nil},
		{"reject path traversal", "rabbeet/../admin", nil},
		{"reject leading dotdot owner", "../foo", nil},
		{"reject dot-only component", "./foo", nil},
		{"reject query injection", "rabbeet/Pulse?evil=1", nil},
		{"reject fragment injection", "rabbeet/Pulse#evil", nil},
		{"reject empty component before slash", "/Pulse", nil},
		{"reject empty component after slash", "rabbeet/", nil},
		{"mixed valid and invalid keeps valids", "rabbeet/Pulse,../evil,good/one", []string{"rabbeet/Pulse", "good/one"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRepos(tc.in)
			if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", tc.want) {
				t.Errorf("ParseRepos(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsValidRepoName(t *testing.T) {
	good := []string{"rabbeet/Pulse", "a/b", "my-org/.github_repo", "Org.Name/repo-name"}
	bad := []string{"", "rabbeet", "rabbeet/", "/Pulse", "../foo", "rabbeet/../admin",
		"./foo", "rabbeet/.", "rabbeet/..", "../..", "rabbeet/Pulse/extra"}
	for _, s := range good {
		if !isValidRepoName(s) {
			t.Errorf("isValidRepoName(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isValidRepoName(s) {
			t.Errorf("isValidRepoName(%q) = true, want false", s)
		}
	}
}

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in      int
		want    time.Duration
		wantErr bool
	}{
		{0, 30 * time.Second, false},
		{-5, 30 * time.Second, false},
		{1, 10 * time.Second, false},
		{30, 30 * time.Second, false},
		{60, 60 * time.Second, false},
		{3600, time.Hour, false},
		{3601, 0, true},
	}
	for _, tc := range cases {
		got, err := ParseInterval(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseInterval(%d) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseInterval(%d) err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseInterval(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoggingSink(t *testing.T) {
	// LoggingSink should never error. Smoke test only.
	s := LoggingSink{}
	err := s.Submit(context.Background(), "x/y", webhooks.TriggerEvent{})
	if err != nil {
		t.Errorf("LoggingSink.Submit err = %v, want nil", err)
	}
}

// --- PUL-186 first-run seeding ---
//
// The four tests below cover the firstRun branch added to
// poller.tickOne. The discriminator is single-field
// `cursor.LastPolledAt.IsZero()` because cursor.Save writes
// LastPolledAt = now() on every successful tick (304, 200, even the
// empty-events fall-through path) — "we have ever polled" maps 1:1
// to "LastPolledAt non-zero". An operator who runs
// `UPDATE github_poll_cursor SET last_event_id = NULL` to rewind
// does NOT clear LastPolledAt, so the operator-rewind case correctly
// classifies as steady-state and events flow normally.

// loadFirstRunFixture reads the full-page /events capture under
// testdata. The fixture is the redacted output of
// `gh api '/repos/rabbeet/multica/events?per_page=100'`. Used as-is
// to defend against the PUL-185 "byte-equivalent" review-gap: tests
// read the on-disk capture, not a hand-rolled approximation.
func loadFirstRunFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "first_run_events_capture.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(b)
}

// firstRunFixtureMaxID is the maximum event id in
// first_run_events_capture.json. Recorded as a constant so the seed
// assertion is explicit; recompute via the helper below if the
// fixture is ever re-captured.
//
// To recompute:
//
//	jq '[.[] | .id | tonumber] | max' \
//	    server/internal/githubpoll/testdata/first_run_events_capture.json
const firstRunFixtureMaxID int64 = 12035692421

// firstRunFixturePRMaxID is the max PullRequestEvent id in the
// fixture. Used by tests that need to assert per-type seed values
// (PUL-201). PR-shaped events live on the ~9.6e9 number line so the
// PR-type max diverges sharply from the global max — that is the
// whole point of per-event-type cursors.
//
// To recompute:
//
//	jq '[.[] | select(.type == "PullRequestEvent") | .id | tonumber] | max' \
//	    server/internal/githubpoll/testdata/first_run_events_capture.json
const firstRunFixturePRMaxID int64 = 9610256213

// maxCursorByType returns the largest value across a CursorByType
// map. Used by firstRun seed assertions where we want to verify the
// global max id landed somewhere in the per-type map (which exact
// type "owns" the max depends on fixture composition).
func maxCursorByType(m map[string]int64) int64 {
	var max int64
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

func TestPoller_TickOne_FirstRun_SkipsHistoricEvents(t *testing.T) {
	// Fresh cursor (no prior poll). Fixture contains 100 events
	// spanning ~90 days of /events history. After firstRun seed, the
	// sink must have received ZERO events (those are pre-existing
	// state, not "events that arrived while we were watching") and
	// the cursor must advance to the fixture's max id.
	srv := newFakeGitHubStateful([]string{loadFirstRunFixture(t)})
	defer srv.Close()

	cur := NewMemCursorStore()
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/multica"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/multica")

	if got := len(sink.Events()); got != 0 {
		t.Errorf("Submit count = %d, want 0 (first-run must not forward historic events)", got)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/multica")
	// PUL-201: cursor is now per-type. The max across all types equals
	// the fixture's global max id; the PR-type max diverges (lives on
	// the ~9.6e9 stream while Create/Push/Delete live at ~12e9), so we
	// assert per-type explicitly to lock in the multi-stream semantics.
	if got := maxCursorByType(saved.CursorByType); got != firstRunFixtureMaxID {
		t.Errorf("max(CursorByType) = %d, want %d (fixture global max)", got, firstRunFixtureMaxID)
	}
	if got := saved.CursorByType["PullRequestEvent"]; got != firstRunFixturePRMaxID {
		t.Errorf("CursorByType[PullRequestEvent] = %d, want %d (PR-stream max — distinct from global max under per-type cursors)",
			got, firstRunFixturePRMaxID)
	}
	// Seed must populate EVERY type observed on the page so the next
	// tick does not re-forward stale events of a type that happened to
	// be classifier-skipped (PUL-201 eng-review).
	for _, want := range []string{"CreateEvent", "DeleteEvent", "IssueCommentEvent", "PullRequestEvent", "PushEvent"} {
		if _, ok := saved.CursorByType[want]; !ok {
			t.Errorf("CursorByType missing seed for %q — every Event.Type on page 1 must be seeded", want)
		}
	}
	if saved.ETag != `W/"v0"` {
		t.Errorf("ETag = %q, want %q", saved.ETag, `W/"v0"`)
	}
	if !saved.LastPolledAt.Equal(cfg.Now()) {
		t.Errorf("LastPolledAt = %v, want injected Now", saved.LastPolledAt)
	}
}

func TestPoller_TickOne_SecondTickAfterSeedSubmitsOnlyNewEvents(t *testing.T) {
	// First tick: seed at HEAD using the full fixture. Second tick:
	// the same fixture plus one synthetic newer PR-merged event
	// (id > firstRunFixtureMaxID). Only the new event should be
	// forwarded to the sink — the historic ones from tick 1 are
	// filtered by client.FetchEvents's `id <= sinceID` check.
	newerID := firstRunFixtureMaxID + 1
	newerEvent := fmt.Sprintf(`{
		"id": "%d",
		"type": "PullRequestEvent",
		"actor": {"id": 0, "login": "redacted-user"},
		"repo": {"id": 0, "name": "rabbeet/multica"},
		"payload": {
			"action": "merged",
			"number": 999,
			"pull_request": {
				"url": "https://api.github.com/repos/rabbeet/multica/pulls/999",
				"id": 99999,
				"number": 999,
				"head": {"ref": "agent-2/pul-186-implementation", "sha": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
			}
		},
		"created_at": "2026-05-19T10:00:00Z"
	}`, newerID)

	fixture := loadFirstRunFixture(t)
	// Inject the new event at the head of the array (GitHub returns
	// newest-first). Hand-rolling the splice is safe because the
	// fixture starts with `[` followed by whitespace + the first
	// event's `{`.
	tick2Body := "[\n" + newerEvent + ",\n" + fixture[1:]

	srv := newFakeGitHubStateful([]string{fixture, tick2Body})
	defer srv.Close()

	cur := NewMemCursorStore()
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/multica"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	p := NewPoller(cfg, client, Classifier{}, cur, sink)

	// Tick 1 — seed only, no submits.
	p.tickOne(context.Background(), "rabbeet/multica")
	if got := len(sink.Events()); got != 0 {
		t.Fatalf("tick 1 Submit count = %d, want 0", got)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/multica")
	if got := maxCursorByType(saved.CursorByType); got != firstRunFixtureMaxID {
		t.Fatalf("tick 1 max(CursorByType) = %d, want %d", got, firstRunFixtureMaxID)
	}

	// Tick 2 — only the newer PR event passes per-type filtering;
	// the historic 100 are filtered out (PR ones against the seeded
	// PR cursor, others against their own type cursors).
	p.tickOne(context.Background(), "rabbeet/multica")
	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("tick 2 Submit count = %d, want 1", len(events))
	}
	if events[0].Event.EventType != webhooks.EventTypePRMerged {
		t.Errorf("tick 2 EventType = %q, want %q", events[0].Event.EventType, webhooks.EventTypePRMerged)
	}
	if events[0].Event.PRNumber != 999 {
		t.Errorf("tick 2 PRNumber = %d, want 999", events[0].Event.PRNumber)
	}
	saved2, _ := cur.Load(context.Background(), "rabbeet/multica")
	if saved2.CursorByType["PullRequestEvent"] != newerID {
		t.Errorf("tick 2 CursorByType[PullRequestEvent] = %d, want %d (new PR event must advance the PR cursor)",
			saved2.CursorByType["PullRequestEvent"], newerID)
	}
	// Non-PR per-type cursors must be unchanged from tick 1 — tick 2
	// has no fresh events of those types so their positions must NOT
	// move.
	for _, t2 := range []string{"PushEvent", "CreateEvent", "DeleteEvent"} {
		if saved2.CursorByType[t2] != saved.CursorByType[t2] {
			t.Errorf("tick 2 CursorByType[%s] = %d, want unchanged %d from tick 1 (no fresh events of that type)",
				t2, saved2.CursorByType[t2], saved.CursorByType[t2])
		}
	}
}

func TestPoller_TickOne_OperatorRewindDoesNotSeed(t *testing.T) {
	// Operator workflow: `UPDATE github_poll_cursor SET last_event_id
	// = NULL, etag = NULL WHERE repo = '…'` to replay a window. This
	// leaves LastPolledAt intact (it was set on every prior successful
	// tick). The discriminator must treat this as steady-state — NOT
	// first-run — so events flow normally to the sink.
	//
	// This is the critical guard against an implementer "simplifying"
	// the check to `cursor.LastEventID == 0`. Without this test that
	// simplification would silently break operator-rewind and CI would
	// stay green.
	body := `[
		{"id":"500","type":"PullRequestEvent","repo":{"name":"rabbeet/Pulse"},"payload":{
			"action":"merged","number":42,
			"pull_request":{
				"number":42,
				"url":"https://api.github.com/repos/rabbeet/Pulse/pulls/42",
				"head":{"sha":"feedface","ref":"agent-1/rewind-test"}
			}
		}}
	]`
	srv := newFakeGitHub(body)
	defer srv.Close()

	cur := NewMemCursorStore()
	// Pre-load the cursor as if a prior successful tick happened
	// some time ago AND the operator subsequently nulled
	// last_event_id and etag. LastPolledAt being non-zero is the
	// invariant the discriminator relies on.
	_ = cur.Save(context.Background(), Cursor{
		Repo:         "rabbeet/Pulse",
		LastEventID:  0,
		LastPolledAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ETag:         "",
	})
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/Pulse"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/Pulse")

	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("Submit count = %d, want 1 (operator rewind must NOT trigger first-run seed)", len(events))
	}
	if events[0].Event.EventType != webhooks.EventTypePRMerged {
		t.Errorf("EventType = %q, want %q", events[0].Event.EventType, webhooks.EventTypePRMerged)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.CursorByType["PullRequestEvent"] != 500 {
		t.Errorf("CursorByType[PullRequestEvent] = %d, want 500 (cursor must advance to the event id)",
			saved.CursorByType["PullRequestEvent"])
	}
}

func TestPoller_TickOne_FirstRun_EmptyResponseStillSeedsRow(t *testing.T) {
	// First tick with NO events in the /events response. The seed
	// branch falls through to the existing classify loop (which
	// iterates over zero events and saves the row with
	// LastEventID = 0, ETag from the response header, LastPolledAt
	// = now). This breaks the firstRun condition for the next tick —
	// without this behavior an empty cold-start could perpetually
	// re-trigger firstRun and silently drop the first batch of real
	// events whenever they finally arrive.
	srv := newFakeGitHubStateful([]string{`[]`})
	defer srv.Close()

	cur := NewMemCursorStore()
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/multica"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/multica")

	if got := len(sink.Events()); got != 0 {
		t.Errorf("Submit count = %d, want 0 (empty response forwards nothing)", got)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/multica")
	if saved.LastEventID != 0 {
		t.Errorf("LastEventID = %d, want 0 (no events to seed from)", saved.LastEventID)
	}
	if saved.ETag != `W/"v0"` {
		t.Errorf("ETag = %q, want %q (saved from response header)", saved.ETag, `W/"v0"`)
	}
	if !saved.LastPolledAt.Equal(cfg.Now()) {
		t.Errorf("LastPolledAt = %v, want injected Now — required so next tick is NOT firstRun", saved.LastPolledAt)
	}
}

func TestPoller_TickOne_FirstRun_SaveErrorReSeedsNextTick(t *testing.T) {
	// Save failure on the first-run path is non-fatal: the next tick
	// reads no cursor row again, is firstRun again, re-seeds at the
	// (then-current) HEAD. Nothing is forwarded across either tick,
	// so the operation is idempotent.
	//
	// Mirrors TestPoller_TickOne_SinkErrorStopsAdvance's role for
	// the classify-and-submit path, but for the seed path.
	fixture := loadFirstRunFixture(t)
	srv := newFakeGitHubStateful([]string{fixture, fixture})
	defer srv.Close()

	cur := newErrorOnceCursorStore()
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/multica"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	p := NewPoller(cfg, client, Classifier{}, cur, sink)

	// Tick 1 — seed runs, Save synthetic-errors, cursor row absent.
	p.tickOne(context.Background(), "rabbeet/multica")
	if got := len(sink.Events()); got != 0 {
		t.Fatalf("tick 1 Submit count = %d, want 0", got)
	}
	saved1, _ := cur.Load(context.Background(), "rabbeet/multica")
	if saved1.LastEventID != 0 {
		t.Fatalf("tick 1 cursor should be absent (Save failed) but LastEventID = %d", saved1.LastEventID)
	}
	if !saved1.LastPolledAt.IsZero() {
		t.Fatalf("tick 1 cursor should still be unset, but LastPolledAt = %v", saved1.LastPolledAt)
	}

	// Tick 2 — still firstRun, Save succeeds this time, cursor lands.
	p.tickOne(context.Background(), "rabbeet/multica")
	if got := len(sink.Events()); got != 0 {
		t.Errorf("tick 2 Submit count = %d, want 0 (still firstRun, still no forward)", got)
	}
	saved2, _ := cur.Load(context.Background(), "rabbeet/multica")
	if got := maxCursorByType(saved2.CursorByType); got != firstRunFixtureMaxID {
		t.Errorf("tick 2 max(CursorByType) = %d, want %d", got, firstRunFixtureMaxID)
	}
}

// TestPoller_TickOne_MigrationFromLegacyCursor_Reseeds models the
// post-PUL-201 deploy state on a row that pre-existed under the
// legacy single-scalar cursor. The 086 up migration clears
// last_polled_at + etag on every row precisely so this branch fires
// on the first post-deploy tick: cursor.LastPolledAt.IsZero() →
// first-run seed engages → per-type map is populated from a fresh
// /events snapshot → nothing is forwarded to the sink (those events
// are pre-existing state, not "events that arrived while we were
// watching").
//
// Without the migration's `UPDATE … SET last_polled_at = NULL`, the
// row would carry a non-zero LastPolledAt + empty CursorByType, the
// seed branch would skip, and the next tick would forward up to 100
// events from /events into the sink. This test guards that property
// from a Go-side regression (e.g. someone "simplifies" the seed
// discriminator).
func TestPoller_TickOne_MigrationFromLegacyCursor_Reseeds(t *testing.T) {
	srv := newFakeGitHubStateful([]string{loadFirstRunFixture(t)})
	defer srv.Close()

	cur := NewMemCursorStore()
	// Pre-load a row in the shape the 086 up migration leaves
	// existing rows in: legacy LastEventID still populated, but
	// LastPolledAt + ETag have been NULL'd. CursorByType empty.
	_ = cur.Save(context.Background(), Cursor{
		Repo:        "rabbeet/multica",
		LastEventID: 12000000000, // pre-migration scalar position
		// LastPolledAt and ETag are zero — that's the migration's
		// signal to re-seed.
	})

	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/multica"},
		Now:   func() time.Time { return time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/multica")

	// First post-deploy tick must NOT forward historic events.
	if got := len(sink.Events()); got != 0 {
		t.Errorf("Submit count = %d, want 0 (post-migration first tick must re-seed silently)", got)
	}
	saved, _ := cur.Load(context.Background(), "rabbeet/multica")
	if len(saved.CursorByType) == 0 {
		t.Fatal("CursorByType empty after migration-reseed tick — seed branch did not fire")
	}
	if got := maxCursorByType(saved.CursorByType); got != firstRunFixtureMaxID {
		t.Errorf("max(CursorByType) = %d, want %d (fixture global max)", got, firstRunFixtureMaxID)
	}
	if got := saved.CursorByType["PullRequestEvent"]; got != firstRunFixturePRMaxID {
		t.Errorf("CursorByType[PullRequestEvent] = %d, want %d (PR-stream max)",
			got, firstRunFixturePRMaxID)
	}
	if !saved.LastPolledAt.Equal(cfg.Now()) {
		t.Errorf("LastPolledAt = %v, want injected Now (must be set so next tick is NOT firstRun)", saved.LastPolledAt)
	}
}

// TestFormatCursorByType_DeterministicAndStable confirms the log
// repr is sorted by key — operators grepping logs across ticks see a
// stable shape, and snapshot diffs in the audit trail are
// meaningful. Empty input must produce an empty string rather than
// `{}` or similar noise that would clutter the dashboard.
func TestFormatCursorByType_DeterministicAndStable(t *testing.T) {
	got := formatCursorByType(map[string]int64{
		"PushEvent":        12078860926,
		"PullRequestEvent": 9648929307,
		"CreateEvent":      12079087170,
	})
	want := "CreateEvent:12079087170,PullRequestEvent:9648929307,PushEvent:12078860926"
	if got != want {
		t.Errorf("formatCursorByType = %q, want %q", got, want)
	}
	if formatCursorByType(nil) != "" {
		t.Errorf("formatCursorByType(nil) should be empty string")
	}
	if formatCursorByType(map[string]int64{}) != "" {
		t.Errorf("formatCursorByType(empty) should be empty string")
	}
}
