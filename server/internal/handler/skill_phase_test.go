package handler

import (
	"reflect"
	"strings"
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
		// PUL-181: markdown-aware stripper now removes blockquote
		// lines before regex sees them. This case was a
		// KNOWN-LIMITATION in PUL-177; flipped to nil here as the
		// regression anchor that proves the fix landed.
		{"blockquote (PUL-181 fixed)", "> /office-hours", nil},
		// Inline-code spans are stripped on remaining lines by the
		// stripper. Locked in as the documented correct behavior.
		{"inline code excluded", "`/office-hours`", nil},

		// PUL-181 added coverage — multi-line markdown constructs:
		{"multiline blockquote", "> first line\n> /office-hours second\n> third", nil},
		{"blockquote inside otherwise normal text",
			"start\n> /office-hours\nend", nil},
		{"fenced code block (backticks)", "```\n/office-hours\n```", nil},
		{"fenced code block (tildes)", "~~~\n/qa\n~~~", nil},
		{"fenced code block with language tag",
			"```bash\n/plan-eng-review\n```", nil},
		{"unclosed fence treats rest as inside", "```\n/office-hours", nil},
		// Independent-review catch: per CommonMark §4.5 a closing
		// fence may be followed only by spaces/tabs. A mid-fence
		// line like "``` info" is NOT a closer; the fence stays
		// open and a later /skill must remain stripped.
		{"fence false-closer with trailing info stays open",
			"```\nsafe code\n``` info\n/office-hours\n```", nil},
		{"fence closer with trailing spaces is a real closer",
			"```\nsafe\n```   \n/office-hours", []string{"office-hours"}},
		{"fence closer with extra fence chars also closes",
			"```\nsafe\n``````\n/office-hours", []string{"office-hours"}},
		{"prose adjacent to inline code span",
			"run /office-hours, see `/qa` for details", []string{"office-hours"}},
		{"inline code stripped on real line",
			"hey `/office-hours` lol", nil},
		{"inline code with prose skill outside",
			"before `/qa` middle /office-hours after", []string{"office-hours"}},
		{"blockquote and prose mixed",
			"> /qa quoted\nactual /office-hours", []string{"office-hours"}},

		// PUL-181 KNOWN-LIMITATION anchors — frozen v1 behavior.
		// Update these explicitly when the parser graduates to full
		// CommonMark coverage.
		{"[KNOWN-LIMITATION] lazy blockquote continuation",
			"> first\n/office-hours continuation", []string{"office-hours"}},
		{"[KNOWN-LIMITATION] indented code block (4-space)",
			"    /office-hours", []string{"office-hours"}},
		// An UNBALANCED-backtick that opens with no closer leaves the
		// rest of the line as plain text from the regex's POV. The
		// classic false-positive: a user starts a sentence with
		// "`note: see /office-hours later" but forgets the closing
		// backtick. CommonMark also treats this as not-code.
		{"[KNOWN-LIMITATION] unbalanced backticks",
			"`note see /office-hours later", []string{"office-hours"}},

		// PUL-181 D1A: content size guard.
		{"content size guard skips large input",
			strings.Repeat("a ", 60_000) + " /office-hours", nil},
		{"content just under guard still detects",
			strings.Repeat("a ", 100) + " /qa", []string{"qa"}},
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
