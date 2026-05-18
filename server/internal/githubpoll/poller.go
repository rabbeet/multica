package githubpoll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/webhooks"
)

// Sink consumes classified TriggerEvents. PR2 ships LoggingSink, a
// dry-run sink that only logs. PR3 swaps in a CascadeRetriggerSink
// that writes rows to the same table the webhook adapter writes to.
// The Sink interface keeps poller.go agnostic to persistence, which
// is the boundary the staged rollout depends on.
type Sink interface {
	Submit(ctx context.Context, repo string, event webhooks.TriggerEvent) error
}

// LoggingSink is the dry-run sink. Logs each event at info and never
// fails. Used in PR2 to validate end-to-end flow without touching
// cascade_retrigger.
type LoggingSink struct {
	Logger *slog.Logger
}

// Submit implements Sink.
func (s LoggingSink) Submit(_ context.Context, repo string, e webhooks.TriggerEvent) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("githubpoll.dry_run.classified",
		"repo", repo,
		"event_id", e.EventID.String(),
		"event_type", e.EventType,
		"pr_number", e.PRNumber,
		"head_sha", e.HeadSHA,
		"branch", e.Branch,
	)
	return nil
}

// cursorIO captures the subset of CursorStore the poller needs.
// The interface lets tests inject NewMemCursorStore without a DB.
// Kept inside the package — production wires *CursorStore directly,
// and Go's structural typing means callers don't need to declare
// implementation.
type cursorIO interface {
	Load(ctx context.Context, repo string) (Cursor, error)
	Save(ctx context.Context, c Cursor) error
}

// Config captures the runtime knobs read from env vars at startup
// in cmd/server/main.go. Empty Repos disables the poller entirely;
// the wiring in main.go then logs "poller disabled" and returns
// without spawning any goroutines.
type Config struct {
	// Repos is the list of "owner/name" the poller scrapes. Source
	// of truth is MULTICA_GITHUB_POLL_REPOS env var (csv).
	Repos []string

	// Interval between ticks. Defaults to 30 * time.Second when
	// zero. Source of truth: MULTICA_GITHUB_POLL_INTERVAL_SEC.
	Interval time.Duration

	// PagesLimit caps pagination per tick. 0 falls back to the
	// client default (5).
	PagesLimit int

	// Logger is used for structured per-event diagnostics. nil
	// falls back to slog.Default().
	Logger *slog.Logger

	// Now is overridable for tests so cursor.LastPolledAt is
	// deterministic. nil falls back to time.Now.
	Now func() time.Time
}

// Poller wires Client + Classifier + Cursor + Sink for one set of
// repos. One Poller serves all watched repos sequentially per tick;
// at 4-10 repos × ~50KB/page the per-tick cost is dominated by
// network round-trip, and sequential keeps the cursor I/O simple.
// If repos ever scale past ~50, fan out per-repo workers.
type Poller struct {
	cfg        Config
	client     *Client
	classifier Classifier
	cursors    cursorIO
	sink       Sink
}

// NewPoller assembles a Poller. cfg.Repos may be empty (caller is
// responsible for skipping the goroutine in that case). cursors and
// sink may be nil only for tests that don't call Run.
func NewPoller(cfg Config, client *Client, classifier Classifier, cursors cursorIO, sink Sink) *Poller {
	return &Poller{
		cfg:        cfg,
		client:     client,
		classifier: classifier,
		cursors:    cursors,
		sink:       sink,
	}
}

// logger returns the configured slog or default.
func (p *Poller) logger() *slog.Logger {
	if p.cfg.Logger != nil {
		return p.cfg.Logger
	}
	return slog.Default()
}

// now returns the configured time source or time.Now.
func (p *Poller) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}

// Run drives the tick loop. Each tick polls every repo sequentially.
// Returns when ctx is canceled. Errors from one repo never abort the
// tick — they are logged and observed via the rate-limit / lag
// metrics, and the next tick retries from the same cursor (the
// fail-shut-and-retry pattern keeps idempotency intact).
func (p *Poller) Run(ctx context.Context) error {
	interval := p.cfg.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if len(p.cfg.Repos) == 0 {
		p.logger().Info("githubpoll.poller.empty_repos_disabled")
		return nil
	}
	p.logger().Info("githubpoll.poller.starting",
		"repos", strings.Join(p.cfg.Repos, ","),
		"interval", interval,
	)
	// Tick once at startup so the first events are observable
	// without waiting `interval`, then on a periodic timer.
	p.tickAll(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			p.tickAll(ctx)
		}
	}
}

// tickAll loops over configured repos. One slow repo doesn't block
// the others past the per-call client timeout (10s); a tick takes
// at most repos × 10s in absolute worst case.
func (p *Poller) tickAll(ctx context.Context) {
	for _, repo := range p.cfg.Repos {
		if ctx.Err() != nil {
			return
		}
		p.tickOne(ctx, repo)
	}
}

