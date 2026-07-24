package telegramoutbound

import (
	"errors"
	"fmt"
	"time"
)

// SendOutcome tells the scheduler what to do next after Client.SendMessage
// or Client.CreateForumTopic returns. The typed shape (rather than error
// sentinels) lets a rate-limit response carry its Retry-After without a
// separate error field.
type SendOutcome int

const (
	// OutcomeOK — 2xx response, ok=true. Scheduler deletes the outbox row.
	OutcomeOK SendOutcome = iota
	// OutcomeTransient — 5xx, network error, or context timeout. Scheduler
	// bumps retry_count and reschedules with exponential backoff.
	OutcomeTransient
	// OutcomeRateLimit — 429. Scheduler advances not_before_at by
	// RetryAfter without bumping retry_count.
	OutcomeRateLimit
	// OutcomeFatal — 4xx that will never succeed (401 bad token,
	// BOT_KICKED, TOPIC_DELETED, CHAT_NOT_FOUND, MESSAGE_TOO_LONG after
	// chunk split gave up). Scheduler parks the row failed_at=now() and
	// escalates via the configured alert function.
	OutcomeFatal
	// OutcomeTopicDeleted — special-case of Fatal: the topic row in
	// telegram_thread is stale. Scheduler deletes the mapping and
	// reschedules the outbox row so the next tick recreates the topic.
	OutcomeTopicDeleted
)

// SendResult carries the outcome of one Bot API call. MessageID is set
// only on OutcomeOK for sendMessage; TopicID on OutcomeOK for
// createForumTopic. RetryAfter is set only on OutcomeRateLimit.
type SendResult struct {
	Outcome     SendOutcome
	MessageID   int
	TopicID     int
	RetryAfter  time.Duration
	Description string // Bot API "description" field; used for last_error
	StatusCode  int    // HTTP status; used for last_error
	Err         error  // underlying transport error, if any
}

// LastError renders a short human-readable summary suitable for
// telegram_outbox.last_error. Never returns empty on non-OK outcomes so
// operators can always see something in psql.
func (r SendResult) LastError() string {
	if r.Outcome == OutcomeOK {
		return ""
	}
	if r.Err != nil {
		return fmt.Sprintf("transport: %s", r.Err.Error())
	}
	desc := r.Description
	if desc == "" {
		desc = "<no description>"
	}
	return fmt.Sprintf("status=%d %s", r.StatusCode, desc)
}

// ErrConfig is returned when the client is constructed with missing
// bot token or malformed chat id.
var ErrConfig = errors.New("telegramoutbound: missing bot token or chat id")
