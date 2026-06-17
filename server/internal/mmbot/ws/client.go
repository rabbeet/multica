// Package ws is the Mattermost WebSocket client. It maintains a single
// long-lived connection with heartbeat detection, exponential-backoff
// reconnect, and a REST catch-up window so messages posted during a
// disconnect are NOT lost (finding #5 of /plan-eng-review).
//
// Each `posted` event is decoded via the events package and handed off to
// the supplied Handler. The Run loop owns the connection lifecycle; callers
// just cancel ctx to shut down.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package ws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/mmbot/events"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// Defaults match plan §"Resilience".
const (
	DefaultHeartbeat   = 30 * time.Second
	DefaultPongTimeout = 30 * time.Second
	DefaultBaseDelay   = 1 * time.Second
	DefaultMaxDelay    = 60 * time.Second
)

// Handler is the callback Run invokes for every `posted` event after
// filter chain in the inbound handler will reject events the bot shouldn't
// act on.
type Handler interface {
	Handle(ctx context.Context, p events.Post) error
}

// CatchupPoster is what Run calls to drain REST-side posts the WS feed
// missed during disconnect. Implemented by rest.Client.
type CatchupPoster interface {
	PostsAfter(ctx context.Context, channelID string, sinceMs int64) ([]rest.Post, error)
}

// Config wires the client to the rest of the daemon.
type Config struct {
	BaseURL        string   // "https://mattermost.example.com" — wss derived from this
	Token          string
	WatchChannelIDs []string

	Handler Handler
	Catchup CatchupPoster
	Store   *state.Store

	Logger *slog.Logger

	HeartbeatInterval time.Duration
	PongTimeout       time.Duration
	BaseDelay         time.Duration
	MaxDelay          time.Duration

	// Dialer is exposed so tests can inject a fake. nil → default
	// websocket.DefaultDialer.
	Dialer *websocket.Dialer
	// Sleep is the delay primitive between reconnect attempts. Tests
	// inject a synchronous stub. nil → time-aware default.
	Sleep func(context.Context, time.Duration)
}

// Client is the WebSocket lifecycle owner.
type Client struct {
	cfg Config
}

// New constructs a Client. Validation is intentionally light so tests can
// build no-op clients; the daemon validates required fields at startup.
func New(cfg Config) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHeartbeat
	}
	if cfg.PongTimeout == 0 {
		cfg.PongTimeout = DefaultPongTimeout
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = DefaultBaseDelay
	}
	if cfg.MaxDelay == 0 {
		cfg.MaxDelay = DefaultMaxDelay
	}
	if cfg.Dialer == nil {
		cfg.Dialer = websocket.DefaultDialer
	}
	if cfg.Sleep == nil {
		cfg.Sleep = defaultSleep
	}
	return &Client{cfg: cfg}
}

// Run blocks until ctx is cancelled. The loop:
//  1. catches up via REST for every watched channel
//  2. dials wss:// and processes events
//  3. on disconnect, exp-backoff sleeps and loops to (1)
func (c *Client) Run(ctx context.Context) error {
	delay := c.cfg.BaseDelay
	failedReconnects := 0
	const escalateAfter = 10

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Always catch up FIRST so cold starts and post-disconnect runs
		// pick up messages that landed during downtime (finding #5).
		if err := c.catchUp(ctx); err != nil {
			c.cfg.Logger.Warn("mmbot/ws: catch-up failed", "err", err)
			// Catch-up errors should not block reconnect; the next
			// cycle will retry.
		}

		err := c.dialAndServe(ctx)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if err == nil {
			// Clean shutdown of inner loop (rare) — keep reconnecting.
			delay = c.cfg.BaseDelay
			failedReconnects = 0
			continue
		}
		failedReconnects++
		c.cfg.Logger.Warn("mmbot/ws: connection broken", "err", err, "fail_count", failedReconnects, "next_delay", delay)
		if failedReconnects >= escalateAfter {
			c.cfg.Logger.Error("mmbot/ws: persistent reconnect failure — operator should investigate", "fail_count", failedReconnects)
			// Reset the counter so escalation isn't logged every cycle;
			// the operator-facing escalation (multica issue create
			// --label pulse-alert) is wired in Lane E.
			failedReconnects = 0
		}
		c.cfg.Sleep(ctx, delay)
		delay = nextDelay(delay, c.cfg.MaxDelay)
		// On reconnect, reset the success-side delay.
		if failedReconnects == 0 {
			delay = c.cfg.BaseDelay
		}
	}
}

