package telegramoutbound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Config wires the scheduler to its collaborators. Callers construct
// this at server startup once the feature flag is on.
type Config struct {
	// Queries is the minimum surface the scheduler needs from the
	// sqlc-generated code. *db.Queries satisfies it structurally in
	// production; tests substitute a mem-fake implementation.
	Queries SchedulerQueries
	Client  BotAPI       // *Client in prod; a fake in tests
	Limiter *Limiter     // *Limiter in prod; a permissive one in tests
	ChatID  int64        // target supergroup (MULTICA_TELEGRAM_CHAT_ID)
	Alert   AlertFunc    // called on fatal errors; may be nil to disable

	// Tunables — production leaves them zero (use defaults). Tests
	// override for fast ticks.
	TickInterval time.Duration
	MaxRetries   int32
	StuckClaimTTL time.Duration
	BackoffBase   time.Duration
	BackoffMax    time.Duration
}

// SchedulerQueries is the narrow DB interface the scheduler depends on.
// *db.Queries implements it by construction (sqlc generates these
// methods with matching signatures). Declared here so scheduler tests
// need only a small in-memory fake, not the full 200-method surface.
type SchedulerQueries interface {
	ClaimPendingTelegramOutbox(ctx context.Context) ([]db.TelegramOutbox, error)
	ResetStuckTelegramOutboxClaims(ctx context.Context) (int64, error)
	DeleteTelegramOutboxRow(ctx context.Context, id int64) error
	BumpTelegramOutboxRetry(ctx context.Context, arg db.BumpTelegramOutboxRetryParams) (db.TelegramOutbox, error)
	ParkTelegramOutboxRateLimit(ctx context.Context, arg db.ParkTelegramOutboxRateLimitParams) (db.TelegramOutbox, error)
	ParkTelegramOutboxFailed(ctx context.Context, arg db.ParkTelegramOutboxFailedParams) (db.TelegramOutbox, error)
	GetTelegramThreadByIssue(ctx context.Context, issueID pgtype.UUID) (db.TelegramThread, error)
	InsertTelegramThread(ctx context.Context, arg db.InsertTelegramThreadParams) (db.TelegramThread, error)
	DeleteTelegramThreadByIssue(ctx context.Context, issueID pgtype.UUID) error
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
}

// BotAPI is the subset of Client used by the scheduler. Interface
// (rather than *Client directly) lets tests inject a fake without
// starting an httptest server.
type BotAPI interface {
	SendMessage(ctx context.Context, chatID int64, threadID int, text string) SendResult
	CreateForumTopic(ctx context.Context, chatID int64, name string) SendResult
}

// AlertFunc is called synchronously when a row is parked as failed.
// The multica-side integration passes a closure that ships an alert
// via notify.Bridge (cascade fallback → issue comment when Slack/TG
// downstream are also down). Kept as a callback rather than importing
// notify directly so the outbound package has zero cross-package
// dependencies inside internal/.
type AlertFunc func(ctx context.Context, row db.TelegramOutbox, res SendResult)

// Scheduler owns the outbound-tick loop.
type Scheduler struct {
	cfg Config
	log *slog.Logger
}

