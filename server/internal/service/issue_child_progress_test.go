package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestShouldFanOutTransition covers the noise filter that gates outbox
// enqueueing. The two noisy pairs are the DecideFlip member↔agent
// waiting/in_progress round-trip; everything else should fan out (no
// fan-out happens for orphan children — that gate is the parent_issue_id
// check at the call site, not this function).
func TestShouldFanOutTransition(t *testing.T) {
	cases := []struct {
		name      string
		prev, new string
		want      bool
	}{
		{"identity_flip_is_noop", "in_progress", "in_progress", false},
		{"identity_flip_deployed", "deployed", "deployed", false},
		{"waiting_to_in_progress_is_noisy", "waiting", "in_progress", false},
		{"in_progress_to_waiting_is_noisy", "in_progress", "waiting", false},
		{"todo_to_in_progress_fans_out", "todo", "in_progress", true},
		{"in_progress_to_deployed_fans_out", "in_progress", "deployed", true},
		{"in_progress_to_cancelled_fans_out", "in_progress", "cancelled", true},
		{"in_progress_to_blocked_fans_out", "in_progress", "blocked", true},
		{"backlog_to_todo_fans_out", "backlog", "todo", true},
		{"todo_to_deployed_fans_out", "todo", "deployed", true},
		{"deployed_to_developing_fans_out", "deployed", "developing", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldFanOutTransition(tc.prev, tc.new)
			if got != tc.want {
				t.Fatalf("ShouldFanOutTransition(%q, %q) = %v, want %v",
					tc.prev, tc.new, got, tc.want)
			}
		})
	}
}

// TestMarshalChildProgressMeta ensures the discriminator is always set and
// the JSON shape matches the documented contract on packages/core/types/
// comment.ts ChildProgressMeta — clients deserialize off this so drift is
// load-bearing.
func TestMarshalChildProgressMeta(t *testing.T) {
	meta := ChildProgressMeta{
		// Intentionally omit Kind — MarshalChildProgressMeta must inject it.
		ChildIssueID:    "00000000-0000-0000-0000-000000000010",
		ChildIdentifier: "PUL-162",
		ChildTitle:      "Sentry autofix observability",
		PrevStatus:      "in_review",
		NewStatus:       "deployed",
		ActorID:         "00000000-0000-0000-0000-000000000020",
		ActorType:       "agent",
		SourceHistoryID: 42,
	}
	raw, err := MarshalChildProgressMeta(meta)
	if err != nil {
		t.Fatalf("MarshalChildProgressMeta: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded["kind"] != MetaKindChildProgress {
		t.Errorf("kind = %v, want %q (marshal must inject the discriminator)",
			decoded["kind"], MetaKindChildProgress)
	}
	if decoded["child_identifier"] != "PUL-162" {
		t.Errorf("child_identifier = %v, want PUL-162", decoded["child_identifier"])
	}
	if decoded["new_status"] != "deployed" {
		t.Errorf("new_status = %v, want deployed", decoded["new_status"])
	}
	// source_history_id arrives as float64 from json.Unmarshal into any.
	if got, _ := decoded["source_history_id"].(float64); got != 42 {
		t.Errorf("source_history_id = %v, want 42", decoded["source_history_id"])
	}
}

// TestMarshalChildProgressMeta_OptionalsOmitted: omitempty fields should be
// absent when zero so the JSON payload stays compact for the common case
// (system-triggered status changes with no real actor).
func TestMarshalChildProgressMeta_OptionalsOmitted(t *testing.T) {
	meta := ChildProgressMeta{
		ChildIssueID:    "00000000-0000-0000-0000-000000000010",
		ChildTitle:      "Anonymous transition",
		PrevStatus:      "todo",
		NewStatus:       "in_progress",
		SourceHistoryID: 1,
		// ChildIdentifier, ActorID, ActorType deliberately empty.
	}
	raw, err := MarshalChildProgressMeta(meta)
	if err != nil {
		t.Fatalf("MarshalChildProgressMeta: %v", err)
	}
	s := string(raw)
	for _, field := range []string{"child_identifier", "actor_id", "actor_type"} {
		if strings.Contains(s, `"`+field+`":`) {
			t.Errorf("expected %q to be omitted when zero, got: %s", field, s)
		}
	}
}

// TestFormatFallbackBody verifies the plain-text body stored on
// comment.content. The frontend renders a richer card from meta, but the
// fallback must be readable on its own (REST clients, search indexing,
// email/Telegram bridges when added).
func TestFormatFallbackBody(t *testing.T) {
	t.Run("with_identifier", func(t *testing.T) {
		got := FormatFallbackBody("PUL-162", "Sentry autofix observability", "deployed")
		want := "🔁 [PUL-162] «Sentry autofix observability» → deployed"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("without_identifier", func(t *testing.T) {
		got := FormatFallbackBody("", "Anonymous title", "blocked")
		want := "🔁 «Anonymous title» → blocked"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
