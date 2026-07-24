// Package telegramoutbound is PUL-479 PR1: the multica → Telegram mirror.
//
// One Telegram forum-topic per multica issue. Each comment on the issue
// becomes a message in the topic. The bridge is outbound-only — the
// package makes no assumption about inbound network reachability
// (PUL-166 forbids public inbound; PR2 will add long-poll ingress
// symmetrical to server/internal/githubpoll).
//
// Wiring shape (mirror of PUL-164 child_progress):
//
//   HTTP POST /comments
//     → service.CommentService.Create
//     → INSERT comment + INSERT telegram_outbox row (same tx)
//     → tx.Commit
//
//   Scheduler tick (every 2s, single worker)
//     → ClaimPendingTelegramOutbox
//     → for each row: resolve issue → topic (lazy createForumTopic
//       on cache miss), rate-limit wait, sendMessage, resolve outcome
//     → DeleteTelegramOutboxRow (success) /
//       BumpTelegramOutboxRetry (5xx, network) /
//       ParkTelegramOutboxRateLimit (429) /
//       ParkTelegramOutboxFailed (401, BOT_KICKED, retry_count>10)
//
// Feature flag: env.FromEnv returns nil when MULTICA_TELEGRAM_ENABLED
// is not "true", which keeps the whole subsystem inert on hosts that
// have not opted in — including the outbox INSERT in CommentService,
// which is guarded by CommentService.SetTelegramOutboxEnabled.
package telegramoutbound
