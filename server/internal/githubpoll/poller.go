package githubpoll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/webhooks"
)

// repoNamePattern enforces the GitHub "owner/repo" shape. Owner and
// repo each match GitHub's actual character set (alphanumeric, dot,
// hyphen, underscore) — anything else is rejected so that path
// fragments like "rabbeet/Pulse/../admin" cannot slip through
// MULTICA_GITHUB_POLL_REPOS into client.buildEventsURL and resolve
// to a different repo after URL normalization.
//
// The pattern alone is not sufficient: "../foo" matches it (each
// component is non-empty and uses only allowed characters), but
// resolves to a different repo after GitHub normalizes the URL.
// isValidRepoName layers an extra check for that on top.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// isValidRepoName returns true iff the repo string is shaped
// "owner/name" and neither component is "." or ".." (the
// path-traversal escape hatches that pattern alone admits).
func isValidRepoName(repo string) bool {
	if !repoNamePattern.MatchString(repo) {
		return false
	}
	slash := strings.IndexByte(repo, '/')
	owner, name := repo[:slash], repo[slash+1:]
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return false
	}
	return true
}

// Sink consumes classified TriggerEvents. PR2 ships LoggingSink, a
// dry-run sink that only logs. PR3 swaps in a CascadeRetriggerSink
// that writes rows to the same table the webhook adapter writes to.
// The Sink interface keeps poller.go agnostic to persistence, which
// is the boundary the staged rollout depends on.
//
// Metric ownership convention: the *caller* (poller.tickOne) owns
// metrics — it increments sink_errors_total on any non-nil error
// from Submit. Implementations should NOT increment metrics
// themselves to avoid double-counting under the current wiring.
// A future replay tool that calls Submit directly will need its
// own metric path.
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

	// Metrics is incremented on every tick. nil-safe — passing
	// nil disables metrics collection without branching the hot
	// path; the Metrics methods all guard on a nil receiver.
	Metrics *Metrics

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
	// Record observed rate-limit on every code path that produced a
	// response. 200, 304, and 403/rate-limit all carry the header;
	// the client propagates info even on the ErrRateLimited path so
	// the gauge actually drops to 0 (and triggers the alert) when
	// the budget is exhausted. We use RateLimit > 0 as the gate:
	// RateLimit is 5000 for authenticated callers, never zero in
	// a healthy response — so a zero-limit signals "no response
	// from GitHub at all" (DNS, transport failure), in which case
	// we leave the gauge alone and let the cursor_age alert fire.
	if result.RateLimit > 0 {
		p.cfg.Metrics.SetRateLimitRemaining(repo, result.RateRemaining)
	}
	switch {
	case errors.Is(err, ErrNotModified):
		// Steady state. Bump last_polled_at; leave cursor.LastEventID
		// and cursor.ETag intact (Save is a no-op write if the row
		// already has the same etag, but we Save anyway to advance
		// last_polled_at for the staleness alert).
		now := p.now()
		cursor.LastPolledAt = now
		p.cfg.Metrics.IncCall(repo, 304)
		p.cfg.Metrics.MarkTickComplete(repo, now.Unix())
		if err := p.cursors.Save(ctx, cursor); err != nil {
			logger.Error("githubpoll.poller.cursor_save_failed_304", "error", err)
		}
		logger.Debug("githubpoll.poller.tick_not_modified",
			"rate_remaining", result.RateRemaining,
			"rate_limit", result.RateLimit,
		)
		return
	case errors.Is(err, ErrNoToken):
		p.cfg.Metrics.IncCall(repo, 0)
		logger.Error("githubpoll.poller.no_token")
		return
	case errors.As(err, new(ErrRateLimited)):
		var rate ErrRateLimited
		_ = errors.As(err, &rate)
		p.cfg.Metrics.IncCall(repo, 403)
		// Don't sleep here — the supervisor's backoff handles next-
		// tick timing, and we don't want to block sibling repos
		// behind a single 403. Log + return; next tick will retry.
		logger.Warn("githubpoll.poller.rate_limited",
			"reset_at", rate.ResetAt.Format(time.RFC3339),
		)
		return
	case err != nil:
		// Generic fetch failure — DNS, timeout, 5xx. status code
		// not recoverable in a structured way; bucket as 0.
		p.cfg.Metrics.IncCall(repo, 0)
		logger.Warn("githubpoll.poller.fetch_failed", "error", err)
		return
	}
	p.cfg.Metrics.IncCall(repo, 200)

	// PUL-186 first-run seeding. A repo with no row in
	// github_poll_cursor returns Cursor{Repo: repo} from cursor.Load
	// — LastPolledAt is the zero time. cursor.Save writes
	// LastPolledAt = now() on EVERY successful tick (304, 200, even
	// the 0-events fall-through below), so a zero LastPolledAt is the
	// single, sufficient signal that we have never successfully polled
	// this repo. Single-field discriminator over a three-field check:
	// `LastEventID == 0` recurs after a legitimate operator rewind
	// (UPDATE … SET last_event_id = NULL), and we want THAT case to
	// flow normally — not re-seed.
	//
	// On firstRun with events present we record the max event id, save
	// the cursor, and return WITHOUT forwarding anything to the sink:
	// those events are pre-existing state, not "events that arrived
	// while we were watching". This implements the contract documented
	// in migration 080_github_poll_cursor.up.sql:24, which the original
	// PR2 wiring never honored.
	//
	// On firstRun with empty events we bump a counter and fall through
	// to the existing loop, which will Save ETag + LastPolledAt over
	// an empty event set — same row written, no events forwarded,
	// next tick is not firstRun.
	if cursor.LastPolledAt.IsZero() {
		if len(result.Events) > 0 {
			seeded := 0
			for _, e := range result.Events {
				id, idErr := e.NumericID()
				if idErr != nil {
					// Mirror the steady-state classifier
					// observability so operators still see GitHub
					// payload-drift signal on cold start.
					p.cfg.Metrics.IncEvent(repo, "schema_mismatch")
					logger.Warn("githubpoll.poller.schema_mismatch_in_seed",
						"github_event_id", e.ID,
						"event_type", e.Type,
						"error", idErr,
					)
					continue
				}
				if id > cursor.LastEventID {
					cursor.LastEventID = id
				}
				seeded++
			}
			if result.ETag != "" {
				cursor.ETag = result.ETag
			}
			cursor.LastPolledAt = p.now()
			p.cfg.Metrics.IncEvent(repo, "first_run_seeded")
			p.cfg.Metrics.MarkTickComplete(repo, cursor.LastPolledAt.Unix())
			if err := p.cursors.Save(ctx, cursor); err != nil {
				// Non-fatal: next tick reads no row again, is
				// firstRun again, re-seeds at then-current HEAD.
				// Idempotent because nothing was forwarded.
				logger.Error("githubpoll.poller.cursor_save_failed_seed", "error", err)
			}
			logger.Info("githubpoll.poller.first_run_seeded",
				"events_skipped_as_history", seeded,
				"last_event_id", cursor.LastEventID,
				"etag", cursor.ETag,
			)
			return
		}
		// firstRun && empty events: record the observation so the
		// cold-start counter is symmetric (seeded vs skipped). Fall
		// through to the existing loop which will Save the row with
		// LastPolledAt = now() — next tick is not firstRun.
		p.cfg.Metrics.IncEvent(repo, "first_run_skipped")
	}

	// Classify each event in oldest-first order. The on-disk cursor
	// is Saved exactly ONCE per successful tickOne — after the
	// whole batch processes. Per-event LastEventID bumps are
	// in-memory only.
	//
	// Mid-batch crash safety relies on Sink idempotency: the next
	// tick will re-fetch the same events (cursor on disk hasn't
	// moved), the Sink will see the same event_id, and PR3's
	// `INSERT ... ON CONFLICT DO NOTHING` will absorb the
	// duplicates. The Sink-failure path below early-returns BEFORE
	// the deferred Save, so a transient sink outage cannot
	// silently skip events.
	classifiedCount := 0
	skipCount := 0
	schemaErrCount := 0
	for _, e := range result.Events {
		evt, err := p.classifier.Classify(ctx, repo, e)
		switch {
		case errors.Is(err, ErrSkip):
			skipCount++
			p.cfg.Metrics.IncEvent(repo, "skip")
		case errors.Is(err, ErrSchemaMismatch):
			schemaErrCount++
			p.cfg.Metrics.IncEvent(repo, "schema_mismatch")
			logger.Warn("githubpoll.poller.schema_mismatch",
				"github_event_id", e.ID,
				"event_type", e.Type,
				"error", err,
			)
		case err != nil:
			// Distinct from schema_mismatch: the classifier failed
			// for a non-sentinel reason, most plausibly a resolver
			// HTTP error (commit→PRs fallback) that swallowed
			// upstream as ErrSkip but slipped through. Separate
			// bucket so alert routing can distinguish "GitHub
			// changed payload shape" from "transient network blip
			// on a fallback API call".
			p.cfg.Metrics.IncEvent(repo, "classify_error")
			logger.Warn("githubpoll.poller.classify_failed",
				"github_event_id", e.ID,
				"event_type", e.Type,
				"error", err,
			)
		case evt != nil:
			if submitErr := p.sink.Submit(ctx, repo, *evt); submitErr != nil {
				p.cfg.Metrics.IncSinkError(repo)
				logger.Warn("githubpoll.poller.sink_failed",
					"github_event_id", e.ID,
					"error", submitErr,
				)
				// Don't advance cursor past an unsubmitted event —
				// the next tick will re-attempt from the same id.
				return
			}
			classifiedCount++
			p.cfg.Metrics.IncEvent(repo, evt.EventType)
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
	now := p.now()
	cursor.LastPolledAt = now
	p.cfg.Metrics.MarkTickComplete(repo, now.Unix())
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
// value. Trims whitespace, drops empties, rejects entries that do
// not match the "owner/repo" pattern. Invalid entries are dropped
// silently with a slog warning — operators see the resulting Repos
// list in the startup banner so a typo (e.g. "rabbeet:Pulse") is
// loud enough.
//
// Validation matters here, not just at the client.buildEventsURL
// layer: an attacker who controls the env var could otherwise inject
// path fragments like "rabbeet/Pulse/../admin" that GitHub's API
// normalizes to a different repo.
//
// Returns nil on empty / fully-invalid input rather than an empty
// slice so callers can `if len(repos) == 0` without ambiguity.
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
		if !isValidRepoName(p) {
			slog.Warn("githubpoll.parse_repos.rejected_entry",
				"value", p,
				"reason", "does not match owner/repo shape (alphanumeric, dot, hyphen, underscore; no '.' or '..' components)")
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
