// Package rest is the Mattermost REST API v4 client used by the
// multica-mattermost-bot daemon to post replies, upload screenshots, and
// catch up on missed posts after a WebSocket reconnect.
//
// All calls take a context and use exponential backoff on retryable errors
// (5xx + transient network failures). Caller is expected to enqueue
// PendingPosts in the state store on terminal failure.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
)

// DefaultHTTPTimeout caps an individual REST request. Callers can cancel
// earlier via context. 30s is generous for MM API which usually responds in
// <1s; the cap exists to prevent goroutine leaks if a backend hangs.
const DefaultHTTPTimeout = 30 * time.Second

// Retry defaults — matched to plan §"Resilience".
const (
	DefaultMaxAttempts = 6
	DefaultBaseDelay   = 1 * time.Second
	DefaultMaxDelay    = 60 * time.Second
)

// Errors returned by the client. Callers use errors.Is for classification.
var (
	// ErrUnauthorized indicates the bot token was rejected by Mattermost
	// (401/403). Non-retryable. Operator must rotate MM_BOT_TOKEN.
	ErrUnauthorized = errors.New("mmbot/rest: unauthorized (rotate MM_BOT_TOKEN)")

	// ErrBadRequest indicates the request itself was malformed (4xx other than
	// 401/403/429). Non-retryable; treat as a coding bug.
	ErrBadRequest = errors.New("mmbot/rest: bad request")

	// ErrTransient indicates a retryable failure (5xx, 429, network error).
	// The retry loop wraps these; callers should rarely see them directly.
	ErrTransient = errors.New("mmbot/rest: transient failure")
)

// Config bundles construction parameters. Zero-valued fields fall back to
// package defaults.
type Config struct {
	// BaseURL is the Mattermost host root, e.g. "https://mattermost.example.com".
	// No trailing /api/v4 — the client appends paths.
	BaseURL string
	// Token is the Personal Access Token of the bot account. Sent as
	// `Authorization: Bearer <token>` on every request.
	Token string
	// BotUserID lets handler code dedupe bot's own events; not strictly
	// needed for REST calls but stored alongside the rest of the bot
	// identity.
	BotUserID string
	// HTTPClient lets tests inject a custom transport (e.g. httptest server).
	// nil falls back to a client with DefaultHTTPTimeout.
	HTTPClient *http.Client
	// Logger for retry attempts and non-fatal failures. nil → slog.Default.
	Logger *slog.Logger
	// MaxAttempts caps retry tries. Zero → DefaultMaxAttempts.
	MaxAttempts int
	// BaseDelay starts the exponential backoff. Zero → DefaultBaseDelay.
	BaseDelay time.Duration
	// MaxDelay caps the backoff at the top of the exponential ladder.
	// Zero → DefaultMaxDelay.
	MaxDelay time.Duration
	// Sleep is the delay primitive used between retry attempts. Tests inject
	// a synchronous stub. nil → time.Sleep (interrupted by ctx).
	Sleep func(context.Context, time.Duration)
}

// Client posts to Mattermost via REST API v4 and recovers missed posts after
// disconnects. Safe for concurrent use.
type Client struct {
	cfg Config
}

// New constructs a Client. BaseURL and Token are required; everything else
// gets sensible defaults.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("mmbot/rest: BaseURL required")
	}
	if cfg.Token == "" {
		return nil, errors.New("mmbot/rest: Token required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = DefaultBaseDelay
	}
	if cfg.MaxDelay == 0 {
		cfg.MaxDelay = DefaultMaxDelay
	}
	if cfg.Sleep == nil {
		cfg.Sleep = defaultSleep
	}
	return &Client{cfg: cfg}, nil
}

// Post mirrors the Mattermost API "post" object. Only the fields the bot
// needs are populated; everything else is silently dropped at JSON-decode time.
type Post struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	RootID    string `json:"root_id"`
	ChannelID string `json:"channel_id"`
	Message   string `json:"message"`
	// CreateAt is milliseconds since epoch — MM API native format.
	CreateAt int64 `json:"create_at"`
}

// CreatedAt returns CreateAt as a Go time. Convenience for handler code that
// needs to update state.MetaLastSeenMMTS.
func (p Post) CreatedAt() time.Time { return time.UnixMilli(p.CreateAt) }

