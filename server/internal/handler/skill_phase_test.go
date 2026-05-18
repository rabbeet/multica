package handler

import (
	"reflect"
	"testing"
)

// PUL-177 pure-unit coverage of the phase mapping. No DB required.

func TestDerivePhaseFromStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"backlog", "backlog"},
		{"todo", "backlog"},     // todo collapses into backlog — both pre-planning
		{"planning", "planning"},
		{"in_progress", "coding"},
		{"developing", "coding"}, // legacy alias; same chip
		{"waiting", "review"},
		{"deployed", "done"},
		{"blocked", "blocked"},
		{"cancelled", "cancelled"},
		{"", "backlog"},          // NULL issue.status (unlinked inbox item)
		{"made_up_status", "backlog"}, // forward-compat default
	}

	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			got := derivePhaseFromStatus(c.in)
			if got != c.want {
				t.Errorf("derivePhaseFromStatus(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExtractSkillCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single at start of line", "/office-hours please", []string{"office-hours"}},
		{"after whitespace", "hey /office-hours please", []string{"office-hours"}},
		{"two skills across newline", "/plan-eng-review\n/plan-design-review",
			[]string{"plan-eng-review", "plan-design-review"}},
		{"URL is not a slash command", "see https://example.com/office-hours", nil},
		{"dedup repeat in same content", "do /office-hours twice /office-hours", []string{"office-hours"}},
		{"dynamic non-priority slug", "do /qa now", []string{"qa"}},
		{"three distinct slugs", "/foo-bar /a-b /x", []string{"foo-bar", "a-b", "x"}},
		{"slash with uppercase or digit start gets rejected by regex shape",
			"/Office-Hours /9skill", []string{"9skill"}},
		// PUL-181 known limitation: regex matches inside blockquotes and
		// inline code spans. Tests freeze the current behavior so a
		// future markdown-aware swap is an explicit diff, not a silent
		// behavior change.
		{"blockquote false-positive (PUL-181)", "> /office-hours", []string{"office-hours"}},
		{"inline code false-positive (PUL-181)", "`/office-hours`", []string{"office-hours"}},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := extractSkillCandidates(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("extractSkillCandidates(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
