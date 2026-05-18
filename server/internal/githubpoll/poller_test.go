package githubpoll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestPoller_TickOne_DryRunEndToEnd(t *testing.T) {
	// REST /events shape: action="merged" (not webhook's "closed"
	// + merged=true), no html_url / title / merged on pull_request.
	// See PUL-185.
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

	// Cursor must advance to the highest seen event ID, not just the
	// classified one.
	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.LastEventID != 200 {
		t.Errorf("LastEventID = %d, want 200", saved.LastEventID)
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
	})
	sink := &captureSink{}
	cfg := Config{
		Repos: []string{"rabbeet/Pulse"},
		Now:   func() time.Time { return time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC) },
	}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(cfg, client, Classifier{}, cur, sink).tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.LastEventID != 500 {
		t.Errorf("LastEventID = %d, want unchanged 500 on 304", saved.LastEventID)
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
	_ = cur.Save(context.Background(), Cursor{Repo: "rabbeet/Pulse", LastEventID: 1000})
	sink := &captureSink{}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(Config{Repos: []string{"rabbeet/Pulse"}}, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.LastEventID != 1000 {
		t.Errorf("LastEventID = %d, want unchanged 1000 on rate limit", saved.LastEventID)
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
	sink := &captureSink{err: errors.New("downstream down")}
	client := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	NewPoller(Config{Repos: []string{"rabbeet/Pulse"}}, client, Classifier{}, cur, sink).
		tickOne(context.Background(), "rabbeet/Pulse")

	saved, _ := cur.Load(context.Background(), "rabbeet/Pulse")
	if saved.LastEventID != 0 {
		t.Errorf("LastEventID = %d, want 0 (cursor must not advance past failed submit)", saved.LastEventID)
	}
}

func TestPoller_Run_TicksUntilCancel(t *testing.T) {
	// Two ticks: first sees event 100 (PushEvent → skip), second
	// sees nothing (304 path is avoided here by serving [] on
	// subsequent calls — same as newFakeGitHub).
	srv := newFakeGitHub(`[{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}]`)
	defer srv.Close()

	cur := NewMemCursorStore()
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
	if saved.LastEventID != 100 {
		t.Errorf("LastEventID = %d, want 100 after first tick", saved.LastEventID)
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
