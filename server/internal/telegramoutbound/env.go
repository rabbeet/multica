package telegramoutbound

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
)

// Env holds the parsed environment configuration for the outbound
// bridge. Callers get this via FromEnv; it is a plain data struct so
// tests can construct it directly.
type Env struct {
	Enabled                bool
	BotToken               string
	ChatID                 int64
	// WorkspaceID isn't consumed by PR1's outbound scheduler, but is
	// captured here for symmetry with PR2's inbound path — both must
	// agree on which workspace this bot bridges.
	WorkspaceID            string
	// DefaultAuthorID and AllowedSenderIDs are PR2-only fields;
	// captured here so a single FromEnv covers both PRs and operators
	// can set the full envvar set at once.
	DefaultAuthorID        string
	AllowedSenderIDs       []int64
	APIBaseURL             string // override for tests / offline use
}

// FromEnv parses the MULTICA_TELEGRAM_* env vars via the caller-
// provided getenv function (usually os.Getenv). Returns Enabled=false
// when the master flag is not "true" — callers should short-circuit
// on !cfg.Enabled without inspecting the other fields.
//
// Validation happens only when Enabled=true. Missing/malformed fields
// then return an error explaining exactly which env var is bad, so
// the operator gets a useful startup log line.
func FromEnv(getenv func(string) string) (Env, error) {
	cfg := Env{}
	cfg.Enabled = strings.EqualFold(strings.TrimSpace(getenv("MULTICA_TELEGRAM_ENABLED")), "true")
	if !cfg.Enabled {
		return cfg, nil
	}
	cfg.BotToken = strings.TrimSpace(getenv("MULTICA_TELEGRAM_BOT_TOKEN"))
	if cfg.BotToken == "" {
		return cfg, errors.New("MULTICA_TELEGRAM_BOT_TOKEN required when MULTICA_TELEGRAM_ENABLED=true")
	}
	chatRaw := strings.TrimSpace(getenv("MULTICA_TELEGRAM_CHAT_ID"))
	if chatRaw == "" {
		return cfg, errors.New("MULTICA_TELEGRAM_CHAT_ID required when MULTICA_TELEGRAM_ENABLED=true")
	}
	chatID, err := strconv.ParseInt(chatRaw, 10, 64)
	if err != nil {
		return cfg, errors.New("MULTICA_TELEGRAM_CHAT_ID must be a signed integer (supergroup ids are negative)")
	}
	cfg.ChatID = chatID
	cfg.WorkspaceID = strings.TrimSpace(getenv("MULTICA_TELEGRAM_WORKSPACE_ID"))
	if cfg.WorkspaceID == "" {
		return cfg, errors.New("MULTICA_TELEGRAM_WORKSPACE_ID required when MULTICA_TELEGRAM_ENABLED=true")
	}
	cfg.DefaultAuthorID = strings.TrimSpace(getenv("MULTICA_TELEGRAM_DEFAULT_AUTHOR_ID"))
	if senders := strings.TrimSpace(getenv("MULTICA_TELEGRAM_ALLOWED_SENDER_IDS")); senders != "" {
		for _, s := range strings.Split(senders, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return cfg, errors.New("MULTICA_TELEGRAM_ALLOWED_SENDER_IDS entry not an integer: " + s)
			}
			cfg.AllowedSenderIDs = append(cfg.AllowedSenderIDs, id)
		}
	}
	cfg.APIBaseURL = strings.TrimSpace(getenv("MULTICA_TELEGRAM_API_BASE_URL"))
	return cfg, nil
}

// LogSummary emits a single startup log line describing the bridge
// state. Never prints the bot token or full envvar values — only
// booleans + counts + non-secret ids that operators need to confirm
// the bridge came up wired to the right group.
func (e Env) LogSummary(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if !e.Enabled {
		logger.Info("telegramoutbound: disabled (set MULTICA_TELEGRAM_ENABLED=true to opt in)")
		return
	}
	logger.Info("telegramoutbound: enabled",
		"chat_id", e.ChatID,
		"workspace_id", e.WorkspaceID,
		"allowed_senders", len(e.AllowedSenderIDs),
		"api_override", e.APIBaseURL != "",
	)
}
