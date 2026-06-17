package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/mmbot/multicacli"
	"github.com/multica-ai/multica/server/internal/mmbot/render"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// MulticaPoller is the multicacli surface the outbound poller uses.
type MulticaPoller interface {
	GetIssue(ctx context.Context, issueID string) (multicacli.Issue, error)
	ListComments(ctx context.Context, issueID string, since time.Time) ([]multicacli.Comment, error)
}

// MMUploader exposes the rest.Client operations the outbound handler needs:
// posting messages and uploading PNG attachments.
type MMUploader interface {
	CreatePost(ctx context.Context, req rest.PostRequest) (rest.Post, error)
	UploadFile(ctx context.Context, channelID, filename string, data io.Reader) (string, error)
}

// Renderer produces a screenshot PNG for a given marimo notebook file.
type Renderer interface {
	Screenshot(ctx context.Context, notebookFile string) ([]byte, error)
}

// OutboundConfig wires the polling loop.
type OutboundConfig struct {
	Store    *state.Store
	Multica  MulticaPoller
	MM       MMUploader
	Render   Renderer
	Logger   *slog.Logger

	// MMBotMulticaID is the multica author UUID assigned to the bot itself
	// (e.g. the agent-2 UUID for the mmbot daemon). Comments authored by
	// this id are dropped on outbound to avoid echo loops between the bot
	// and itself when the agent posts inside the same workspace.
	MMBotMulticaID string

	// AgentMulticaID is the multica author UUID of the marimo agent
	// (e.g. agent-2 UUID). Used to set the MM post author_name on
	// forwarded comments: infra messages stay as multica-bot, agent
	// messages display as "agent-1 ↪ marimo-pair" (finding #8).
	AgentMulticaID string

	// TailnetHostHint is passed to render.ExtractTailnetURL so the
	// outbound poller doesn't hardcode the tail38d0e3.ts.net hostname
	// (finding #1).
	TailnetHostHint string

	// ScreenshotWindow rate-limits per-issue screenshots. Default 60s.
	ScreenshotWindow time.Duration

	// MulticaWebBase for forming PUL-N links in status-change notices.
	MulticaWebBase string
}

// Outbound runs polling cycles and pushes multica updates into MM threads.
type Outbound struct {
	cfg OutboundConfig
}

// NewOutbound constructs an Outbound with default rate-limit + log fallback.
func NewOutbound(cfg OutboundConfig) *Outbound {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ScreenshotWindow == 0 {
		cfg.ScreenshotWindow = 60 * time.Second
	}
	if cfg.MulticaWebBase == "" {
		cfg.MulticaWebBase = "https://multica.ai"
	}
	if cfg.TailnetHostHint == "" {
		cfg.TailnetHostHint = "ts.net"
	}
	return &Outbound{cfg: cfg}
}

// PollOnce iterates active threads, fetches new comments via the multica
// CLI, posts them to MM (with author override + optional PNG attachment),
// and forwards status changes.
//
// PollOnce is safe to call concurrently with itself only insofar as the
// underlying store and MM/multica APIs are; the daemon calls this from one
// loop goroutine.
func (o *Outbound) PollOnce(ctx context.Context) error {
	issueIDs, err := o.cfg.Store.ActiveIssueIDs(ctx)
	if err != nil {
		return fmt.Errorf("mmbot/outbound: active issues: %w", err)
	}
	for _, issueID := range issueIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := o.pollIssue(ctx, issueID); err != nil {
			o.cfg.Logger.Warn("mmbot/outbound: per-issue poll error", "issue", issueID, "err", err)
			// Keep going — one issue's failure shouldn't starve the rest.
		}
	}
	return nil
}

