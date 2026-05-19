package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestClient_FetchEvents_Basic(t *testing.T) {
	// Two events on page 1, newest first. After reverse(), caller
	// gets them oldest-first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "W/\"abc\"")
		w.Header().Set("X-RateLimit-Remaining", "4998")
		w.Header().Set("X-RateLimit-Limit", "5000")
		page := r.URL.Query().Get("page")
		if page != "" && page != "1" {
			// Real GitHub returns [] when paging past the end.
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[
			{"id":"200","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}},
			{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}
		]`)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", nil)
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(got.Events))
	}
	// Oldest first now.
	if got.Events[0].ID != "100" {
		t.Errorf("Events[0].ID = %q, want 100 (oldest-first)", got.Events[0].ID)
	}
	if got.ETag != "W/\"abc\"" {
		t.Errorf("ETag = %q, want %q", got.ETag, "W/\"abc\"")
	}
	if got.RateRemaining != 4998 || got.RateLimit != 5000 {
		t.Errorf("rate = %d/%d, want 4998/5000", got.RateRemaining, got.RateLimit)
	}
}

func TestClient_FetchEvents_NotModified(t *testing.T) {
	// Conditional GET with matching ETag → 304. Caller persists
	// last_polled_at but leaves last_event_id alone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "W/\"abc\"" {
			t.Errorf("If-None-Match = %q, want %q", r.Header.Get("If-None-Match"), "W/\"abc\"")
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "W/\"abc\"", nil)
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("err = %v, want ErrNotModified", err)
	}
	if got.ETag != "W/\"abc\"" {
		t.Errorf("ETag = %q on 304, want previous %q", got.ETag, "W/\"abc\"")
	}
	if got.RateRemaining != 4999 {
		t.Errorf("RateRemaining = %d on 304, want passthrough 4999", got.RateRemaining)
	}
}

func TestClient_FetchEvents_FilterPerType(t *testing.T) {
	// Single-stream version of the filter test: every event is a
	// PushEvent, sinceByType maps "PushEvent" → 150, so 100 is
	// filtered and 200/300 pass. Same shape as the original
	// FilterBySinceID, just under the new per-type API.
	body := `[
		{"id":"300","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}},
		{"id":"200","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}},
		{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "",
		map[string]int64{"PushEvent": 150})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("len = %d, want 2 (ids 200, 300 above 150)", len(got.Events))
	}
	if got.Events[0].ID != "200" || got.Events[1].ID != "300" {
		t.Errorf("ids = [%s, %s], want [200, 300]", got.Events[0].ID, got.Events[1].ID)
	}
}

func TestClient_FetchEvents_Paginates(t *testing.T) {
	// Two pages of real content, then empty: page 1 has 100..199
	// newest first; page 2 has 0..99 newest first; page 3+ is empty
	// (real GitHub behavior when paging past the end). sinceByType
	// {PushEvent:50} → caller gets 51..199 oldest first.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		page := 1
		if v := r.URL.Query().Get("page"); v != "" {
			p, _ := strconv.Atoi(v)
			if p > 0 {
				page = p
			}
		}
		w.Header().Set("ETag", "W/\"p"+strconv.Itoa(page)+"\"")
		if page > 2 {
			fmt.Fprint(w, `[]`)
			return
		}
		evs := []map[string]any{}
		for i := 99; i >= 0; i-- {
			id := i + (page-1)*100 + (page-1)*0
			// page 1: ids 199..100 (newest first); page 2: ids 99..0.
			if page == 1 {
				id = 100 + i
			} else {
				id = i
			}
			evs = append(evs, map[string]any{
				"id":      strconv.Itoa(id),
				"type":    "PushEvent",
				"repo":    map[string]any{"name": "rabbeet/Pulse"},
				"payload": map[string]any{},
			})
		}
		b, _ := json.Marshal(evs)
		w.Write(b)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL).WithPagesLimit(3)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "",
		map[string]int64{"PushEvent": 50})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 149 {
		t.Errorf("len = %d, want 149 (ids 51..199)", len(got.Events))
	}
	if got.Events[0].ID != "51" {
		t.Errorf("first event id = %q, want 51", got.Events[0].ID)
	}
	if got.Events[len(got.Events)-1].ID != "199" {
		t.Errorf("last event id = %q, want 199", got.Events[len(got.Events)-1].ID)
	}
	if hits < 2 {
		t.Errorf("hits = %d, expected at least 2 pages", hits)
	}
}

func TestClient_FetchEvents_StopsAtPagesLimit(t *testing.T) {
	// Every page returns 100 events with progressively smaller ids
	// that never reach sinceID=0 — exposes the pagesLimit cap.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		evs := []map[string]any{}
		base := 9999 - (hits-1)*100
		for i := 0; i < 100; i++ {
			evs = append(evs, map[string]any{
				"id":      strconv.Itoa(base - i),
				"type":    "PushEvent",
				"repo":    map[string]any{"name": "rabbeet/Pulse"},
				"payload": map[string]any{},
			})
		}
		b, _ := json.Marshal(evs)
		w.Write(b)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL).WithPagesLimit(2)
	if _, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", nil); err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if hits != 2 {
		t.Errorf("hits = %d, want pagesLimit=2", hits)
	}
}

