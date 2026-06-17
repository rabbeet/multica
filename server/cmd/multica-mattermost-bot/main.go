// Command multica-mattermost-bot bridges a Mattermost channel into multica
// issues in the marimo project (PUL-328).
//
// One MM thread maps to one multica issue, bidirectionally. Top-level posts
// from whitelisted users in whitelisted channels become new issues; thread
// replies become comments; agent comments and status changes flow back into
// the MM thread as posts (with PNG screenshots for matplotlib cells).
//
// Lane E entrypoint: loads Config from env (populated by the systemd
// ExecStartPre op-read-env.sh hook), takes a startup flock to enforce a
// single instance, constructs every dependency, runs the WS and outbound
// loops in goroutines, blocks on SIGTERM, then performs a bounded graceful
// shutdown.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/internal/mmbot/handler"
	"github.com/multica-ai/multica/server/internal/mmbot/multicacli"
	"github.com/multica-ai/multica/server/internal/mmbot/render"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
	"github.com/multica-ai/multica/server/internal/mmbot/ws"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("multica-mattermost-bot exited with error", "err", err)
		os.Exit(1)
	}
}

// run is the testable entrypoint — separated from main so an integration
// test could drive it with a mocked environment if needed.
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Info("multica-mattermost-bot starting",
		"mm_host", cfg.MMHost,
		"channels", len(cfg.AllowedChannels),
		"allowed_users", len(cfg.AllowedUserIDs),
		"state_path", cfg.StateDBPath,
		"poll_interval", cfg.OutboundPollInterval)

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 1. State store + startup flock so two daemons can't fight over the
	//    same SQLite file. The lock is released when the process exits.
	store, err := state.Open(rootCtx, cfg.StateDBPath)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer store.Close()

	lockPath := cfg.StateDBPath + ".lock"
	releaseLock, err := acquireLock(lockPath)
	if err != nil {
		return fmt.Errorf("startup lock: %w", err)
	}
	defer releaseLock()

	// 2. Dependencies — REST, multica CLI, screenshot renderer.
	mmRest, err := rest.New(rest.Config{
		BaseURL:   cfg.MMHost,
		Token:     cfg.MMBotToken,
		BotUserID: cfg.MMBotUserID,
		Logger:    logger.With("component", "rest"),
	})
	if err != nil {
		return fmt.Errorf("rest client: %w", err)
	}
	mc := multicacli.New(multicacli.Config{
		Binary:          cfg.MulticaBinary,
		AssigneeAgentID: cfg.AssigneeAgentID,
		Logger:          logger.With("component", "multicacli"),
	})
	rend := render.New(render.Config{
		MarimoBaseURL: cfg.MarimoLocalURL,
	})

	// 3. Handlers.
	inbound := handler.NewInbound(handler.InboundConfig{
		Store:             store,
		Multica:           mc,
		MM:                mmRest,
		Logger:            logger.With("component", "inbound"),
		AllowedChannelIDs: cfg.AllowedChannelSet(),
		AllowedUserIDs:    cfg.AllowedUserSet(),
		BotUserID:         cfg.MMBotUserID,
	})
	outbound := handler.NewOutbound(handler.OutboundConfig{
		Store:           store,
		Multica:         mc,
		MM:              mmRest,
		Render:          rend,
		Logger:          logger.With("component", "outbound"),
		AgentMulticaID:  cfg.AgentAuthorID,
		TailnetHostHint: cfg.MarimoTailnetHostHint,
	})

	// 4. WS client.
	wsClient := ws.New(ws.Config{
		BaseURL:         cfg.MMHost,
		Token:           cfg.MMBotToken,
		WatchChannelIDs: cfg.AllowedChannels,
		Handler:         inbound,
		Catchup:         mmRest,
		Store:           store,
		Logger:          logger.With("component", "ws"),
	})

	// 5. Run goroutines. Use a waitgroup so SIGTERM waits briefly for the
	//    polling loop to finish its current iteration.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := wsClient.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("ws loop exited", "err", err)
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		runOutboundLoop(rootCtx, outbound, cfg.OutboundPollInterval, logger.With("component", "outbound"))
	}()

	logger.Info("multica-mattermost-bot ready")
	<-rootCtx.Done()

	// 6. Graceful shutdown with a deadline so a wedged goroutine doesn't
	//    hold the unit forever. `pending_outbound` is persistent so a hard
	//    exit at the deadline is safe: the next startup drains the queue.
	logger.Info("multica-mattermost-bot stopping; awaiting in-flight work")
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	const shutdownGrace = 10 * time.Second
	select {
	case <-done:
		logger.Info("multica-mattermost-bot stopped cleanly")
	case <-time.After(shutdownGrace):
		logger.Warn("multica-mattermost-bot shutdown deadline reached; forcing exit")
	}
	return nil
}

// runOutboundLoop ticks outbound.PollOnce at cfg.OutboundPollInterval. One
// iteration runs at a time per Mattermost thread; we don't burst.
func runOutboundLoop(ctx context.Context, o *handler.Outbound, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pollCtx, cancel := context.WithTimeout(ctx, interval*3)
			if err := o.PollOnce(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("outbound poll error", "err", err)
			}
			cancel()
		}
	}
}