// PostRequest is the bot's outbound message payload.
type PostRequest struct {
	ChannelID string
	// RootID is the MM root_post_id of the thread we are replying into.
	// Empty means a new top-level post (rare for the bot — it usually only
	// posts inside existing threads).
	RootID string
	// Message is the plain-text body of the post. May be empty when only an
	// AuthorOverride attachment is being sent.
	Message string
	// FileIDs from a prior UploadFile call.
	FileIDs []string
	// AuthorOverride sets props.attachments[0].author_name so the post
	// appears in the MM thread as a different author (e.g.
	// "agent-1 ↪ marimo-pair" for forwarded agent comments,
	// finding #8 of /plan-eng-review).
	//
	// When non-empty: Message goes into the attachment's text field, and
	// the top-level Message field is left empty.
	AuthorOverride string
}

// CreatePost sends one message into a Mattermost channel/thread. Returns the
// created Post (so callers can record the mm_post_id in
// mm_synced_posts(direction=multica_to_mm) for echo dedup — finding #7).
func (c *Client) CreatePost(ctx context.Context, req PostRequest) (Post, error) {
	if req.ChannelID == "" {
		return Post{}, errors.New("mmbot/rest: ChannelID required")
	}

	body := map[string]any{
		"channel_id": req.ChannelID,
	}
	if req.RootID != "" {
		body["root_id"] = req.RootID
	}
	if len(req.FileIDs) > 0 {
		body["file_ids"] = req.FileIDs
	}
	if req.AuthorOverride != "" {
		body["message"] = ""
		body["props"] = map[string]any{
			"attachments": []map[string]any{{
				"author_name": req.AuthorOverride,
				"text":        req.Message,
			}},
		}
	} else {
		body["message"] = req.Message
	}

	var out Post
	err := c.retryJSON(ctx, http.MethodPost, "/api/v4/posts", body, &out)
	if err != nil {
		return Post{}, err
	}
	return out, nil
}

// UploadFile uploads bytes to Mattermost and returns the resulting file_id,
// which can be passed to CreatePost.FileIDs.
//
// The MM endpoint is `POST /api/v4/files?channel_id=...` with multipart
// body. Returns the first file id from the response.
func (c *Client) UploadFile(ctx context.Context, channelID, filename string, data io.Reader) (string, error) {
	if channelID == "" {
		return "", errors.New("mmbot/rest: channelID required")
	}
	if filename == "" {
		filename = "attachment.bin"
	}

	// Drain into a buffer once so the retry loop can re-issue the request
	// without re-reading from a non-seekable source. Files we upload are
	// PNGs in the low-MB range; cost is acceptable.
	raw, err := io.ReadAll(data)
	if err != nil {
		return "", fmt.Errorf("mmbot/rest: read upload body: %w", err)
	}

	endpoint := "/api/v4/files?" + url.Values{"channel_id": {channelID}, "filename": {filepath.Base(filename)}}.Encode()

	var resp struct {
		FileInfos []struct {
			ID string `json:"id"`
		} `json:"file_infos"`
	}

	err = c.retry(ctx, func() (int, []byte, error) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("files", filepath.Base(filename))
		if err != nil {
			return 0, nil, fmt.Errorf("multipart form-file: %w", err)
		}
		if _, err := fw.Write(raw); err != nil {
			return 0, nil, fmt.Errorf("multipart write: %w", err)
		}
		if err := mw.Close(); err != nil {
			return 0, nil, fmt.Errorf("multipart close: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+endpoint, &buf)
		if err != nil {
			return 0, nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		httpReq.Header.Set("Content-Type", mw.FormDataContentType())

		httpResp, err := c.cfg.HTTPClient.Do(httpReq)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: %w", ErrTransient, err)
		}
		defer httpResp.Body.Close()
		respBody, _ := io.ReadAll(httpResp.Body)
		return httpResp.StatusCode, respBody, nil
	}, &resp)
	if err != nil {
		return "", err
	}
	if len(resp.FileInfos) == 0 {
		return "", errors.New("mmbot/rest: upload returned no file_infos")
	}
	return resp.FileInfos[0].ID, nil
}

// PostsAfter fetches all posts in a channel created strictly after sinceMs
// (milliseconds since epoch). Used by the WS reconnect catch-up loop to drain
// posts that arrived during the disconnect.
//
// Iterates MM API pagination (60 posts/page) until exhausted. Posts are
// returned in ascending CreateAt order regardless of MM API page order.
func (c *Client) PostsAfter(ctx context.Context, channelID string, sinceMs int64) ([]Post, error) {
	if channelID == "" {
		return nil, errors.New("mmbot/rest: channelID required")
	}

	type pagePayload struct {
		Order []string        `json:"order"`
		Posts map[string]Post `json:"posts"`
	}

	page := 0
	const perPage = 60
	var collected []Post
	seen := map[string]struct{}{}

	for {
		q := url.Values{
			"since":     {strconv.FormatInt(sinceMs, 10)},
			"page":      {strconv.Itoa(page)},
			"per_page":  {strconv.Itoa(perPage)},
		}
		endpoint := fmt.Sprintf("/api/v4/channels/%s/posts?%s", url.PathEscape(channelID), q.Encode())

		var raw pagePayload
		if err := c.retryJSON(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
			return nil, err
		}
		if len(raw.Posts) == 0 {
			break
		}
		// MM returns posts in newest-first order within a page; we want
		// ascending so the handler can apply them sequentially without
		// reordering events on the multica side.
		newOnThisPage := 0
		for _, post := range raw.Posts {
			if _, dup := seen[post.ID]; dup {
				continue
			}
			if post.CreateAt <= sinceMs {
				// Defensive: MM occasionally returns the sinceMs anchor
				// itself; we exclude it strictly to avoid replay loops.
				continue
			}
			seen[post.ID] = struct{}{}
			collected = append(collected, post)
			newOnThisPage++
		}
		if newOnThisPage == 0 {
			// Page returned only duplicates / pre-cursor posts. End of
			// fresh data — stop pagination even if order had entries.
			break
		}
		if len(raw.Order) < perPage {
			break
		}
		page++
	}

	// Sort ascending by CreateAt. Tiny slices, stable insertion is fine.
	for i := 1; i < len(collected); i++ {
		for j := i; j > 0 && collected[j-1].CreateAt > collected[j].CreateAt; j-- {
			collected[j-1], collected[j] = collected[j], collected[j-1]
		}
	}
	return collected, nil
}

// retryJSON does JSON-in/JSON-out with retry. Body may be nil for GETs.
func (c *Client) retryJSON(ctx context.Context, method, path string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("mmbot/rest: marshal: %w", err)
		}
		bodyBytes = b
	}

	return c.retry(ctx, func() (int, []byte, error) {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
		if err != nil {
			return 0, nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.cfg.HTTPClient.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: %w", ErrTransient, err)
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, respBody, nil
	}, out)
}

