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
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 0)
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
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "W/\"abc\"", 0)
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

func TestClient_FetchEvents_FilterBySinceID(t *testing.T) {
	body := `[
		{"id":"300","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}},
		{"id":"200","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}},
		{"id":"100","type":"PushEvent","repo":{"name":"rabbeet/Pulse"},"payload":{}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := NewClient(NewStaticTokenSource("pat")).WithBaseURL(srv.URL)
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 150)
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
	// Two pages: page 1 has 100..199 newest first → reverse:
	// 100,101,...,199; page 2 has 0..99 newest first → 0,1,...,99.
	// sinceID=50 → caller gets 51..199 oldest first.
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
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 50)
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
	if _, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 0); err != nil {
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
	got, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 0)
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
	_, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 0)
	if err == nil {
		t.Fatal("expected error on 502")
	}
	if errors.Is(err, ErrNotModified) {
		t.Errorf("502 misclassified as ErrNotModified")
	}
}

func TestClient_FetchEvents_NoToken(t *testing.T) {
	c := NewClient(NewStaticTokenSource(""))
	_, err := c.FetchEvents(context.Background(), "rabbeet/Pulse", "", 0)
	if !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
}
