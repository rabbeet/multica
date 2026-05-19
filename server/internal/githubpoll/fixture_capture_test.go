//go:build fixturecheck

package githubpoll

// Re-capture / drift-check harness for first_run_events_capture.json.
//
// Build tag `fixturecheck` keeps this OUT of the default `go test`
// run because it requires network access and a GITHUB_TOKEN — neither
// is available in CI. Operators run this quarterly or after any
// GitHub API version bump to keep the committed fixture aligned with
// what GitHub actually emits:
//
//	go test -tags=fixturecheck -run=Recapture -recapture ./server/internal/githubpoll
//	git diff server/internal/githubpoll/testdata/first_run_events_capture.json
//
// A non-empty diff signals GitHub /events shape drift since the last
// capture. Investigate before committing: the structural shift may
// invalidate assumptions in classify.go (see PUL-183, PUL-185).
//
// Without `-recapture`, TestRecaptureFirstRunFixture is a no-op so
// `go test -tags=fixturecheck ./...` does not blow up if invoked
// without intent.

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var recaptureFlag = flag.Bool("recapture", false,
	"re-fetch the /events fixture from GitHub and overwrite the committed file")

func TestRecaptureFirstRunFixture(t *testing.T) {
	if !*recaptureFlag {
		t.Skip("re-capture not requested; pass -recapture to overwrite the fixture")
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		// Fall back to `gh auth token` — matches the agent-runtime
		// setup where the PAT lives in `gh`'s config, not the env.
		out, err := exec.CommandContext(testContext(t), "gh", "auth", "token").Output()
		if err != nil {
			t.Fatalf("GITHUB_TOKEN unset and `gh auth token` failed: %v", err)
		}
		token = string(out)
		// Strip trailing newline that `gh` writes.
		for len(token) > 0 && (token[len(token)-1] == '\n' || token[len(token)-1] == '\r') {
			token = token[:len(token)-1]
		}
	}

	const apiURL = "https://api.github.com/repos/rabbeet/multica/events?per_page=100"
	req, err := http.NewRequestWithContext(testContext(t), http.MethodGet, apiURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/events returned status %d", resp.StatusCode)
	}

	var events []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatalf("decode /events: %v", err)
	}

	redactEvents(events)

	out, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out = append(out, '\n')

	path := filepath.Join("testdata", "first_run_events_capture.json")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %d events to %s — review the diff with `git diff %s`",
		len(events), path, path)
}

// redactEvents walks the parsed /events response and clears PII
// fields on actor and org blocks. Mirror of the one-off Python
// redaction used for the initial capture, so re-captures produce
// the same byte shape (modulo the events themselves).
func redactEvents(events []map[string]any) {
	const (
		placeholderLogin  = "redacted-user"
		placeholderGrav   = ""
		placeholderAvatar = "https://avatars.githubusercontent.com/u/0?v=4"
		placeholderURL    = "https://api.github.com/users/redacted-user"
	)
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			_, hasLogin := t["login"]
			_, hasGrav := t["gravatar_id"]
			_, hasAvatar := t["avatar_url"]
			if hasLogin && hasGrav && hasAvatar {
				t["login"] = placeholderLogin
				t["gravatar_id"] = placeholderGrav
				t["avatar_url"] = placeholderAvatar
				if _, ok := t["display_login"]; ok {
					t["display_login"] = placeholderLogin
				}
				if url, ok := t["url"].(string); ok && len(url) > 0 && url != placeholderURL {
					// Anonymize the per-user URL on actor blocks.
					if contains(url, "users/") {
						t["url"] = placeholderURL
					}
				}
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	for _, e := range events {
		walk(e)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