func (o *Outbound) pollIssue(ctx context.Context, issueID string) error {
	thread, err := o.cfg.Store.ThreadByIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("lookup thread: %w", err)
	}

	cursorKey := state.MetaKey("issue_cursor_" + issueID)
	since, _ := o.cfg.Store.GetMeta(ctx, cursorKey)
	var sinceT time.Time
	if since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			sinceT = t
		}
	}

	comments, err := o.cfg.Multica.ListComments(ctx, issueID, sinceT)
	if err != nil {
		return fmt.Errorf("list comments: %w", err)
	}

	var maxCreatedAt time.Time
	for _, cmt := range comments {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Skip comments authored by the mmbot itself (we wrote them via
		// the inbound handler — though today this is rare because the
		// inbound handler doesn't add multica comments as the bot;
		// keeping the check for defence).
		if cmt.AuthorID == o.cfg.MMBotMulticaID && o.cfg.MMBotMulticaID != "" {
			continue
		}
		// Skip comments we've already forwarded.
		seen, err := o.cfg.Store.IsSyncedComment(ctx, cmt.ID)
		if err != nil {
			return fmt.Errorf("synced lookup: %w", err)
		}
		if seen {
			continue
		}
		if err := o.forwardComment(ctx, thread, cmt); err != nil {
			return fmt.Errorf("forward comment %s: %w", cmt.ID, err)
		}
		// Track the maximum create-time so we can move the per-issue
		// cursor forward.
		if cmt.CreatedAt != "" {
			if t, err := time.Parse(time.RFC3339, cmt.CreatedAt); err == nil && t.After(maxCreatedAt) {
				maxCreatedAt = t
			}
		}
	}

	if !maxCreatedAt.IsZero() {
		_ = o.cfg.Store.SetMeta(ctx, cursorKey, maxCreatedAt.UTC().Format(time.RFC3339))
	}

	// Status-change observation.
	issue, err := o.cfg.Multica.GetIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	if issue.Status != "" && issue.Status != thread.LastSeenStatus {
		notice := fmt.Sprintf("Статус: → %s", issue.Status)
		_, postErr := o.cfg.MM.CreatePost(ctx, rest.PostRequest{
			ChannelID:      thread.ChannelID,
			RootID:         thread.RootPostID,
			Message:        notice,
			AuthorOverride: "multica-bot",
		})
		if postErr != nil {
			o.cfg.Logger.Warn("mmbot/outbound: status notice failed", "issue", issueID, "err", postErr)
		} else {
			_ = o.cfg.Store.SetThreadStatus(ctx, issueID, issue.Status)
		}
	}
	return nil
}

func (o *Outbound) forwardComment(ctx context.Context, thread state.Thread, cmt multicacli.Comment) error {
	author := "agent"
	if o.cfg.AgentMulticaID != "" && cmt.AuthorID == o.cfg.AgentMulticaID {
		author = "agent-1 ↪ marimo-pair"
	}

	// Idempotent screenshot pipeline (finding #1). Extract tailnet URL,
	// check rate-limit window, render PNG, upload, attach to the post.
	var fileIDs []string
	if tailURL := render.ExtractTailnetURL(cmt.Content, o.cfg.TailnetHostHint); tailURL != "" {
		ok, err := o.cfg.Store.ShouldRender(ctx, thread.MulticaIssueID, time.Now(), o.cfg.ScreenshotWindow)
		if err != nil {
			o.cfg.Logger.Warn("mmbot/outbound: should-render lookup", "err", err)
		} else if ok {
			if notebook, err := render.NotebookFromTailnetURL(tailURL); err == nil {
				png, rerr := o.cfg.Render.Screenshot(ctx, notebook)
				if rerr != nil {
					// Soft failure: post text-only.
					o.cfg.Logger.Info("mmbot/outbound: screenshot skipped", "err", rerr, "notebook", notebook)
				} else if len(png) > 0 {
					fid, uerr := o.cfg.MM.UploadFile(ctx, thread.ChannelID, notebook+".png", bytes.NewReader(png))
					if uerr != nil {
						o.cfg.Logger.Warn("mmbot/outbound: upload PNG failed", "err", uerr)
					} else {
						fileIDs = []string{fid}
						_ = o.cfg.Store.MarkRendered(ctx, thread.MulticaIssueID, time.Now())
					}
				}
			}
		}
	}

	mmPost, err := o.cfg.MM.CreatePost(ctx, rest.PostRequest{
		ChannelID:      thread.ChannelID,
		RootID:         thread.RootPostID,
		Message:        cmt.Content,
		FileIDs:        fileIDs,
		AuthorOverride: author,
	})
	if err != nil {
		return fmt.Errorf("create MM post: %w", err)
	}

	// Belt-and-suspenders echo dedup (finding #7).
	if err := o.cfg.Store.RecordSyncedPost(ctx, mmPost.ID, cmt.ID, state.DirectionMulticaToMM); err != nil {
		o.cfg.Logger.Warn("mmbot/outbound: record outbound dedup", "err", err)
	}
	if err := o.cfg.Store.RecordSyncedComment(ctx, cmt.ID, mmPost.ID); err != nil {
		return fmt.Errorf("record synced comment: %w", err)
	}
	return nil
}

