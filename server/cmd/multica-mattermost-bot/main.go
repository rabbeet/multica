// Command multica-mattermost-bot bridges a Mattermost channel into multica
// issues in the marimo project (PUL-328).
//
// One MM thread maps to one multica issue, bidirectionally. Top-level posts
// from whitelisted users in whitelisted channels become new issues; thread
// replies become comments; agent-1 comments and status changes flow back into
// the MM thread as posts (with PNG screenshots for matplotlib cells, finding
// #1 of /plan-eng-review).
//
// This file is the entrypoint stub for the Lane A foundation PR. Subsequent
// PRs wire the REST client (Lane B), screenshot pipeline (Lane C), WS client
// + handlers (Lane D), and runtime wiring (Lane E).
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

func main() {
	statePath := flag.String("state", envOr("MMBOT_STATE_DB_PATH", "/var/lib/multica-mattermost-bot/state.db"), "path to SQLite state database")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := state.Open(ctx, *statePath)
	if err != nil {
		logger.Error("open state", "err", err, "path", *statePath)
		os.Exit(1)
	}
	defer store.Close()

	logger.Info("multica-mattermost-bot scaffold ready",
		"state_path", *statePath,
		"schema_version", state.SchemaVersion,
		"note", "REST/WS/render/handler wiring lands in subsequent PRs (Lanes B-E)")

	// Hold the process until a signal arrives so the scaffold can be smoke-
	// tested with `systemd-run --user` even before the WS client lands.
	<-ctx.Done()
	logger.Info("multica-mattermost-bot shutting down")
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