// catchUp pulls posts created since the high-water mark for each watched
// channel and feeds each one through Handler. Updates last_seen_mm_ts after
// every successful channel.
func (c *Client) catchUp(ctx context.Context) error {
	if c.cfg.Catchup == nil {
		return nil
	}
	sinceStr, _ := c.cfg.Store.GetMeta(ctx, state.MetaLastSeenMMTS)
	var sinceMs int64
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			sinceMs = t.UnixMilli()
		}
	}
	var maxSeen int64 = sinceMs
	for _, ch := range c.cfg.WatchChannelIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		posts, err := c.cfg.Catchup.PostsAfter(ctx, ch, sinceMs)
		if err != nil {
			return fmt.Errorf("catch-up %s: %w", ch, err)
		}
		for _, p := range posts {
			ev := events.Post{
				ID:        p.ID,
				UserID:    p.UserID,
				ChannelID: p.ChannelID,
				RootID:    p.RootID,
				Message:   p.Message,
				CreateAt:  p.CreateAt,
			}
			if err := c.cfg.Handler.Handle(ctx, ev); err != nil {
				c.cfg.Logger.Warn("mmbot/ws: catch-up handler error", "err", err, "post_id", p.ID)
			}
			if p.CreateAt > maxSeen {
				maxSeen = p.CreateAt
			}
		}
	}
	if maxSeen > sinceMs {
		ts := time.UnixMilli(maxSeen).UTC().Format(time.RFC3339)
		if err := c.cfg.Store.SetMeta(ctx, state.MetaLastSeenMMTS, ts); err != nil {
			c.cfg.Logger.Warn("mmbot/ws: update last_seen_mm_ts", "err", err)
		}
	}
	return nil
}

// dialAndServe connects and processes events until the connection breaks
// or ctx is cancelled. Returns nil on clean ctx cancellation, error
// otherwise.
func (c *Client) dialAndServe(ctx context.Context) error {
	wsURL, err := buildWSURL(c.cfg.BaseURL)
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+c.cfg.Token)
	conn, _, err := c.cfg.Dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Mattermost uses control-frame pongs; we set a deadline that the
	// pong handler refreshes. Without pong inside PongTimeout the next
	// read returns an error and we break out.
	conn.SetReadDeadline(time.Now().Add(c.cfg.HeartbeatInterval + c.cfg.PongTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.cfg.HeartbeatInterval + c.cfg.PongTimeout))
	})

	// Heartbeat goroutine.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go c.runHeartbeat(heartbeatCtx, conn)

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read: %w", err)
		}
		p, err := events.DecodePosted(payload)
		if errors.Is(err, events.ErrNotPosted) {
			continue
		}
		if err != nil {
			c.cfg.Logger.Warn("mmbot/ws: decode error", "err", err)
			continue
		}
		if err := c.cfg.Handler.Handle(ctx, p); err != nil {
			c.cfg.Logger.Warn("mmbot/ws: handler error", "err", err, "post_id", p.ID)
		}
		if p.CreateAt > 0 {
			ts := time.UnixMilli(p.CreateAt).UTC().Format(time.RFC3339)
			_ = c.cfg.Store.SetMeta(ctx, state.MetaLastSeenMMTS, ts)
		}
	}
}

func (c *Client) runHeartbeat(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(c.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			deadline := time.Now().Add(c.cfg.PongTimeout)
			if err := conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
				c.cfg.Logger.Warn("mmbot/ws: ping write failed", "err", err)
				_ = conn.Close()
				return
			}
		}
	}
}

// buildWSURL turns "https://mattermost.example.com" into
// "wss://mattermost.example.com/api/v4/websocket".
func buildWSURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = "/api/v4/websocket"
	return u.String(), nil
}

func nextDelay(current, cap_ time.Duration) time.Duration {
	next := current * 2
	if next > cap_ {
		return cap_
	}
	return next
}

func defaultSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
