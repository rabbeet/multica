package telegramoutbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a thin Bot API wrapper for the outbound bridge. It exposes
// only the two methods the scheduler needs (SendMessage into a topic,
// CreateForumTopic when the topic mapping is missing) and classifies
// every response into a SendResult so the scheduler never has to parse
// Bot API error shapes itself.
//
// The client is deliberately independent of server/internal/cascade/notify:
// that package's TelegramChannel is single-chat-single-message and its
// error contract does not model 429 Retry-After or topic-scoped fatals.
// Sharing an HTTP transport across the two subsystems was considered
// and rejected — the coupling would force cascade rewrites for the
// PUL-479 error taxonomy with no user-observable benefit.
type Client struct {
	apiBaseURL string
	botToken   string
	httpClient *http.Client
}

// NewClient constructs a Client. apiBaseURL empty → public Bot API.
// httpClient nil → 15s per-request timeout (Bot API long calls like
// createForumTopic are typically <1s; 15s is comfortable head-room
// without blocking a scheduler tick for a full minute on a stalled
// TCP connection).
func NewClient(apiBaseURL, botToken string, httpClient *http.Client) (*Client, error) {
	if botToken == "" {
		return nil, ErrConfig
	}
	if apiBaseURL == "" {
		apiBaseURL = "https://api.telegram.org"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		botToken:   botToken,
		httpClient: httpClient,
	}, nil
}

// SendMessage posts plain-text (parse_mode="") to a topic. Plain text
// sidesteps MarkdownV2 escape hell for user-authored content that may
// contain code fences, mentions, or arbitrary punctuation — see the
// eng review in PUL-479 for the escape-hell rationale.
func (c *Client) SendMessage(ctx context.Context, chatID int64, messageThreadID int, text string) SendResult {
	body := map[string]any{
		"chat_id":           chatID,
		"message_thread_id": messageThreadID,
		"text":              text,
		// parse_mode intentionally omitted (Bot API interprets this as
		// plain text, TG auto-links bare URLs, and no character in
		// `text` is treated as markup).
		"disable_web_page_preview": true,
	}
	var resp struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	statusCode, err := c.do(ctx, "sendMessage", body, &resp)
	return classifyResponse(statusCode, err, resp.OK, resp.Description, resp.Parameters.RetryAfter, resp.Result.MessageID, 0)
}

// CreateForumTopic creates a new topic in the group. Bot must be a
// supergroup admin with can_manage_topics. Returns TopicID on success.
func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string) SendResult {
	body := map[string]any{
		"chat_id": chatID,
		// Forum topic titles are capped at 128 characters by Bot API.
		"name": truncateTopicName(name, 128),
	}
	var resp struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
		Result struct {
			MessageThreadID int `json:"message_thread_id"`
		} `json:"result"`
	}
	statusCode, err := c.do(ctx, "createForumTopic", body, &resp)
	return classifyResponse(statusCode, err, resp.OK, resp.Description, resp.Parameters.RetryAfter, 0, resp.Result.MessageThreadID)
}

// do POSTs a JSON body to /bot{token}/{method}, decodes into out.
// Returns the HTTP status code (0 on transport failure) and any
// transport error. Bot-level API errors (ok:false) are surfaced by the
// caller reading `out.OK` / `out.Description`.
func (c *Client) do(ctx context.Context, method string, body any, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("marshal %s body: %w", method, err)
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.apiBaseURL, url.PathEscape(c.botToken), method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Best-effort decode. Bot API always returns JSON on 2xx and 4xx;
	// on 5xx a non-JSON body is possible (nginx / cloudfront error
	// page) — swallow the decode error and let classifyResponse pick
	// OutcomeTransient based on statusCode alone.
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode, nil
}

// classifyResponse maps (statusCode, transport error, ok, description,
// retry_after, message_id, topic_id) into a SendResult. Split out so
// tests can exercise it directly without spinning an httptest server.
func classifyResponse(statusCode int, transportErr error, ok bool, description string, retryAfterSec, messageID, topicID int) SendResult {
	// Transport error trumps everything (context cancelled, DNS,
	// connection refused, TLS). Always transient — the message is
	// still in the outbox, retry on next tick.
	if transportErr != nil {
		return SendResult{
			Outcome:    OutcomeTransient,
			StatusCode: statusCode,
			Err:        transportErr,
		}
	}
	// 2xx + ok:true is the happy path.
	if ok && statusCode >= 200 && statusCode < 300 {
		return SendResult{
			Outcome:     OutcomeOK,
			StatusCode:  statusCode,
			MessageID:   messageID,
			TopicID:     topicID,
			Description: description,
		}
	}
	// 429 is separate from other 4xx: it's a throttle signal, not a
	// permanent failure. Retry-After is in seconds; treat 0 as 1s
	// (some Bot API responses set 0 with description "flood control").
	if statusCode == http.StatusTooManyRequests {
		wait := time.Duration(retryAfterSec) * time.Second
		if wait <= 0 {
			wait = time.Second
		}
		return SendResult{
			Outcome:     OutcomeRateLimit,
			StatusCode:  statusCode,
			RetryAfter:  wait,
			Description: description,
		}
	}
	// 5xx → transient. Bot API sometimes returns 502/504 during
	// deploys; the scheduler's exponential backoff waits it out.
	if statusCode >= 500 {
		return SendResult{
			Outcome:     OutcomeTransient,
			StatusCode:  statusCode,
			Description: description,
		}
	}
	// TOPIC_DELETED / TOPIC_NOT_MODIFIED / MESSAGE_THREAD_INVALID all
	// indicate the topic mapping is stale — signal a scheduler-side
	// cleanup + retry rather than a permanent fail.
	if isTopicDeletedDescription(description) {
		return SendResult{
			Outcome:     OutcomeTopicDeleted,
			StatusCode:  statusCode,
			Description: description,
		}
	}
	// Everything else 4xx: 401 bad token, 403 BOT_KICKED / BOT_BLOCKED,
	// 400 CHAT_NOT_FOUND / MESSAGE_TOO_LONG, 400 TOPICS_LIMIT_EXCEEDED
	// on createForumTopic. All fatal — no amount of retry recovers.
	return SendResult{
		Outcome:     OutcomeFatal,
		StatusCode:  statusCode,
		Description: description,
	}
}

// isTopicDeletedDescription checks Bot API description strings for the
// known topic-scoped stale-mapping signals. Case-insensitive since
// Bot API descriptions have inconsistent capitalization across API
// versions.
func isTopicDeletedDescription(desc string) bool {
	d := strings.ToLower(desc)
	// Known Bot API descriptions (as of Bot API 7.x). Adding new
	// patterns here is safe — the fallback is OutcomeFatal, which
	// gives operators a paper trail either way.
	patterns := []string{
		"topic_deleted",
		"topic_closed",
		"message_thread_not_found",
		"message thread not found",
		"topic is closed",
	}
	for _, p := range patterns {
		if strings.Contains(d, p) {
			return true
		}
	}
	return false
}

// truncateTopicName clamps the topic title to a UTF-8-safe max byte
// budget. Bot API enforces 128 chars (code points). Callers pass
// max=128; the extra parameter keeps the function testable at other
// widths.
func truncateTopicName(name string, max int) string {
	// Fast path: name is ASCII and shorter than max.
	if len(name) <= max {
		return name
	}
	// Walk runes; stop at max runes total. Elide with a single
	// ellipsis rune when we truncate.
	var b strings.Builder
	count := 0
	for _, r := range name {
		if count >= max-1 {
			break
		}
		b.WriteRune(r)
		count++
	}
	b.WriteRune('…')
	return b.String()
}
