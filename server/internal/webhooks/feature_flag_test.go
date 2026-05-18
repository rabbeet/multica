package webhooks

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMountFromEnv_DisabledByDefault(t *testing.T) {
	// Make sure the env is clean. Other tests in the binary may have
	// set it; t.Setenv unsets at end automatically.
	t.Setenv(FeatureFlagEnvVar, "")

	mux := chi.NewRouter()
	router := MountFromEnv(mux, MountOptions{}, nil)
	if router != nil {
		t.Fatalf("MountFromEnv with flag unset returned non-nil router")
	}

	// Route should literally not exist on the parent mux.
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("with flag off, /webhooks/github should 404, got %d", rec.Code)
	}
}

func TestMountFromEnv_EnabledRegistersStubs(t *testing.T) {
	t.Setenv(FeatureFlagEnvVar, "true")

	mux := chi.NewRouter()
	router := MountFromEnv(mux, MountOptions{}, nil)
	if router == nil {
		t.Fatalf("MountFromEnv with flag=true returned nil router")
	}
	// PUL-166 PR5: github source removed from this router. Source
	// count is the three placeholder vendor stubs only; GitHub
	// ingress is now outbound polling (internal/githubpoll).
	if got := router.SourceCount(); got != 3 {
		t.Fatalf("source count = %d, want 3 (linear, slack, gitlab)", got)
	}

	// /webhooks/github should 404 — no adapter registered.
	t.Run("github_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/webhooks/github should 404 after PR5 cleanup, got %d", rec.Code)
		}
	})

	// Each remaining stub should be reachable and respond 204.
	for _, name := range []string{"linear", "slack", "gitlab"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks/"+name, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("/webhooks/%s with stub adapter should 204, got %d", name, rec.Code)
			}
		})
	}
}

func TestEnvEnabled_TruthValues(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"off", false},
		{"ture", false}, // typo — must not pass
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"  true  ", true}, // whitespace tolerated
		{"yes", true},
		{"on", true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv(FeatureFlagEnvVar, tc.raw)
			if got := envEnabled(); got != tc.want {
				t.Errorf("envEnabled(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
