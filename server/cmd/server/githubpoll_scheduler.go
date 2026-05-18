package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/githubpoll"
	"github.com/multica-ai/multica/server/internal/webhooks/github"
)

// runGithubPoller boots the PUL-166 outbound poller. PR2 ships the
// goroutine wiring under a default-off feature flag — the function
// is invoked unconditionally from main.go, but exits silently when
// MULTICA_GITHUB_POLL_ENABLED is not truthy, mirroring how the
// cascade webhook subsystem is gated.
//
// The poller in PR2 runs with a LoggingSink (dry-run). PR3 replaces
// the sink with a CascadeRetriggerSink that writes the same rows the
// inbound webhook adapter writes today; main.go's call signature
// does not change.
//
// pool may be nil during tests / DB-disabled runs. We tolerate that
// by checking before constructing the cursor store — the poller
// goroutine just doesn't spawn.
func runGithubPoller(ctx context.Context, pool *pgxpool.Pool) {
	if !envBool("MULTICA_GITHUB_POLL_ENABLED", false) {
		return
	}
	repos := githubpoll.ParseRepos(os.Getenv("MULTICA_GITHUB_POLL_REPOS"))
	if len(repos) == 0 {
		slog.Warn("githubpoll.disabled.no_repos",
			"hint", "set MULTICA_GITHUB_POLL_REPOS=owner/name,owner/name to enable")
		return
	}
	if pool == nil {
		slog.Warn("githubpoll.disabled.no_db_pool")
		return
	}
	intervalSec, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("MULTICA_GITHUB_POLL_INTERVAL_SEC")))
	interval, err := githubpoll.ParseInterval(intervalSec)
	if err != nil {
		slog.Error("githubpoll.disabled.invalid_interval", "error", err)
		return
	}

	// Auth: PAT today (MULTICA_GITHUB_API_TOKEN, same env var the
	// webhook adapter already reads). PUL-141 swaps the TokenSource
	// implementation for an App-installation source without changing
	// the poller wiring — that's the whole point of the indirection.
	tokenSource := githubpoll.EnvPATSource{EnvVar: "MULTICA_GITHUB_API_TOKEN"}

	// Resolver: shared with the webhook adapter's commit→PRs
	// fallback. Constructed only when a PAT is present; without it
	// the workflow_run / check_run path silently skips events with
	// empty pull_requests (same behavior as the webhook adapter
	// pre-PUL-148 fix).
	var resolver github.PRResolver
	if token := strings.TrimSpace(os.Getenv("MULTICA_GITHUB_API_TOKEN")); token != "" {
		resolver = github.NewHTTPResolver(token)
	}

	client := githubpoll.NewClient(tokenSource)
	classifier := githubpoll.Classifier{Resolver: resolver}
	cursors := githubpoll.NewCursorStore(pool)

	// PR2: dry-run only. Logs every classified event; never writes
	// to cascade_retrigger. PR3 swaps in the real sink.
	sink := githubpoll.LoggingSink{}

	cfg := githubpoll.Config{
		Repos:    repos,
		Interval: interval,
		Logger:   slog.Default(),
	}
	poller := githubpoll.NewPoller(cfg, client, classifier, cursors, sink)

	// Counter is observable via /health/realtime-style introspection
	// in PR3 (added alongside the live writes). For PR2 it lives
	// only in-process and rolls up to the structured log fields.
	var panics atomic.Int64

	go githubpoll.RunWithRecover(
		ctx,
		"github_poller",
		&panics,
		slog.Default(),
		5*time.Second,
		poller.Run,
	)
	slog.Info("githubpoll.started",
		"repos", strings.Join(repos, ","),
		"interval", interval,
		"dry_run", true,
	)
}

// envBool is a small helper kept local to this file rather than
// reaching for the existing parseBoolFlag — it preserves the
// truthy/falsy semantics used by webhooks.FeatureFlagEnvVar and the
// cascade subsystem, so operators flipping the poller flag see the
// same accept-list ("1", "true", "yes", "on").
func envBool(name string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
