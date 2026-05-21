// Package github used to host the inbound GitHub webhook adapter
// (PUL-102 / PR3 onwards). PUL-166 replaced that ingress with
// outbound polling; PR5 of that issue deleted the webhook adapter
// from this package, leaving only the GitHub REST API helpers
// (HTTPResolver, PRRef, PRResolver) that the polling classifier
// uses for the commit→PRs fallback.
//
// TODO(post-PR5): rename this package to internal/githubapi/. The
// import path is the only blocker for moving it cleanly; the code
// is no longer "webhooks"-specific.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PRRef is the minimal PR shape that resolver consumers
// (internal/githubpoll classifier, formerly the webhook adapter)
// need. Mirrors the fields workflow_run / check_run normally carry
// inline when GitHub populates pull_requests. PUL-166 PR5 moved
// this type here from the deleted source.go.
type PRRef struct {
	Number  int
	HTMLURL string
	Title   string
	Ref     string // head ref / branch name
}

// PRResolver resolves PR metadata for the cascade poller.
//
// LookupPRsByCommit returns the open PRs that contain a commit SHA.
// An empty slice means "no PRs match" (e.g. push on the default
// branch); an error means "lookup failed, try again later".
//
// LookupPRTitle returns the title of a single PR by number. Empty
// string + nil error means "no title available" (PR not visible,
// token lacks scope) — callers treat this identically to the
// REST /events strip case and fall through to the branch regex.
// Errors mean "transient lookup failure"; callers log + leave
// PRTitle empty so classification never blocks on title hydration.
// Added in PUL-216 to give the cascade classifier the title that
// REST /events strips from pull_request payloads.
type PRResolver interface {
	LookupPRsByCommit(ctx context.Context, repoFullName, headSHA string) ([]PRRef, error)
	LookupPRTitle(ctx context.Context, repoFullName string, prNumber int) (string, error)
}

// defaultAPIBaseURL is the public GitHub REST endpoint. Overridable
// on HTTPResolver for tests (httptest.Server) and self-hosted GHES.
const defaultAPIBaseURL = "https://api.github.com"

// defaultResolverTimeout caps the round trip on the fallback lookup.
// The webhook handler runs under a "p99 ≤ 1s" SLO; 3s leaves enough
// headroom for the rare slow API call while keeping us inside
// GitHub's 10s delivery timeout if the lookup is itself slow.
const defaultResolverTimeout = 3 * time.Second

// HTTPResolver looks up PRs for a commit via the GitHub REST API:
//
//	GET /repos/{owner}/{repo}/commits/{sha}/pulls
//
// Used by the workflow_run / check_run adapters as a fallback when
// the inbound payload arrives with an empty pull_requests array —
// GitHub frequently omits the array for same-repo PR runs even
// though the PR↔commit linkage is available through this endpoint.
type HTTPResolver struct {
	token   string
	baseURL string
	client  *http.Client
}

// NewHTTPResolver constructs a resolver authenticated with the given
// token. An empty token is permitted (anonymous calls subject to a
// 60/hour rate limit) but in practice always populated from
// MULTICA_GITHUB_API_TOKEN — the FromEnv wiring only constructs the
// resolver when the token is non-empty.
func NewHTTPResolver(token string) *HTTPResolver {
	return &HTTPResolver{
		token:   token,
		baseURL: defaultAPIBaseURL,
		client:  &http.Client{Timeout: defaultResolverTimeout},
	}
}

// WithBaseURL overrides the API base URL. Used by tests pointing at
// an httptest.Server and by self-hosted GHES deployments.
func (r *HTTPResolver) WithBaseURL(baseURL string) *HTTPResolver {
	r.baseURL = strings.TrimRight(baseURL, "/")
	return r
}

// WithHTTPClient overrides the HTTP client. Tests use this to inject
// a transport that observes outbound requests.
func (r *HTTPResolver) WithHTTPClient(c *http.Client) *HTTPResolver {
	r.client = c
	return r
}

// commitPullsResponse mirrors the subset of the GitHub commits/pulls
// response we read. The endpoint returns the full PR objects; we
// pick out the four fields the adapter then forwards to the cascade
// worker.
type commitPullsResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

// LookupPRsByCommit implements PRResolver. Returns the open PRs that
// contain the commit; an empty slice when GitHub knows of no matching
// PR (e.g. push on default branch). Closed PRs are filtered out — a
// CI-failure event for a closed PR isn't actionable for the cascade.
func (r *HTTPResolver) LookupPRsByCommit(ctx context.Context, repoFullName, headSHA string) ([]PRRef, error) {
	if repoFullName == "" || headSHA == "" {
		return nil, fmt.Errorf("github resolver: empty repo or sha")
	}

	// Path components are interpolated directly into the URL — repo
	// full names contain a "/" by design (owner/repo) and SHAs are
	// hex, neither needs query-escaping. Reject anything that looks
	// like an injection attempt to keep the constructed URL well-formed.
	if strings.ContainsAny(repoFullName, "?#") || strings.ContainsAny(headSHA, "?#/") {
		return nil, fmt.Errorf("github resolver: invalid repo or sha characters")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/commits/%s/pulls", r.baseURL, repoFullName, url.PathEscape(headSHA))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("github resolver: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github resolver: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// 404 means the commit isn't visible (private repo, wrong
		// token scope) or doesn't exist. Treat as "no PRs" rather
		// than an error so the adapter logs once and moves on.
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github resolver: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw []commitPullsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("github resolver: decode: %w", err)
	}

	out := make([]PRRef, 0, len(raw))
	for _, p := range raw {
		if !strings.EqualFold(p.State, "open") {
			continue
		}
		out = append(out, PRRef{
			Number:  p.Number,
			HTMLURL: p.HTMLURL,
			Title:   p.Title,
			Ref:     p.Head.Ref,
		})
	}
	return out, nil
}

// LookupPRTitle fetches the PR title via GET /repos/{owner}/{repo}/pulls/{n}.
//
// Used by the poll classifier (server/internal/githubpoll) to
// hydrate pr_title when REST /events strips pull_request.title
// from PR-related event payloads. PUL-216.
//
// Returns ("", nil) when GitHub answers 404 (PR not visible) or 403
// (token lacks pull_request:read scope). Both surface as "no title
// hint" upstream — the classifier leaves PRTitle empty and the
// cascade worker falls through to the branch regex. Other non-2xx
// responses surface as errors so transient API failures are
// visible in the warn-log.
func (r *HTTPResolver) LookupPRTitle(ctx context.Context, repoFullName string, prNumber int) (string, error) {
	if repoFullName == "" || prNumber <= 0 {
		return "", fmt.Errorf("github resolver: empty repo or pr number")
	}
	if strings.ContainsAny(repoFullName, "?#") {
		return "", fmt.Errorf("github resolver: invalid repo characters")
	}

	endpoint := fmt.Sprintf("%s/repos/%s/pulls/%d", r.baseURL, repoFullName, prNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("github resolver: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("github resolver: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("github resolver: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var raw struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", fmt.Errorf("github resolver: decode: %w", err)
	}
	return raw.Title, nil
}