// retry runs `do` up to MaxAttempts times with exponential backoff, parsing
// the response body into `out` (which may be nil to ignore). Classifies
// status codes into terminal vs retryable.
func (c *Client) retry(ctx context.Context, do func() (int, []byte, error), out any) error {
	delay := c.cfg.BaseDelay
	var lastErr error
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		status, body, err := do()
		if err != nil && errors.Is(err, ErrTransient) {
			lastErr = err
			c.cfg.Logger.Warn("mmbot/rest: transient failure", "attempt", attempt, "err", err)
			if attempt < c.cfg.MaxAttempts {
				c.cfg.Sleep(ctx, delay)
				delay = nextDelay(delay, c.cfg.MaxDelay)
			}
			continue
		}
		if err != nil {
			// Non-transient build error.
			return err
		}
		switch {
		case status >= 200 && status < 300:
			if out != nil {
				if err := json.Unmarshal(body, out); err != nil {
					return fmt.Errorf("mmbot/rest: decode response: %w (body=%q)", err, truncate(body, 200))
				}
			}
			return nil
		case status == 401 || status == 403:
			return fmt.Errorf("%w: status=%d body=%q", ErrUnauthorized, status, truncate(body, 200))
		case status == 429 || status >= 500:
			lastErr = fmt.Errorf("%w: status=%d body=%q", ErrTransient, status, truncate(body, 200))
			c.cfg.Logger.Warn("mmbot/rest: retryable status", "attempt", attempt, "status", status)
			if attempt < c.cfg.MaxAttempts {
				c.cfg.Sleep(ctx, delay)
				delay = nextDelay(delay, c.cfg.MaxDelay)
			}
			continue
		default:
			return fmt.Errorf("%w: status=%d body=%q", ErrBadRequest, status, truncate(body, 200))
		}
	}
	if lastErr == nil {
		lastErr = ErrTransient
	}
	return fmt.Errorf("mmbot/rest: exhausted %d attempts: %w", c.cfg.MaxAttempts, lastErr)
}

func nextDelay(current, cap_ time.Duration) time.Duration {
	next := current * 2
	if next > cap_ {
		return cap_
	}
	return next
}

// defaultSleep blocks for d unless ctx is cancelled first.
func defaultSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
