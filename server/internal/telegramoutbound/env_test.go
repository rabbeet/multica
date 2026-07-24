package telegramoutbound

import (
	"strings"
	"testing"
)

func envFn(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_DisabledByDefault(t *testing.T) {
	cfg, err := FromEnv(envFn(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Enabled {
		t.Errorf("Enabled must default false")
	}
}

func TestFromEnv_EnabledRequiresBotToken(t *testing.T) {
	_, err := FromEnv(envFn(map[string]string{
		"MULTICA_TELEGRAM_ENABLED": "true",
	}))
	if err == nil || !strings.Contains(err.Error(), "BOT_TOKEN") {
		t.Errorf("want BOT_TOKEN error, got %v", err)
	}
}

func TestFromEnv_EnabledRequiresChatID(t *testing.T) {
	_, err := FromEnv(envFn(map[string]string{
		"MULTICA_TELEGRAM_ENABLED":   "true",
		"MULTICA_TELEGRAM_BOT_TOKEN": "T",
	}))
	if err == nil || !strings.Contains(err.Error(), "CHAT_ID") {
		t.Errorf("want CHAT_ID error, got %v", err)
	}
}

func TestFromEnv_ChatIDMustBeInt(t *testing.T) {
	_, err := FromEnv(envFn(map[string]string{
		"MULTICA_TELEGRAM_ENABLED":   "true",
		"MULTICA_TELEGRAM_BOT_TOKEN": "T",
		"MULTICA_TELEGRAM_CHAT_ID":   "not-a-number",
	}))
	if err == nil {
		t.Errorf("want parse error, got nil")
	}
}

func TestFromEnv_HappyPath(t *testing.T) {
	cfg, err := FromEnv(envFn(map[string]string{
		"MULTICA_TELEGRAM_ENABLED":            "true",
		"MULTICA_TELEGRAM_BOT_TOKEN":          "SECRET",
		"MULTICA_TELEGRAM_CHAT_ID":            "-1001234567",
		"MULTICA_TELEGRAM_WORKSPACE_ID":       "ws-1",
		"MULTICA_TELEGRAM_ALLOWED_SENDER_IDS": "111, 222 , 333",
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("Enabled should be true")
	}
	if cfg.BotToken != "SECRET" {
		t.Errorf("BotToken=%q", cfg.BotToken)
	}
	if cfg.ChatID != -1001234567 {
		t.Errorf("ChatID=%d", cfg.ChatID)
	}
	if cfg.WorkspaceID != "ws-1" {
		t.Errorf("WorkspaceID=%q", cfg.WorkspaceID)
	}
	if got := cfg.AllowedSenderIDs; len(got) != 3 || got[0] != 111 || got[2] != 333 {
		t.Errorf("AllowedSenderIDs=%v", got)
	}
}

func TestFromEnv_BadAllowedSender(t *testing.T) {
	_, err := FromEnv(envFn(map[string]string{
		"MULTICA_TELEGRAM_ENABLED":            "true",
		"MULTICA_TELEGRAM_BOT_TOKEN":          "T",
		"MULTICA_TELEGRAM_CHAT_ID":            "-1",
		"MULTICA_TELEGRAM_WORKSPACE_ID":       "ws",
		"MULTICA_TELEGRAM_ALLOWED_SENDER_IDS": "111,not-a-number",
	}))
	if err == nil || !strings.Contains(err.Error(), "ALLOWED_SENDER_IDS") {
		t.Errorf("want ALLOWED_SENDER_IDS error, got %v", err)
	}
}