func TestClient_FetchEvents_RateLimit(t *testing.T) {
	reset := time.Now().Add(15 * time.Minute).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		http.Error(w, "API rate limit exceeded", http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", nil)
	var rate ErrRateLimited
	if !errors.As(err, &rate) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if rate.ResetAt.Unix() != reset {
		t.Errorf("ResetAt = %v, want %v", rate.ResetAt.Unix(), reset)
	}
	// Regression guard for review finding: rate info must propagate
	// to the FetchResult even on the ErrRateLimited path so the
	// poller's gauge drops to 0 and the alert fires. Without this,
	// when the budget hits zero the gauge stays at the last 200's
	// value and ops never sees the problem.
	if got.RateRemaining != 0 || got.RateLimit != 5000 {
		t.Errorf("FetchResult on 403 = remaining=%d limit=%d, want remaining=0 limit=5000",
			got.RateRemaining, got.RateLimit)
	}
}

func TestClient_FetchEvents_OtherErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	_, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", nil)
	if err == nil {
		t.Fatal("expected error on 502")
	}
	if errors.Is(err, ErrNotModified) {
		t.Errorf("502 misclassified as ErrNotModified")
	}
}

func TestClient_FetchEvents_NoToken(t *testing.T) {
	c := NewClient(NewStaticTokenSource(""))
	_, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", nil)
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
}

