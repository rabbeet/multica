package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPResolver_OneOpenPR(t *testing.T) {
	var gotAuth, gotAccept, gotAPIVersion, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotAPIVersion = r.Header.Get("X-GitHub-Api-Version")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
            "number": 498,
            "html_url": "https://github.com/rabbeet/Pulse/pull/498",
            "title": "[PUL-143] cascade smoke",
            "state": "open",
            "head": {"ref": "agent-2/pul-143-cascade-smoke"}
        }]`))
	}))
	defer srv.Close()

	r := NewHTTPResolver("ghp_test").WithBaseURL(srv.URL)
	prs, err := r.LookupPRsByCommit(context.Background(), "rabbeet/Pulse", "d89b92ec")
	if err != nil {
		t.Fatalf("LookupPRsByCommit: %v", err)
	}
	if gotPath != "/repos/rabbeet/Pulse/commits/d89b92ec/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ghp_test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("accept = %q", gotAccept)
	}
	if gotAPIVersion != "2022-11-28" {
		t.Errorf("api version = %q", gotAPIVersion)
	}
	if len(prs) != 1 {
		t.Fatalf("len(prs) = %d, want 1", len(prs))
	}
	got := prs[0]
	if got.Number != 498 || got.HTMLURL != "https://github.com/rabbeet/Pulse/pull/498" {
		t.Errorf("unexpected pr: %+v", got)
	}
	if got.Title != "[PUL-143] cascade smoke" || got.Ref != "agent-2/pul-143-cascade-smoke" {
		t.Errorf("title/ref not parsed: %+v", got)
	}
}

func TestHTTPResolver_FiltersClosedPRs(t *testing.T) {
	// commits/{sha}/pulls returns both open AND closed PRs that
	// contain the commit. The cascade only cares about open ones —
	// a closed PR no longer needs a CI-failure wake-up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
            {"number": 1, "html_url": "u1", "state": "closed", "head": {"ref": "old"}},
            {"number": 2, "html_url": "u2", "state": "open",   "head": {"ref": "new"}}
        ]`))
	}))
	defer srv.Close()

	r := NewHTTPResolver("").WithBaseURL(srv.URL)
	prs, err := r.LookupPRsByCommit(context.Background(), "o/r", "sha")
	if err != nil {
		t.Fatalf("LookupPRsByCommit: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 2 {
		t.Fatalf("expected only open PR #2, got %+v", prs)
	}
}

func TestHTTPResolver_NoTokenSkipsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	r := NewHTTPResolver("").WithBaseURL(srv.URL)
	if _, err := r.LookupPRsByCommit(context.Background(), "o/r", "sha"); err != nil {
		t.Fatalf("LookupPRsByCommit: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization should be absent for unauth call, got %q", gotAuth)
	}
}

func TestHTTPResolver_404ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	r := NewHTTPResolver("t").WithBaseURL(srv.URL)
	prs, err := r.LookupPRsByCommit(context.Background(), "o/r", "sha")
	if err != nil {
		t.Fatalf("404 should be a soft empty, got err %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("expected empty slice, got %+v", prs)
	}
}

func TestHTTPResolver_5xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewHTTPResolver("t").WithBaseURL(srv.URL)
	_, err := r.LookupPRsByCommit(context.Background(), "o/r", "sha")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err should mention status code: %v", err)
	}
}

func TestHTTPResolver_RejectsBlankInputs(t *testing.T) {
	r := NewHTTPResolver("t")
	if _, err := r.LookupPRsByCommit(context.Background(), "", "sha"); err == nil {
		t.Error("expected error on empty repo")
	}
	if _, err := r.LookupPRsByCommit(context.Background(), "o/r", ""); err == nil {
		t.Error("expected error on empty sha")
	}
}

func TestHTTPResolver_RejectsInjectableInputs(t *testing.T) {
	r := NewHTTPResolver("t")
	for _, bad := range []string{"o/r?x=1", "o/r#frag"} {
		if _, err := r.LookupPRsByCommit(context.Background(), bad, "sha"); err == nil {
			t.Errorf("repo %q should be rejected", bad)
		}
	}
	for _, bad := range []string{"sha?x=1", "sha#frag", "sha/extra"} {
		if _, err := r.LookupPRsByCommit(context.Background(), "o/r", bad); err == nil {
			t.Errorf("sha %q should be rejected", bad)
		}
	}
}
