// Package handler wires events from the Mattermost WS feed and the multica
// CLI together. Inbound: MM → multica (new issue or comment). Outbound:
// multica comment + status polling → MM thread posts (with screenshots).
//
// All filter chain enforcement and project pinning happens here; the rest
// of the daemon trusts the handler.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/multica-ai/multica/server/internal/mmbot/events"
	"github.com/multica-ai/multica/server/internal/mmbot/multicacli"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// MMPoster is the slice of rest.Client used by inbound (ack posts).
type MMPoster interface {
	CreatePost(ctx context.Context, req rest.PostRequest) (rest.Post, error)
}

// InboundConfig wires the inbound handler to its dependencies.
type InboundConfig struct {
	Store   *state.Store
	Multica MulticaInbound
	MM      MMPoster
	Logger  *slog.Logger

	// MulticaWebBase is used to build the [PUL-N] mention link in the ack
	// message (default "https://multica.ai").
	MulticaWebBase string

	// AllowedChannelIDs is the set of MM channels we listen to. Posts in
	// any other channel are silently dropped (defence in depth — the bot
	// account is invited to specific channels, so this is belt-and-
	// braces).
	AllowedChannelIDs map[string]struct{}

	// AllowedUserIDs whitelists which MM users may trigger the bot.
	// Membership in a watched channel is necessary but not sufficient —
	// the user_id must appear here too.
	AllowedUserIDs map[string]struct{}

	// BotUserID is the bot account's own user_id; we skip events authored
	// by it to avoid acting on our own ack messages.
	BotUserID string
}

// MulticaInbound is the narrowed interface inbound needs. Avoiding
// MulticaClient (which also has ListComments) keeps test fakes minimal.
type MulticaInbound interface {
	CreateIssue(ctx context.Context, req multicacli.CreateIssueRequest) (multicacli.Issue, error)
	AddComment(ctx context.Context, issueID, content string) (multicacli.Comment, error)
}

// Inbound forwards MM posts into multica.
type Inbound struct {
	cfg InboundConfig
}

// NewInbound constructs an Inbound. Validation is intentionally light —
// missing dependencies fail loud at handler invocation.
func NewInbound(cfg InboundConfig) *Inbound {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MulticaWebBase == "" {
		cfg.MulticaWebBase = "https://multica.ai"
	}
	return &Inbound{cfg: cfg}
}

// Handle decides what (if anything) to do with one MM `posted` event.
//
// Filter chain (in order):
//  1. channel_id in AllowedChannelIDs
//  2. user_id in AllowedUserIDs
//  3. user_id != BotUserID (anti-echo via author)
//  4. id not in state.mm_synced_posts (anti-replay during catch-up)
//
// Top-level posts → create multica issue; record thread mapping; ack in MM.
// Thread replies → look up multica issue from mapping; append comment.
func (h *Inbound) Handle(ctx context.Context, p events.Post) error {
	if _, ok := h.cfg.AllowedChannelIDs[p.ChannelID]; !ok {
		return nil
	}
	if _, ok := h.cfg.AllowedUserIDs[p.UserID]; !ok {
		h.cfg.Logger.Info("mmbot/handler: ignored unallowed user", "user_id", p.UserID, "channel_id", p.ChannelID)
		return nil
	}
	if p.UserID == h.cfg.BotUserID {
		return nil
	}
	seen, err := h.cfg.Store.IsSyncedPost(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("mmbot/handler: dedup lookup: %w", err)
	}
	if seen {
		return nil
	}

	if p.IsTopLevel() {
		return h.handleTopLevel(ctx, p)
	}
	return h.handleReply(ctx, p)
}

func (h *Inbound) handleTopLevel(ctx context.Context, p events.Post) error {
	title := strings.TrimSpace(p.Message)
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80])
	}
	desc := p.Message
	if p.Username != "" {
		desc = p.Message + "\n\n_From: @" + p.Username + "_"
	}

	issue, err := h.cfg.Multica.CreateIssue(ctx, multicacli.CreateIssueRequest{
		Title:       title,
		Description: desc,
	})
	if err != nil {
		return fmt.Errorf("mmbot/handler: create issue: %w", err)
	}

	if err := h.cfg.Store.RecordThread(ctx, state.Thread{
		RootPostID:     p.ID,
		ChannelID:      p.ChannelID,
		MulticaIssueID: issue.ID,
	}); err != nil {
		return fmt.Errorf("mmbot/handler: record thread: %w", err)
	}
	if err := h.cfg.Store.RecordSyncedPost(ctx, p.ID, "", state.DirectionMMToMulitca); err != nil {
		return fmt.Errorf("mmbot/handler: record synced post: %w", err)
	}

	ack := fmt.Sprintf("Создан [%s](%s/issues/%s)", issue.Identifier, h.cfg.MulticaWebBase, issue.Identifier)
	ackPost, err := h.cfg.MM.CreatePost(ctx, rest.PostRequest{
		ChannelID:      p.ChannelID,
		RootID:         p.ID,
		Message:        ack,
		AuthorOverride: "multica-bot",
	})
	if err != nil {
		// Non-fatal: the multica issue exists, the mapping is recorded.
		// Outbound polling will still flow agent updates back into the
		// MM thread once it gets created. We log and continue.
		h.cfg.Logger.Warn("mmbot/handler: ack post failed", "err", err, "issue", issue.Identifier)
		return nil
	}
	if err := h.cfg.Store.RecordSyncedPost(ctx, ackPost.ID, "", state.DirectionMulticaToMM); err != nil {
		h.cfg.Logger.Warn("mmbot/handler: record ack dedup", "err", err)
	}
	return nil
}

func (h *Inbound) handleReply(ctx context.Context, p events.Post) error {
	thread, err := h.cfg.Store.ThreadByRoot(ctx, p.RootID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			// Reply in a thread we don't own (created before bot started,
			// or a non-bot conversation that happened to include
			// allowlisted users). Stay silent.
			return nil
		}
		return fmt.Errorf("mmbot/handler: lookup thread: %w", err)
	}

	body := p.Message
	if p.Username != "" {
		body = p.Message + "\n\n_From: @" + p.Username + "_"
	}
	comment, err := h.cfg.Multica.AddComment(ctx, thread.MulticaIssueID, body)
	if err != nil {
		return fmt.Errorf("mmbot/handler: add comment: %w", err)
	}
	if err := h.cfg.Store.RecordSyncedPost(ctx, p.ID, comment.ID, state.DirectionMMToMulitca); err != nil {
		return fmt.Errorf("mmbot/handler: record synced reply: %w", err)
	}
	return nil
}