// TestClient_FetchEvents_PerTypeFilter_MixedStreams checks that
// per-type cursors work when events of different types arrive on the
// two distinct numeric-id streams GitHub exposes (PR-shaped events
// at ~9.6e9, Push-shaped at ~12e9). A PushEvent at 12e9 must be
// filtered against sinceByType["PushEvent"], not the PR cursor.
func TestClient_FetchEvents_PerTypeFilter_MixedStreams(t *testing.T) {
	body := `[
		{"id":"12078860926","type":"DeleteEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
		{"id":"12078860425","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
		{"id":"9648929307","type":"PullRequestEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
		{"id":"9648929100","type":"PullRequestEvent","repo":{"name":"rabbeet/multica"},"payload":{}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	// Push cursor: 12078860400 (between the two Push-shaped events on
	// the page → newer one passes, older one… well, only the newer is
	// in the body, so both Push-shaped events pass).
	// PR cursor: 9648929200 (between the two PR-shaped events → newer
	// 9648929307 passes, older 9648929100 does NOT).
	got, err := c.FetchEvents(context.Background(), "rabbeet/multica", "",
		map[string]int64{
			"PushEvent":        12078860400,
			"DeleteEvent":      12078860400,
			"PullRequestEvent": 9648929200,
		})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 3 {
		t.Fatalf("len = %d, want 3 (the older PR is filtered, others pass)", len(got.Events))
	}
	// Oldest first: 9648929307 (PR), 12078860425 (Push), 12078860926 (Delete)
	wantIDs := []string{"9648929307", "12078860425", "12078860926"}
	for i, want := range wantIDs {
		if got.Events[i].ID != want {
			t.Errorf("Events[%d].ID = %q, want %q", i, got.Events[i].ID, want)
		}
	}
}

// TestClient_FetchEvents_StaleOldTypeDoesNotBlockFreshNewType is the
// blocker-fix regression test for PUL-201. Pre-fix, the pagination
// loop set stop=true on the FIRST event with id ≤ sinceID. Under
// per-type cursors that shortcut is wrong: PR events (~9.6e9) and
// Push events (~12e9) live on different number lines and interleave
// by created_at, so encountering a stale PR mid-page would terminate
// the page-walk while fresh Push events further down the page (with
// id > Push cursor) should still be collected. The fix is the
// passed-count predicate: stop only if NO event on the page survived
// per-type filtering.
//
// Layout: page 1 contains [fresh Push 1, stale PR, fresh Push 2].
// Stale PR must not abort scanning; both Push events must come back.
func TestClient_FetchEvents_StaleOldTypeDoesNotBlockFreshNewType(t *testing.T) {
	body := `[
		{"id":"12078860926","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
		{"id":"9648929100","type":"PullRequestEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
		{"id":"12078860425","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/multica", "",
		map[string]int64{
			"PushEvent":        12078860400, // both Push events are > cursor
			"PullRequestEvent": 9648929999,  // stale PR is < cursor → filtered, not stop-signal
		})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("len = %d, want 2 (both Push events present; the old PR-event must NOT stop the scan)", len(got.Events))
	}
	// Oldest first: 12078860425, then 12078860926.
	if got.Events[0].ID != "12078860425" || got.Events[1].ID != "12078860926" {
		t.Errorf("ids = [%s, %s], want [12078860425, 12078860926]",
			got.Events[0].ID, got.Events[1].ID)
	}
	for _, e := range got.Events {
		if e.Type != "PushEvent" {
			t.Errorf("unexpected event type %q in result; only PushEvent should survive", e.Type)
		}
	}
}

// TestClient_FetchEvents_NewStopSemantics_StopsOnAllStalePage verifies
// the inverse case: when an entire page is stale for its types, the
// loop stops and does NOT request page 2. Combined with the previous
// test, this nails down the stop predicate (`passed == 0`).
func TestClient_FetchEvents_NewStopSemantics_StopsOnAllStalePage(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Page 1: all events ≤ their respective per-type cursors.
		// Page 2 (which we should never fetch): a fresh event.
		page := 1
		if v := r.URL.Query().Get("page"); v != "" {
			p, _ := strconv.Atoi(v)
			if p > 0 {
				page = p
			}
		}
		if page == 1 {
			fmt.Fprint(w, `[
				{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
				{"id":"50","type":"PullRequestEvent","repo":{"name":"rabbeet/multica"},"payload":{}}
			]`)
			return
		}
		fmt.Fprint(w, `[{"id":"9999","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}}]`)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/multica", "",
		map[string]int64{
			"PushEvent":        200,
			"PullRequestEvent": 100,
		})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(got.Events) != 0 {
		t.Errorf("len = %d, want 0 (all page-1 events stale)", len(got.Events))
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (must NOT page past an all-stale page 1)", hits)
	}
}

// TestClient_FetchEvents_PaginatesWhenAnyEventPasses guards the
// "passed > 0 → keep paginating" half of the new stop predicate.
// Page 1 has a single passing event among many stale ones; the loop
// MUST fetch page 2 (in case it has more fresh events of any type).
func TestClient_FetchEvents_PaginatesWhenAnyEventPasses(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		page := 1
		if v := r.URL.Query().Get("page"); v != "" {
			p, _ := strconv.Atoi(v)
			if p > 0 {
				page = p
			}
		}
		if page == 1 {
			fmt.Fprint(w, `[
				{"id":"500","type":"PushEvent","repo":{"name":"rabbeet/multica"},"payload":{}},
				{"id":"50","type":"PullRequestEvent","repo":{"name":"rabbeet/multica"},"payload":{}}
			]`)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL).WithPagesLimit(3)
	_, err := c.FetchEvents(context.Background(), "rabbeet/multica", "",
		map[string]int64{
			"PushEvent":        100, // page 1 push passes (500 > 100)
			"PullRequestEvent": 100, // page 1 PR stale
		})
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if hits < 2 {
		t.Errorf("hits = %d, want ≥2 (page 1 had a passing event, must continue)", hits)
	}
}
