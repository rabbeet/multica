package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultAPIBaseURL is the public REST endpoint. Override via
// Client.WithBaseURL for tests (httptest.Server) and self-hosted
// GHES. Mirrors the constant in webhooks/github/resolver.go — kept
// here so the poll path is self-contained.
const defaultAPIBaseURL = "https://api.github.com"

// defaultClientTimeout caps any single REST call. 10s is the upper
// bound GitHub itself uses on webhook deliveries; the poller can
// afford the same. Per-tick, the supervisor short-circuits at
// context.Done before the next tick fires.
const defaultClientTimeout = 10 * time.Second

// defaultPagesLimit caps the pagination depth per tick. /events
// hard-limits to 30 pages × 100 items = 3000 events of history,
// but a poller running at 30s intervals never needs more than the
// first 1-2 pages in steady state. The cap is observability — if
// we ever paginate past it, an alert is louder than infinite walk.
const defaultPagesLimit = 5

// ErrNotModified is returned by FetchEvents when the response was
// 304 — nothing changed since last poll's ETag. The caller updates
// last_polled_at but leaves last_event_id and etag untouched.
var ErrNotModified = errors.New("githubpoll: 304 not modified")

// ErrRateLimited is returned when GitHub responds 403 with
// X-RateLimit-Remaining=0. The wrapped time is the X-RateLimit-Reset
// value — caller sleeps until then before retrying.
type ErrRateLimited struct {
	ResetAt time.Time
}

func (e ErrRateLimited) Error() string {
	return fmt.Sprintf("githubpoll: rate-limited until %s", e.ResetAt.Format(time.RFC3339))
}

// FetchResult is the outcome of one /events tick for one repo.
// Events is the slice of items with id > sinceID, ordered oldest
// first (caller-facing — we reverse GitHub's newest-first order so
// downstream sees chronological events). ETag is the new value to
// persist via CursorStore.Save. RateRemaining is the observed budget
// for the metric/alert path.
type FetchResult struct {
	Events        []Event
	ETag          string
	RateRemaining int
	RateLimit     int
}

// Client is the REST wrapper. Sends Authorization Bearer + GitHub
// API headers + If-None-Match for conditional GETs. Concurrency-safe
// after construction; the poller shares one Client across all
// per-repo goroutines.
type Client struct {
	tokenSource TokenSource
	baseURL     string
	httpClient  *http.Client
	pagesLimit  int
}

// NewClient constructs a Client. nil tokenSource is permitted at
// construction time (panics deferred until first Token call) so
// tests can swap auth schemes; production wires EnvPATSource.
func NewClient(tokenSource TokenSource) *Client {
	return &Client{
		tokenSource: tokenSource,
		baseURL:     defaultAPIBaseURL,
		httpClient:  &http.Client{Timeout: defaultClientTimeout},
		pagesLimit:  defaultPagesLimit,
	}
}

// WithBaseURL overrides the base URL. Used by tests pointing at an
// httptest.Server and by self-hosted GHES deployments.
func (c *Client) WithBaseURL(baseURL string) *Client {
	c.baseURL = strings.TrimRight(baseURL, "/")
	return c
}

// WithHTTPClient overrides the http.Client. Tests use this to plug
// in a custom transport that observes outbound requests.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.httpClient = h
	return c
}

// WithPagesLimit overrides the per-tick pagination depth cap.
// Default 5. 0 or negative resets to default.
func (c *Client) WithPagesLimit(n int) *Client {
	if n <= 0 {
		n = defaultPagesLimit
	}
	c.pagesLimit = n
	return c
}

