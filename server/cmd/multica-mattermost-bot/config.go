package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config bundles every runtime knob the mmbot daemon reads from the
// environment. Production loads these via systemd's EnvironmentFile after
// the op-read-env.sh ExecStartPre hook has resolved 1Password secrets into
// a tmpfs env file. Tests construct it directly.
//
// Required vs optional fields documented inline. loadConfig surfaces a single
// composite error listing every missing required key, so an operator sees
// the full set in one journalctl line rather than playing whack-a-mole.
type Config struct {
	// MM_HOST — base URL of the Mattermost instance, e.g.
	// "https://mattermost.clickavia.com". REQUIRED.
	MMHost string
	// MM_BOT_TOKEN — Personal Access Token of the bot account. NEVER logged.
	// REQUIRED.
	MMBotToken string
	// MM_BOT_USER_ID — the bot's own user_id in MM, for the echo-loop
	// filter. REQUIRED.
	MMBotUserID string
	// MM_ALLOWED_CHANNELS — CSV of MM channel_ids the bot watches.
	// REQUIRED (at least one).
	AllowedChannels []string
	// MM_ALLOWED_USER_IDS — CSV of MM user_ids whose posts trigger the bot.
	// REQUIRED (at least one).
	AllowedUserIDs []string

	// MMBOT_STATE_DB_PATH — local SQLite path. Default
	// "/var/lib/multica-mattermost-bot/state.db".
	StateDBPath string
	// MMBOT_ASSIGNEE_AGENT_ID — multica agent UUID that new issues are
	// assigned to (e.g. the agent-2 UUID for the marimo workflow).
	// REQUIRED. Embedded in multicacli.Client.AssigneeAgentID.
	AssigneeAgentID string
	// MMBOT_AGENT_AUTHOR_ID — the multica author UUID whose comments
	// flow back into MM with the "agent-1 ↪ marimo-pair" author override.
	// Optional. When unset, all forwarded comments use the generic
	// "agent" label.
	AgentAuthorID string

	// MARIMO_LOCAL_URL — local marimo server, default
	// "http://127.0.0.1:2718".
	MarimoLocalURL string
	// MARIMO_TAILNET_HOSTNAME_HINT — substring used by
	// render.ExtractTailnetURL to recognise screenshot-worthy URLs.
	// Default "ts.net".
	MarimoTailnetHostHint string

	// MULTICA_BINARY — name or absolute path of the multica CLI. Default
	// "multica". Production usually leaves this unset.
	MulticaBinary string

	// OutboundPollInterval — how often the outbound loop iterates.
	// Default 5s. Mapped from MMBOT_POLL_INTERVAL (Go duration string).
	OutboundPollInterval time.Duration
}

// Defaults documented as constants so tests and runbook agree.
const (
	defaultStatePath      = "/var/lib/multica-mattermost-bot/state.db"
	defaultMarimoURL      = "http://127.0.0.1:2718"
	defaultTailnetHostHint = "ts.net"
	defaultMulticaBinary  = "multica"
	defaultPollInterval   = 5 * time.Second
)

// loadConfig pulls every knob from os.Getenv. Composite error lists ALL
// missing required fields so an operator fixes them in one pass.
func loadConfig() (Config, error) {
	c := Config{
		MMHost:                strings.TrimSpace(os.Getenv("MM_HOST")),
		MMBotToken:            os.Getenv("MM_BOT_TOKEN"),
		MMBotUserID:           strings.TrimSpace(os.Getenv("MM_BOT_USER_ID")),
		AllowedChannels:       splitCSV(os.Getenv("MM_ALLOWED_CHANNELS")),
		AllowedUserIDs:        splitCSV(os.Getenv("MM_ALLOWED_USER_IDS")),
		StateDBPath:           strings.TrimSpace(os.Getenv("MMBOT_STATE_DB_PATH")),
		AssigneeAgentID:       strings.TrimSpace(os.Getenv("MMBOT_ASSIGNEE_AGENT_ID")),
		AgentAuthorID:         strings.TrimSpace(os.Getenv("MMBOT_AGENT_AUTHOR_ID")),
		MarimoLocalURL:        strings.TrimSpace(os.Getenv("MARIMO_LOCAL_URL")),
		MarimoTailnetHostHint: strings.TrimSpace(os.Getenv("MARIMO_TAILNET_HOSTNAME_HINT")),
		MulticaBinary:         strings.TrimSpace(os.Getenv("MULTICA_BINARY")),
	}
	if s := strings.TrimSpace(os.Getenv("MMBOT_POLL_INTERVAL")); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return Config{}, fmt.Errorf("MMBOT_POLL_INTERVAL: %w", err)
		}
		c.OutboundPollInterval = d
	}

	// Defaults.
	if c.StateDBPath == "" {
		c.StateDBPath = defaultStatePath
	}
	if c.MarimoLocalURL == "" {
		c.MarimoLocalURL = defaultMarimoURL
	}
	if c.MarimoTailnetHostHint == "" {
		c.MarimoTailnetHostHint = defaultTailnetHostHint
	}
	if c.MulticaBinary == "" {
		c.MulticaBinary = defaultMulticaBinary
	}
	if c.OutboundPollInterval == 0 {
		c.OutboundPollInterval = defaultPollInterval
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	var missing []string
	if c.MMHost == "" {
		missing = append(missing, "MM_HOST")
	}
	if c.MMBotToken == "" {
		missing = append(missing, "MM_BOT_TOKEN")
	}
	if c.MMBotUserID == "" {
		missing = append(missing, "MM_BOT_USER_ID")
	}
	if len(c.AllowedChannels) == 0 {
		missing = append(missing, "MM_ALLOWED_CHANNELS")
	}
	if len(c.AllowedUserIDs) == 0 {
		missing = append(missing, "MM_ALLOWED_USER_IDS")
	}
	if c.AssigneeAgentID == "" {
		missing = append(missing, "MMBOT_ASSIGNEE_AGENT_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: required env vars unset: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.MMHost, "http://") && !strings.HasPrefix(c.MMHost, "https://") {
		return errors.New("config: MM_HOST must include http:// or https://")
	}
	return nil
}

// AllowedChannelSet returns AllowedChannels as a set for O(1) membership.
func (c Config) AllowedChannelSet() map[string]struct{} {
	m := make(map[string]struct{}, len(c.AllowedChannels))
	for _, ch := range c.AllowedChannels {
		m[ch] = struct{}{}
	}
	return m
}

// AllowedUserSet returns AllowedUserIDs as a set for O(1) membership.
func (c Config) AllowedUserSet() map[string]struct{} {
	m := make(map[string]struct{}, len(c.AllowedUserIDs))
	for _, u := range c.AllowedUserIDs {
		m[u] = struct{}{}
	}
	return m
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