// NewScheduler validates cfg and applies defaults. Returns an error
// when required deps are nil.
func NewScheduler(cfg Config, logger *slog.Logger) (*Scheduler, error) {
	if cfg.Queries == nil {
		return nil, errors.New("telegramoutbound: Queries required")
	}
	if cfg.Client == nil {
		return nil, errors.New("telegramoutbound: Client required")
	}
	if cfg.ChatID == 0 {
		return nil, errors.New("telegramoutbound: ChatID required")
	}
	if cfg.Limiter == nil {
		cfg.Limiter = NewLimiter()
	}
	if cfg.TickInterval == 0 {
		cfg.TickInterval = 2 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 10
	}
	if cfg.StuckClaimTTL == 0 {
		cfg.StuckClaimTTL = 60 * time.Second
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 2 * time.Second
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = 5 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{cfg: cfg, log: logger}, nil
}

// Run is the entry point spawned by cmd/server/main.go. Blocks until
// ctx is cancelled. Never returns an error — transient PG hiccups are
// logged and the loop keeps going.
func (s *Scheduler) Run(ctx context.Context) {
	// Startup recovery: release any claims left orphaned by a previous
	// process crash. Safe to run unconditionally.
	if rows, err := s.cfg.Queries.ResetStuckTelegramOutboxClaims(ctx); err != nil {
		s.log.Warn("telegramoutbound: startup stuck-claim reset failed", "error", err)
	} else if rows > 0 {
		s.log.Info("telegramoutbound: startup released stuck claims", "count", rows)
	}

	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick performs one cycle: reset stuck claims → claim pending →
// process each. Each row is processed sequentially inside the same
// goroutine; the per-chat rate limiter is what paces us, not the
// scheduler loop itself.
func (s *Scheduler) tick(ctx context.Context) {
	if _, err := s.cfg.Queries.ResetStuckTelegramOutboxClaims(ctx); err != nil {
		s.log.Warn("telegramoutbound: stuck-claim reset failed", "error", err)
	}
	rows, err := s.cfg.Queries.ClaimPendingTelegramOutbox(ctx)
	if err != nil {
		s.log.Warn("telegramoutbound: claim failed", "error", err)
		return
	}
	for _, row := range rows {
		s.processRow(ctx, row)
	}
}

// processRow: look up (or lazily create) the topic, format the body,
// chunk if needed, send each chunk under the rate-limiter, finalize
// the outbox row according to the result of the FIRST failing chunk
// (or delete on all-success).
func (s *Scheduler) processRow(ctx context.Context, row db.TelegramOutbox) {
	thread, res, ok := s.resolveThread(ctx, row)
	if !ok {
		s.finalize(ctx, row, res)
		return
	}
	text, ok := s.buildText(row)
	if !ok {
		// Malformed payload — treat as fatal. Should not happen
		// unless a schema change lands without a migration.
		s.finalize(ctx, row, SendResult{
			Outcome:     OutcomeFatal,
			Description: "malformed payload",
		})
		return
	}
	chunks := WithProgressPrefix(SplitByLines(text))
	for _, chunk := range chunks {
		if err := s.cfg.Limiter.Wait(ctx, thread.ChatID); err != nil {
			// Context cancelled during shutdown; leave the claim
			// intact — stuck-claim recovery will pick it up.
			s.log.Info("telegramoutbound: shutdown mid-send", "row_id", row.ID)
			return
		}
		res := s.cfg.Client.SendMessage(ctx, thread.ChatID, int(thread.MessageThreadID), chunk)
		if res.Outcome != OutcomeOK {
			s.finalize(ctx, row, res)
			return
		}
	}
	// All chunks OK → delete row.
	if err := s.cfg.Queries.DeleteTelegramOutboxRow(ctx, row.ID); err != nil {
		s.log.Warn("telegramoutbound: delete row failed", "row_id", row.ID, "error", err)
	}
}

// resolveThread returns the existing telegram_thread row for the
// issue or lazily creates one via createForumTopic. Returns
// (thread, ok=true) on success; (zero, res, ok=false) if the topic
// creation itself failed — in which case finalize() is called on the
// outbox row using res.
func (s *Scheduler) resolveThread(ctx context.Context, row db.TelegramOutbox) (db.TelegramThread, SendResult, bool) {
	thread, err := s.cfg.Queries.GetTelegramThreadByIssue(ctx, row.IssueID)
	if err == nil {
		return thread, SendResult{}, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// PG error other than no-row — treat as transient.
		s.log.Warn("telegramoutbound: GetTelegramThread failed", "row_id", row.ID, "error", err)
		return db.TelegramThread{}, SendResult{
			Outcome:     OutcomeTransient,
			Description: "GetTelegramThread: " + err.Error(),
		}, false
	}
	// No mapping yet — create the topic.
	name := s.topicNameForIssue(ctx, row.IssueID)
	if err := s.cfg.Limiter.Wait(ctx, s.cfg.ChatID); err != nil {
		return db.TelegramThread{}, SendResult{Outcome: OutcomeTransient, Description: "ctx cancelled during rate-limit"}, false
	}
	res := s.cfg.Client.CreateForumTopic(ctx, s.cfg.ChatID, name)
	if res.Outcome != OutcomeOK {
		return db.TelegramThread{}, res, false
	}
	inserted, err := s.cfg.Queries.InsertTelegramThread(ctx, db.InsertTelegramThreadParams{
		IssueID:         row.IssueID,
		ChatID:          s.cfg.ChatID,
		MessageThreadID: int32(res.TopicID),
	})
	if err != nil {
		// ON CONFLICT DO NOTHING → 0 rows returned → pgx.ErrNoRows
		// on RETURNING. Re-read to get the winner's row.
		refetched, gErr := s.cfg.Queries.GetTelegramThreadByIssue(ctx, row.IssueID)
		if gErr != nil {
			s.log.Warn("telegramoutbound: InsertTelegramThread + refetch failed",
				"row_id", row.ID, "error", err, "refetch_err", gErr)
			return db.TelegramThread{}, SendResult{Outcome: OutcomeTransient, Description: "thread insert failed"}, false
		}
		return refetched, SendResult{}, true
	}
	return inserted, SendResult{}, true
}

// topicNameForIssue builds "PUL-N · title". Falls back to the raw
// issue UUID if we cannot resolve the identifier — never returns
// empty, because Bot API rejects blank topic names.
func (s *Scheduler) topicNameForIssue(ctx context.Context, issueID pgtype.UUID) string {
	issue, err := s.cfg.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return "issue " + fmtUUID(issueID)
	}
	ws, err := s.cfg.Queries.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		// Give up on the PUL-N prefix; use just the title.
		return FormatTopicName("", issue.Title)
	}
	identifier := ws.IssuePrefix + "-" + strconv.Itoa(int(issue.Number))
	return FormatTopicName(identifier, issue.Title)
}

// outboxPayload is the JSONB shape written by CommentService.Create
// (kind='comment'). Kept as a small struct rather than
// map[string]any so schema changes require code changes here too —
// catching drift at compile time.
type outboxPayload struct {
	Content     string `json:"content"`
	AuthorLabel string `json:"author_label"`
	Identifier  string `json:"identifier"`
}

