package cascade

import "testing"

func TestLookupIssueIdentifier_TitleMatches(t *testing.T) {
	cases := []struct {
		name, title, want string
	}{
		{"basic", "[PUL-102] feat(x): y", "PUL-102"},
		{"lowercase", "[pul-99] fix(z): w", "PUL-99"},
		{"mul prefix", "[MUL-1] foo", "MUL-1"},
		{"long prefix", "[PROJECT-12345] thing", "PROJECT-12345"},
		{"deep reference not matched", "fix typo in PUL-99 ref", ""},
		{"no brackets", "PUL-12 feat: x", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LookupIssueIdentifier(tc.title, "")
			if got != tc.want {
				t.Errorf("LookupIssueIdentifier(%q, \"\") = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestLookupIssueIdentifier_BranchFallback(t *testing.T) {
	cases := []struct {
		name, branch, want string
	}{
		// Pre-PUL-216 cases — canonical dash-separated shape.
		{"basic agent branch", "agent-1/pul-102-foo", "PUL-102"},
		{"multi-digit agent", "agent-2/pul-99-bar", "PUL-99"},
		{"alphanumeric agent id", "agent-a1b/MUL-7-thing", "MUL-7"},
		{"slash slug", "agent-1/pul-1/x", "PUL-1"},
		{"underscore separator", "agent-3/pul-42_xy", "PUL-42"},
		{"feat branch not matched (not agent-)", "feat/pul-7-foo", ""},
		{"main not matched", "main", ""},
		{"empty", "", ""},

		// PUL-216: regression cases for the prod-miss branches.
		// All `pul196-pr5.x-...` rows must resolve to PUL-196 (not
		// PR-5 from the slug). The lazy `??` on the sub-classifier
		// group is load-bearing — greedy `?` would resolve to PR-5.
		// See branchRegex doc-comment.
		{"pulN no-dash + dotted slug", "agent-1/pul196-pr5.1-refresh-dispatch", "PUL-196"},
		{"pulN no-dash + dotted slug (.5)", "agent-1/pul196-pr5.5-weighted-strategies", "PUL-196"},
		{"pulN no-dash + dotted slug (.3)", "agent-1/pul196-pr5.3-g1-acceptance", "PUL-196"},
		{"pulN no-dash + dotted slug (.2)", "agent-1/pul196-pr5.2-refresh-trigger", "PUL-196"},
		{"pulN no-dash + plain slug", "agent-1/pul193-phc-g1-measurement", "PUL-193"},
		{"sub-classifier u1-pulN", "agent-1/u1-pul212-drop-failure-events", "PUL-212"},
		{"canonical pul-N preserved", "agent-2/pul-209-rev4-v2", "PUL-209"},

		// PUL-216: negative cases.
		{"alpha-only branch (no digits)", "agent-1/refactor-something", ""},
		// `u198-publish-plan-enroll`: the `u198-` is consumed as a
		// sub-classifier, then `publish-plan-enroll` has no digits
		// to form a prefix-N pair — no match.
		{"unit-id branch with no PUL ref", "agent-1/u198-publish-plan-enroll", ""},
		// `pr5-only-rev`: matches as PR-5 (no known workspace prefix
		// "PR"). Acceptable false-positive — downstream IssueLoader
		// returns ErrIssueNotFound and the row scope_filter_skips
		// with reason "issue not found: PR-5", strictly safer than
		// the pre-PUL-216 "no PUL-N identifier" reason for real
		// PUL rows.
		{"two-letter non-issue prefix", "agent-1/pr5-only-rev", "PR-5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LookupIssueIdentifier("", tc.branch)
			if got != tc.want {
				t.Errorf("LookupIssueIdentifier(\"\", %q) = %q, want %q", tc.branch, got, tc.want)
			}
		})
	}
}

func TestLookupIssueIdentifier_TitlePrefersBranch(t *testing.T) {
	// When both match, title wins (primary). Pin this so a future
	// refactor doesn't accidentally swap priority.
	got := LookupIssueIdentifier("[PUL-1] feat", "agent-1/pul-2-x")
	if got != "PUL-1" {
		t.Errorf("title should be primary: got %q, want PUL-1", got)
	}
}

func TestLookupIssueIdentifier_TitleEditedBranchSurvives(t *testing.T) {
	// G4 scenario: user edited the title and dropped [PUL-N]; branch
	// is the fallback that keeps the lookup working.
	got := LookupIssueIdentifier("now without prefix", "agent-1/pul-42-x")
	if got != "PUL-42" {
		t.Errorf("branch fallback failed: got %q, want PUL-42", got)
	}
}

func TestInScope(t *testing.T) {
	cases := []struct {
		name, title, branch string
		want                bool
	}{
		{"agent branch alone", "manual title", "agent-1/x", true},
		{"bracket title alone", "[PUL-1] x", "feat/y", true},
		{"both present", "[PUL-1] x", "agent-2/pul-1-x", true},
		{"manual PR", "fix login", "feat/login-redirect", false},
		{"empty inputs", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InScope(tc.title, tc.branch); got != tc.want {
				t.Errorf("InScope(%q, %q) = %v, want %v", tc.title, tc.branch, got, tc.want)
			}
		})
	}
}