// FetchEvents calls /repos/{owner}/{repo}/events with conditional
// headers. Returns ErrNotModified on 304, ErrRateLimited{ResetAt:…}
// on 403+remaining=0, and a plain error otherwise.
//
// Events are filtered with id > sinceID and returned oldest-first
// (caller-facing chronological order). Pagination walks while the
// last item on a page has id > sinceID; capped at pagesLimit pages
// (configured via WithPagesLimit, default 5).
//
// The (etagPrev, sinceID) pair is the per-repo cursor state. Pass
// the empty string + 0 on a first poll — server returns 200 with
// full body.
func (c *Client) FetchEvents(ctx context.Context, repo, etagPrev string, sinceID int64) (FetchResult, error) {
	token, _, err := c.tokenSource.Token(ctx)
	if err != nil {
		return FetchResult{}, fmt.Errorf("githubpoll.client: token: %w", err)
	}

	collected := make([]Event, 0, 100)
	var newETag string
	var rateRemaining, rateLimit int

	for page := 1; page <= c.pagesLimit; page++ {
		batch, etag, info, err := c.fetchOnePage(ctx, repo, token, etagPrev, page)
		if err != nil {
			// 304 may surface on page 1 only — the ETag governs the
			// whole resource. On subsequent pages we never send
			// If-None-Match, so 304 there would be a server bug.
			if errors.Is(err, ErrNotModified) {
				if page == 1 {
					return FetchResult{ETag: etagPrev, RateRemaining: info.remaining, RateLimit: info.limit}, ErrNotModified
				}
				return FetchResult{}, fmt.Errorf("unexpected 304 on page %d", page)
			}
			return FetchResult{}, err
		}
		// Stop sending If-None-Match after page 1.
		etagPrev = ""

		if page == 1 {
			newETag = etag
		}
		rateRemaining, rateLimit = info.remaining, info.limit

		var stop bool
		for _, e := range batch {
			id, err := e.NumericID()
			if err != nil {
				// Schema mismatch on a single event id — log + skip.
				// We don't bail the whole tick: the classifier sees
				// the same id and produces ErrSchemaMismatch then,
				// which is the place to surface it as a metric.
				continue
			}
			if id <= sinceID {
				stop = true
				continue
			}
			collected = append(collected, e)
		}
		if stop {
			break
		}
		if len(batch) == 0 {
			break
		}
	}

	// GitHub returns events newest first; reverse to oldest first
	// so downstream classification and cursor advancement happen
	// in chronological order.
	reverseEvents(collected)

	return FetchResult{
		Events:        collected,
		ETag:          newETag,
		RateRemaining: rateRemaining,
		RateLimit:     rateLimit,
	}, nil
}

type rateInfo struct {
	remaining int
	limit     int
}

func (c *Client) fetchOnePage(ctx context.Context, repo, token, etagPrev string, page int) ([]Event, string, rateInfo, error) {
	endpoint, err := c.buildEventsURL(repo, page)
	if err != nil {
		return nil, "", rateInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", rateInfo{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)
	if etagPrev != "" {
		req.Header.Set("If-None-Match", etagPrev)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", rateInfo{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	info := readRateInfo(resp.Header)

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, etagPrev, info, ErrNotModified
	case resp.StatusCode == http.StatusForbidden && info.remaining == 0:
		// GitHub uses 403 (not 429) for rate-limit-by-budget. Read
		// X-RateLimit-Reset for the unix-second wakeup. The caller
		// is responsible for sleeping; this path just propagates.
		return nil, "", info, ErrRateLimited{ResetAt: readRateReset(resp.Header)}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", info, fmt.Errorf("github events: status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, resp.Header.Get("ETag"), info, nil
		}
		return nil, "", info, fmt.Errorf("decode: %w", err)
	}
	return events, resp.Header.Get("ETag"), info, nil
}

func (c *Client) buildEventsURL(repo string, page int) (string, error) {
	if strings.ContainsAny(repo, "?#") {
		return "", fmt.Errorf("invalid repo characters")
	}
	// Repo full name contains a "/" by design; PathEscape would
	// double-encode it.
	u := c.baseURL + "/repos/" + repo + "/events"
	q := url.Values{}
	q.Set("per_page", "100")
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	u += "?" + q.Encode()
	return u, nil
}

func readRateInfo(h http.Header) rateInfo {
	rem, _ := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	lim, _ := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	return rateInfo{remaining: rem, limit: lim}
}

func readRateReset(h http.Header) time.Time {
	v := h.Get("X-RateLimit-Reset")
	if v == "" {
		// Unknown reset — give caller a far-future deadline so the
		// supervisor's normal backoff handles retry timing.
		return time.Now().Add(15 * time.Minute)
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Now().Add(15 * time.Minute)
	}
	return time.Unix(sec, 0)
}

func reverseEvents(s []Event) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
