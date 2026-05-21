package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/cascade"
	"github.com/multica-ai/multica/server/internal/githubpoll"
	"github.com/multica-ai/multica/server/internal/webhooks/github"
)

// runGithubPoller boots the PUL-166 outbound poller. The function is
// invoked unconditionally from main.go, but exits silently when
// MULTICA_GITHUB_POLL_ENABLED is not truthy, mirroring how the
// cascade webhook subsystem is gated.
//
// PR3 swap: replaces PR2's dry-run LoggingSink with a real
// CascadeRetriggerSink that writes to the cascade_retrigger table.
// Same table the inbound webhook adapter writes to — both channels
// share idempotency through the UNIQUE event_id constraint, though
// the production hard-cutover (PR4) keeps webhook OFF before poll
// ON so they never race in steady state.
//
// pool may be nil during tests / DB-disabled runs. The poller does
// not spawn in that case.
//
// metrics is shared with the Prometheus collector registered in
// internal/metrics.NewRegistry — the same pointer flows into both,
// so /metrics scrapes the live counters the hot path writes.
func runGithubPoller(ctx context.Context, pool *pgxpool.Pool, metrics *githubpoll.Metrics) {
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

	// PUL-216 reintroduces the resolver — narrower role than its
	// pre-PUL-212 shape. PullRequestEvent carries the PR number
	// inline, but REST /events strips pull_request.title; the
	// classifier uses the resolver only to hydrate that title via
	// GET /repos/{owner}/{repo}/pulls/{n}. Without a PAT the
	// resolver is nil and hydration is skipped silently — the
	// cascade worker falls through to the branch regex.
	var resolver github.PRResolver
	if token := strings.TrimSpace(os.Getenv("MULTICA_GITHUB_API_TOKEN")); token != "" {
		resolver = github.NewHTTPResolver(token)
	}

	client := githubpoll.NewClient(tokenSource)
	classifier := githubpoll.Classifier{Resolver: resolver}
	cursors := githubpoll.NewCursorStore(pool)

	// PR3 sink: writes to cascade_retrigger via the same store the
	// webhook adapter uses. ON CONFLICT (event_id) DO NOTHING handles
	// re-tick replays as silent no-ops.
	sink := githubpoll.NewCascadeRetriggerSink(cascade.NewStore(pool), slog.Default(), metrics)

	cfg := githubpoll.Config{
		Repos:    repos,
		Interval: interval,
		Logger:   slog.Default(),
		Metrics:  metrics,
	}
	poller := githubpoll.NewPoller(cfg, client, classifier, cursors, sink)

	go githubpoll.RunWithRecover(
		ctx,
		"github_poller",
		&metrics.PanicsTotal,
		slog.Default(),
		5*time.Second,
		poller.Run,
	)
	slog.Info("githubpoll.started",
		"repos", strings.Join(repos, ","),
		"interval", interval,
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
