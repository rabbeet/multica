package handler

import (
	"testing"
	"time"
)

// TestDeriveOwnership exercises the pure-fn ownership derivation that
// drives the Inbox 3rd chip (PUL-180). It is the single test that
// validates the precedence rules locked in by /office-hours Q1 +
// /plan-eng-review A1/A5:
//
//   recipient=agent     → hidden       (A5 guard)
//   phase=done/cancelled→ hidden       (parity with PUL-177 last-skill)
//   active task wins    → "agent"      (over phase=review/blocked)
//   phase=review        → "waiting"/review
//   phase=blocked       → "waiting"/approval
//   fallback            → "me"
//
// Cases below mirror the test plan in
// `plans-multica/Multica/2026-05-18-pul-180-inbox-ownership-chip.md`
// (Test plan → unit, post-eng-review revision). Subtest names match
// the plan's case numbers so PR review can cross-reference.
func TestDeriveOwnership(t *testing.T) {
	t0 := time.Date(2026, 5, 18, 17, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Minute)
	t2 := t1.Add(5 * time.Minute)
	issueUpdated := t1

	emptyLast := lastActivity{}

	tests := []struct {
		name              string
		recipientType     string
		phase             string
		activeTask        *activeTaskRow
		last              lastActivity
		issueUpdatedAt    *time.Time
		wantOwnership     string
		wantMeta          bool   // expect non-nil meta
		wantAgentName     string // "" means must be nil
		wantReason        string
		wantSinceMatches  *time.Time
		wantSinceNil      bool
	}{
		{
			name:           "1: member/backlog/no-task → me, since=nil",
			recipientType:  "member",
			phase:          "backlog",
			last:           emptyLast,
			wantOwnership:  "me",
			wantMeta:       true,
			wantSinceNil:   true,
		},
		{
			name:           "2: member/coding/no-task → me (active-task gone)",
			recipientType:  "member",
			phase:          "coding",
			last:           lastActivity{StatusAt: &t1},
			wantOwnership:  "me",
			wantMeta:       true,
			wantSinceMatches: &t1,
		},
		{
			name:          "3: member/coding/active(running) → agent + name + since=started_at",
			recipientType: "member",
			phase:         "coding",
			activeTask: &activeTaskRow{
				AgentName: "agent-1",
				StartedAt: &t2,
				DispatchedAt: &t1,
			},
			wantOwnership:    "agent",
			wantMeta:         true,
			wantAgentName:    "agent-1",
			wantSinceMatches: &t2,
		},
		{
			name:          "4: member/coding/active(dispatched, no started_at) → agent + since=dispatched_at fallback",
			recipientType: "member",
			phase:         "coding",
			activeTask: &activeTaskRow{
				AgentName:    "agent-2",
				DispatchedAt: &t1,
			},
			wantOwnership:    "agent",
			wantMeta:         true,
			wantAgentName:    "agent-2",
			wantSinceMatches: &t1,
		},
		{
			name:             "5: member/review/no-task → waiting/reason=review/since=issue.updated_at",
			recipientType:    "member",
			phase:            "review",
			issueUpdatedAt:   &issueUpdated,
			wantOwnership:    "waiting",
			wantMeta:         true,
			wantReason:       "review",
			wantSinceMatches: &issueUpdated,
		},
		{
			name:             "6: member/blocked/no-task → waiting/reason=approval/since=issue.updated_at",
			recipientType:    "member",
			phase:            "blocked",
			issueUpdatedAt:   &issueUpdated,
			wantOwnership:    "waiting",
			wantMeta:         true,
			wantReason:       "approval",
			wantSinceMatches: &issueUpdated,
		},
		{
			name:          "7: member/review/active(running) → agent wins over waiting (Q1 precedence)",
			recipientType: "member",
			phase:         "review",
			activeTask: &activeTaskRow{
				AgentName: "agent-1",
				StartedAt: &t1,
			},
			issueUpdatedAt:   &issueUpdated,
			wantOwnership:    "agent",
			wantMeta:         true,
			wantAgentName:    "agent-1",
			wantSinceMatches: &t1,
		},
		{
			name:          "8: member/done → hidden",
			recipientType: "member",
			phase:         "done",
			wantOwnership: "",
		},
		{
			name:          "9: member/cancelled → hidden",
			recipientType: "member",
			phase:         "cancelled",
			wantOwnership: "",
		},
		{
			name:          "10a: me-since picks status_history when later than comment",
			recipientType: "member",
			phase:         "coding",
			last:          lastActivity{StatusAt: &t2, CommentAt: &t1},
			wantOwnership: "me",
			wantMeta:      true,
			wantSinceMatches: &t2,
		},
		{
			name:          "10b: me-since picks comment when later than status_history",
			recipientType: "member",
			phase:         "coding",
			last:          lastActivity{StatusAt: &t0, CommentAt: &t1},
			wantOwnership: "me",
			wantMeta:      true,
			wantSinceMatches: &t1,
		},
		{
			name:          "11: me-since fallback — both nil → since=nil",
			recipientType: "member",
			phase:         "coding",
			last:          emptyLast,
			wantOwnership: "me",
			wantMeta:      true,
			wantSinceNil:  true,
		},
		{
			// A5: server-side guard against the agent-to-agent inbox row.
			// ListInbox today only queries recipient_type='member' rows
			// so this path is defense-in-depth; future callers that pass
			// a different recipient_type still get a hidden chip.
			name:          "12 (A5): recipient=agent/coding/active(running) → hidden",
			recipientType: "agent",
			phase:         "coding",
			activeTask: &activeTaskRow{
				AgentName: "agent-1",
				StartedAt: &t1,
			},
			wantOwnership: "",
		},
		{
			// T1: SQL-side WHERE type='comment' filter is exercised by
			// the integration test; the unit-level contract is that
			// deriveOwnership uses lastActivity.CommentAt verbatim
			// (i.e. it doesn't re-filter by type since the SQL did).
			// Verified by piping a comment-only fixture and asserting
			// the since reflects exactly that timestamp.
			name:          "13 (T1): comment-only fixture pipes through verbatim",
			recipientType: "member",
			phase:         "coding",
			last:          lastActivity{CommentAt: &t1},
			wantOwnership: "me",
			wantMeta:      true,
			wantSinceMatches: &t1,
		},
		{
			// T3: archived agent (agent.archived_at NOT NULL) still has
			// a non-empty name in the schema and a non-NULL row in the
			// agent table, so the JOIN succeeds and the chip honestly
			// reports who held the ticket. The deriveOwnership input is
			// indistinguishable from a live agent — by design — so we
			// just confirm a non-empty AgentName flows through.
			name:          "14 (T3): archived agent with running task → agent + name flows through",
			recipientType: "member",
			phase:         "coding",
			activeTask: &activeTaskRow{
				AgentName: "agent-deprecated",
				StartedAt: &t1,
			},
			wantOwnership:    "agent",
			wantMeta:         true,
			wantAgentName:    "agent-deprecated",
			wantSinceMatches: &t1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotOwnership, gotMeta := deriveOwnership(
				tc.recipientType, tc.phase, tc.activeTask, tc.last, tc.issueUpdatedAt,
			)

			if gotOwnership != tc.wantOwnership {
				t.Fatalf("ownership: want %q, got %q", tc.wantOwnership, gotOwnership)
			}

			if tc.wantOwnership == "" {
				if gotMeta != nil {
					t.Fatalf("meta: want nil (chip hidden), got %+v", gotMeta)
				}
				return
			}

			if !tc.wantMeta {
				if gotMeta != nil {
					t.Fatalf("meta: want nil, got %+v", gotMeta)
				}
				return
			}
			if gotMeta == nil {
				t.Fatalf("meta: want non-nil, got nil")
			}

			if tc.wantAgentName != "" {
				if gotMeta.AgentName == nil {
					t.Fatalf("meta.agent_name: want %q, got nil", tc.wantAgentName)
				}
				if *gotMeta.AgentName != tc.wantAgentName {
					t.Fatalf("meta.agent_name: want %q, got %q", tc.wantAgentName, *gotMeta.AgentName)
				}
			} else if gotMeta.AgentName != nil {
				t.Fatalf("meta.agent_name: want nil, got %q", *gotMeta.AgentName)
			}

			if tc.wantReason != "" {
				if gotMeta.Reason == nil {
					t.Fatalf("meta.reason: want %q, got nil", tc.wantReason)
				}
				if *gotMeta.Reason != tc.wantReason {
					t.Fatalf("meta.reason: want %q, got %q", tc.wantReason, *gotMeta.Reason)
				}
			} else if gotMeta.Reason != nil {
				t.Fatalf("meta.reason: want nil, got %q", *gotMeta.Reason)
			}

			if tc.wantSinceNil {
				if gotMeta.Since != nil {
					t.Fatalf("meta.since: want nil, got %q", *gotMeta.Since)
				}
			} else if tc.wantSinceMatches != nil {
				if gotMeta.Since == nil {
					t.Fatalf("meta.since: want %s, got nil", tc.wantSinceMatches.Format(time.RFC3339))
				}
				want := tc.wantSinceMatches.Format(time.RFC3339)
				if *gotMeta.Since != want {
					t.Fatalf("meta.since: want %s, got %s", want, *gotMeta.Since)
				}
			}
		})
	}
}
