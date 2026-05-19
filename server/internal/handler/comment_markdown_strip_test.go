package handler

import (
	"strings"
	"testing"
)

// PUL-181 unit coverage of the markdown stripper. No DB required.

func TestStripMarkdownNonText(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantContains []string
		wantStripped []string
	}{
		{"empty", "", nil, nil},
		{"plain prose untouched",
			"hello world", []string{"hello world"}, nil},
		{"blockquote stripped to whitespace",
			"> quoted", nil, []string{"quoted"}},
		{"fenced block (backticks) stripped",
			"```\nfoo\n```", nil, []string{"foo"}},
		{"tilde fence stripped",
			"~~~\nfoo\n~~~", nil, []string{"foo"}},
		{"fence with language tag stripped",
			"```go\npkg\n```", nil, []string{"pkg"}},
		{"inline code stripped",
			"see `code` here", []string{"see", "here"}, []string{"code"}},
		{"multi-paragraph prose preserves prose, drops blockquote",
			"prose one\n\n> quoted\n\nprose two",
			[]string{"prose one", "prose two"}, []string{"quoted"}},
		{"multi-line fence preserved as block-strip",
			"text before\n```\ncode\nmore code\n```\ntext after",
			[]string{"text before", "text after"}, []string{"code", "more code"}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := stripMarkdownNonText(c.in)
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("stripMarkdownNonText(%q) lost %q; output=%q", c.in, want, got)
				}
			}
			for _, gone := range c.wantStripped {
				if strings.Contains(got, gone) {
					t.Errorf("stripMarkdownNonText(%q) kept %q; output=%q", c.in, gone, got)
				}
			}
		})
	}
}

// Length preservation is a load-bearing property — downstream
// offset-bearing tooling (Sentry breadcrumbs, future column-aware
// diagnostics) assumes char counts stay aligned with the original.
func TestStripMarkdownNonTextPreservesLength(t *testing.T) {
	inputs := []string{
		"",
		"plain prose",
		"> blockquote line",
		"```\nfenced\n```",
		"see `inline` text",
		"mixed > q and `inline` together",
		"prose\n\n> q\n\n```\ncode\n```\n\nmore prose",
		"line\r\n> quoted\r\nline",
		"```\r\ncode\r\n```\r\nafter",
	}
	for _, in := range inputs {
		out := stripMarkdownNonText(in)
		if len(out) != len(in) {
			t.Errorf("length mismatch: in=%d out=%d for %q", len(in), len(out), in)
		}
	}
}

// Dedicated coverage of the manual backtick-pair algorithm. Go RE2
// has no backreferences, so equal-count enforcement is algorithmic
// rather than regex — needs its own assertions.
func TestStripInlineCodeSpansPairing(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single backtick pair", "a `b` c", "a     c"},
		{"double backtick pair", "a ``b`` c", "a       c"},
		{"mismatched counts left untouched", "a `b`` c", "a `b`` c"},
		{"two independent pairs", "`x` and `y`", "    and    "},
		{"pair plus unbalanced trailing", "`x` `y", "    `y"},
		{"outer pair wraps inner (CommonMark)",
			"``a `b` c``", "           "},
		{"no backticks unchanged", "no code here", "no code here"},
		{"single unpaired backtick unchanged", "lone ` tick", "lone ` tick"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := stripInlineCodeSpans(c.in)
			if got != c.want {
				t.Errorf("stripInlineCodeSpans(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