// tickOne is the per-repo work unit. Public-ish on the receiver so
// future tests / metrics middleware can wrap it.
func (p *Poller) tickOne(ctx context.Context, repo string) {
	logger := p.logger().With("repo", repo)

	cursor, err := p.cursors.Load(ctx, repo)
	if err != nil {
		logger.Error("githubpoll.poller.cursor_load_failed", "error", err)
		return
	}

	result, err := p.client.FetchEvents(ctx, repo, cursor.ETag, cursor.LastEventID)
	switch {
	case errors.Is(err, ErrNotModified):
		// Steady state. Bump last_polled_at; leave cursor.LastEventID
		// and cursor.ETag intact (Save is a no-op write if the row
		// already has the same etag, but we Save anyway to advance
		// last_polled_at for the staleness alert).
		cursor.LastPolledAt = p.now()
		if err := p.cursors.Save(ctx, cursor); err != nil {
			logger.Error("githubpoll.poller.cursor_save_failed_304", "error", err)
		}
		logger.Debug("githubpoll.poller.tick_not_modified",
			"rate_remaining", result.RateRemaining,
			"rate_limit", result.RateLimit,
		)
		return
	case errors.Is(err, ErrNoToken):
		logger.Error("githubpoll.poller.no_token")
		return
	case errors.As(err, new(ErrRateLimited)):
		var rate ErrRateLimited
		_ = errors.As(err, &rate)
		// Don't sleep here — the supervisor's backoff handles next-
		// tick timing, and we don't want to block sibling repos
		// behind a single 403. Log + return; next tick will retry.
		logger.Warn("githubpoll.poller.rate_limited",
			"reset_at", rate.ResetAt.Format(time.RFC3339),
		)
		return
	case err != nil:
		logger.Warn("githubpoll.poller.fetch_failed", "error", err)
		return
	}

	// Classify each event in oldest-first order. cursor.LastEventID
	// advances after every successful Sink.Submit so a mid-batch
	// crash resumes from the last persisted event_id, not the start.
	// (Save is best-effort each loop — under DB outage we just spin
	// re-submitting through Sink, which is idempotent.)
	classifiedCount := 0
	skipCount := 0
	schemaErrCount := 0
	for _, e := range result.Events {
		evt, err := p.classifier.Classify(ctx, repo, e)
		switch {
		case errors.Is(err, ErrSkip):
			skipCount++
		case errors.Is(err, ErrSchemaMismatch):
			schemaErrCount++
			logger.Warn("githubpoll.poller.schema_mismatch",
				"github_event_id", e.ID,
				"event_type", e.Type,
				"error", err,
			)
		case err != nil:
			logger.Warn("githubpoll.poller.classify_failed",
				"github_event_id", e.ID,
				"event_type", e.Type,
				"error", err,
			)
		case evt != nil:
			if submitErr := p.sink.Submit(ctx, repo, *evt); submitErr != nil {
				logger.Warn("githubpoll.poller.sink_failed",
					"github_event_id", e.ID,
					"error", submitErr,
				)
				// Don't advance cursor past an unsubmitted event —
				// the next tick will re-attempt from the same id.
				return
			}
			classifiedCount++
		}
		// Advance cursor regardless of skip/classify: we don't
		// need to re-see this event next tick.
		id, idErr := e.NumericID()
		if idErr == nil && id > cursor.LastEventID {
			cursor.LastEventID = id
		}
	}
	if result.ETag != "" {
		cursor.ETag = result.ETag
	}
	cursor.LastPolledAt = p.now()
	if err := p.cursors.Save(ctx, cursor); err != nil {
		logger.Error("githubpoll.poller.cursor_save_failed", "error", err)
	}
	logger.Info("githubpoll.poller.tick_complete",
		"events_seen", len(result.Events),
		"events_classified", classifiedCount,
		"events_skipped", skipCount,
		"events_schema_mismatch", schemaErrCount,
		"last_event_id", cursor.LastEventID,
		"rate_remaining", result.RateRemaining,
	)
}

// ParseRepos parses the comma-separated MULTICA_GITHUB_POLL_REPOS
// value. Trims whitespace, drops empties. Returns nil on empty input
// rather than an empty slice so callers can `if len(repos) == 0`
// without ambiguity.
func ParseRepos(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseInterval converts MULTICA_GITHUB_POLL_INTERVAL_SEC to a
// time.Duration with a 30s floor. Used by main.go to keep env-var
// parsing in one place. The cap (60min) prevents a typo (a
// nine-digit number) from silently disabling polling.
func ParseInterval(secs int) (time.Duration, error) {
	if secs <= 0 {
		return 30 * time.Second, nil
	}
	if secs > 3600 {
		return 0, fmt.Errorf("interval_sec=%d exceeds 1h ceiling", secs)
	}
	if secs < 10 {
		// Floor at 10s — sub-10s polling burns rate budget without
		// improving cascade reaction time meaningfully. 30s is the
		// default for a reason.
		return 10 * time.Second, nil
	}
	return time.Duration(secs) * time.Second, nil
}