// buildText unmarshals payload and calls FormatMessage. Returns
// (text, true) on success; ("", false) on JSON error.
func (s *Scheduler) buildText(row db.TelegramOutbox) (string, bool) {
	var p outboxPayload
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		s.log.Warn("telegramoutbound: bad outbox payload", "row_id", row.ID, "error", err)
		return "", false
	}
	return FormatMessage(p.Identifier, p.AuthorLabel, p.Content), true
}

// finalize picks the right query for the outcome and calls the alert
// callback for fatal parks. Every outbox-row exit path funnels here.
func (s *Scheduler) finalize(ctx context.Context, row db.TelegramOutbox, res SendResult) {
	switch res.Outcome {
	case OutcomeRateLimit:
		notBefore := time.Now().Add(res.RetryAfter)
		if _, err := s.cfg.Queries.ParkTelegramOutboxRateLimit(ctx, db.ParkTelegramOutboxRateLimitParams{
			NotBeforeAt: pgtype.Timestamptz{Time: notBefore, Valid: true},
			LastError:   pgtype.Text{String: res.LastError(), Valid: true},
			ID:          row.ID,
		}); err != nil {
			s.log.Warn("telegramoutbound: park rate-limit failed", "row_id", row.ID, "error", err)
		}

	case OutcomeTopicDeleted:
		// Delete the stale mapping. Backoff a small amount before
		// retry so the next tick has a chance to observe the new
		// topic id.
		if err := s.cfg.Queries.DeleteTelegramThreadByIssue(ctx, row.IssueID); err != nil {
			s.log.Warn("telegramoutbound: delete stale thread failed", "row_id", row.ID, "error", err)
		}
		notBefore := time.Now().Add(s.cfg.BackoffBase)
		if _, err := s.cfg.Queries.ParkTelegramOutboxRateLimit(ctx, db.ParkTelegramOutboxRateLimitParams{
			NotBeforeAt: pgtype.Timestamptz{Time: notBefore, Valid: true},
			LastError:   pgtype.Text{String: "topic deleted → will recreate", Valid: true},
			ID:          row.ID,
		}); err != nil {
			s.log.Warn("telegramoutbound: reschedule after topic-deleted failed", "row_id", row.ID, "error", err)
		}

	case OutcomeTransient:
		if row.RetryCount+1 >= s.cfg.MaxRetries {
			s.parkFailed(ctx, row, res)
			return
		}
		delay := backoff(row.RetryCount, s.cfg.BackoffBase, s.cfg.BackoffMax)
		notBefore := time.Now().Add(delay)
		if _, err := s.cfg.Queries.BumpTelegramOutboxRetry(ctx, db.BumpTelegramOutboxRetryParams{
			NotBeforeAt: pgtype.Timestamptz{Time: notBefore, Valid: true},
			LastError:   pgtype.Text{String: res.LastError(), Valid: true},
			ID:          row.ID,
		}); err != nil {
			s.log.Warn("telegramoutbound: bump retry failed", "row_id", row.ID, "error", err)
		}

	case OutcomeFatal:
		s.parkFailed(ctx, row, res)

	default:
		// OutcomeOK should not reach finalize — that path deletes
		// the row directly. Log as a bug if it happens.
		s.log.Warn("telegramoutbound: unexpected OK in finalize", "row_id", row.ID)
	}
}

func (s *Scheduler) parkFailed(ctx context.Context, row db.TelegramOutbox, res SendResult) {
	if _, err := s.cfg.Queries.ParkTelegramOutboxFailed(ctx, db.ParkTelegramOutboxFailedParams{
		LastError: pgtype.Text{String: res.LastError(), Valid: true},
		ID:        row.ID,
	}); err != nil {
		s.log.Warn("telegramoutbound: park failed", "row_id", row.ID, "error", err)
		return
	}
	s.log.Warn("telegramoutbound: parked as failed",
		"row_id", row.ID,
		"issue_id", fmtUUID(row.IssueID),
		"kind", row.Kind,
		"reason", res.LastError(),
	)
	if s.cfg.Alert != nil {
		s.cfg.Alert(ctx, row, res)
	}
}

// backoff returns exp-backoff (2^retry_count * base) clamped to max.
// Deterministic (no jitter) so tests can assert exact timings.
func backoff(retryCount int32, base, max time.Duration) time.Duration {
	if retryCount < 0 {
		retryCount = 0
	}
	// Cap the exponent to avoid overflow on int32.
	shift := retryCount
	if shift > 20 {
		shift = 20
	}
	d := base * (1 << shift)
	if d > max || d <= 0 {
		return max
	}
	return d
}

// fmtUUID returns the canonical UUID string for a pgtype.UUID, or
// "<nil>" when the value is invalid. Not exported — a copy exists in
// server/internal/util but importing util from an internal package
// causes cyclic-import churn we don't need here.
func fmtUUID(u pgtype.UUID) string {
	if !u.Valid {
		return "<nil>"
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
