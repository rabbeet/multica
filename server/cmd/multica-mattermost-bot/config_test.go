package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_HappyPath(t *testing.T) {
	withEnv(t, map[string]string{
		"MM_HOST":                       "https://mm.example.com",
		"MM_BOT_TOKEN":                  "secret-token-xxx",
		"MM_BOT_USER_ID":                "bot-uuid",
		"MM_ALLOWED_CHANNELS":           "chan-a, chan-b",
		"MM_ALLOWED_USER_IDS":           "user-1,user-2 , user-3",
		"MMBOT_ASSIGNEE_AGENT_ID":       "agent-uuid",
		"MMBOT_AGENT_AUTHOR_ID":         "agent-author-uuid",
		"MARIMO_LOCAL_URL":              "http://127.0.0.1:2718",
		"MARIMO_TAILNET_HOSTNAME_HINT":  "ts.net",
		"MMBOT_STATE_DB_PATH":           "/tmp/state.db",
		"MMBOT_POLL_INTERVAL":           "10s",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.MMHost != "https://mm.example.com" {
		t.Errorf("MMHost = %q", cfg.MMHost)
	}
	if len(cfg.AllowedChannels) != 2 || cfg.AllowedChannels[0] != "chan-a" || cfg.AllowedChannels[1] != "chan-b" {
		t.Errorf("AllowedChannels = %v", cfg.AllowedChannels)
	}
	if len(cfg.AllowedUserIDs) != 3 {
		t.Errorf("AllowedUserIDs = %v", cfg.AllowedUserIDs)
	}
	if cfg.OutboundPollInterval != 10*time.Second {
		t.Errorf("poll interval = %v", cfg.OutboundPollInterval)
	}
	if cfg.AgentAuthorID != "agent-author-uuid" {
		t.Errorf("AgentAuthorID = %q", cfg.AgentAuthorID)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	withEnv(t, map[string]string{
		"MM_HOST":                 "https://x",
		"MM_BOT_TOKEN":            "t",
		"MM_BOT_USER_ID":          "u",
		"MM_ALLOWED_CHANNELS":     "c",
		"MM_ALLOWED_USER_IDS":     "u",
		"MMBOT_ASSIGNEE_AGENT_ID": "a",
	})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.StateDBPath != defaultStatePath {
		t.Errorf("StateDBPath default = %q", cfg.StateDBPath)
	}
	if cfg.MarimoLocalURL != defaultMarimoURL {
		t.Errorf("MarimoLocalURL default = %q", cfg.MarimoLocalURL)
	}
	if cfg.MarimoTailnetHostHint != defaultTailnetHostHint {
		t.Errorf("hostHint default = %q", cfg.MarimoTailnetHostHint)
	}
	if cfg.MulticaBinary != defaultMulticaBinary {
		t.Errorf("MulticaBinary default = %q", cfg.MulticaBinary)
	}
	if cfg.OutboundPollInterval != defaultPollInterval {
		t.Errorf("poll interval default = %v", cfg.OutboundPollInterval)
	}
}

func TestLoadConfig_MissingRequiredListedTogether(t *testing.T) {
	withEnv(t, map[string]string{
		// Nothing set.
	})
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error on missing required env")
	}
	required := []string{
		"MM_HOST", "MM_BOT_TOKEN", "MM_BOT_USER_ID",
		"MM_ALLOWED_CHANNELS", "MM_ALLOWED_USER_IDS",
		"MMBOT_ASSIGNEE_AGENT_ID",
	}
	for _, key := range required {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error missing %s — composite report should list every gap; got %v", key, err)
		}
	}
}

func TestLoadConfig_MMHostMustBeHTTPish(t *testing.T) {
	withEnv(t, map[string]string{
		"MM_HOST":                 "mattermost.example.com", // missing scheme
		"MM_BOT_TOKEN":            "t",
		"MM_BOT_USER_ID":          "u",
		"MM_ALLOWED_CHANNELS":     "c",
		"MM_ALLOWED_USER_IDS":     "u",
		"MMBOT_ASSIGNEE_AGENT_ID": "a",
	})
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("expected http-prefix error, got %v", err)
	}
}

func TestLoadConfig_RejectsBadDuration(t *testing.T) {
	withEnv(t, map[string]string{
		"MM_HOST":                 "https://x",
		"MM_BOT_TOKEN":            "t",
		"MM_BOT_USER_ID":          "u",
		"MM_ALLOWED_CHANNELS":     "c",
		"MM_ALLOWED_USER_IDS":     "u",
		"MMBOT_ASSIGNEE_AGENT_ID": "a",
		"MMBOT_POLL_INTERVAL":     "not-a-duration",
	})
	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error on bad duration")
	}
}

func TestSplitCSV_StripsAndIgnoresEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
		{"", nil},
		{"   ", nil},
		{"only", []string{"only"}},
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if !equalSlices(got, c.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAllowedSetsAreLookupable(t *testing.T) {
	cfg := Config{
		AllowedChannels: []string{"c1", "c2"},
		AllowedUserIDs:  []string{"u1", "u2"},
	}
	cs := cfg.AllowedChannelSet()
	if _, ok := cs["c1"]; !ok {
		t.Error("c1 missing from channel set")
	}
	if _, ok := cs["c-x"]; ok {
		t.Error("c-x unexpectedly present")
	}
	us := cfg.AllowedUserSet()
	if _, ok := us["u2"]; !ok {
		t.Error("u2 missing from user set")
	}
}

// withEnv replaces os env entirely for the duration of t. Other env keys
// don't interfere with loadConfig's MMBOT_* reads, but isolating is safest.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	keys := []string{
		"MM_HOST", "MM_BOT_TOKEN", "MM_BOT_USER_ID",
		"MM_ALLOWED_CHANNELS", "MM_ALLOWED_USER_IDS",
		"MMBOT_STATE_DB_PATH", "MMBOT_ASSIGNEE_AGENT_ID",
		"MMBOT_AGENT_AUTHOR_ID", "MARIMO_LOCAL_URL",
		"MARIMO_TAILNET_HOSTNAME_HINT", "MULTICA_BINARY",
		"MMBOT_POLL_INTERVAL",
	}
	saved := map[string]string{}
	for _, k := range keys {
		saved[k] = osGetenv(k)
		osUnsetenv(k)
	}
	for k, v := range kv {
		osSetenv(k, v)
	}
	t.Cleanup(func() {
		for k, v := range saved {
			if v == "" {
				osUnsetenv(k)
			} else {
				osSetenv(k, v)
			}
		}
	})
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
