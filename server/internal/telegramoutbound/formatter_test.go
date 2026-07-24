package telegramoutbound

import (
	"strings"
	"testing"
)

func TestUnwrapMentions_MemberMention(t *testing.T) {
	in := "hi [@Vadim](mention://member/abcdef-1234) look at this"
	out := unwrapMentions(in)
	want := "hi @Vadim look at this"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestUnwrapMentions_IssueMention(t *testing.T) {
	in := "linked to [PUL-42](mention://issue/some-uuid) yesterday"
	out := unwrapMentions(in)
	want := "linked to PUL-42 yesterday"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestUnwrapMentions_MultipleInOneParagraph(t *testing.T) {
	in := "[@A](mention://member/x) told [@B](mention://member/y) about [PUL-1](mention://issue/z)."
	out := unwrapMentions(in)
	want := "@A told @B about PUL-1."
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestUnwrapMentions_LeavesPlainMarkdownAlone(t *testing.T) {
	in := "here is `code` and **bold** and a real [link](https://example.com)"
	out := unwrapMentions(in)
	if out != in {
		t.Errorf("plain markdown should pass through unchanged: got %q", out)
	}
}

func TestUnwrapMentions_UnicodeLabel(t *testing.T) {
	in := "[@Вадим](mention://member/1) — привет"
	out := unwrapMentions(in)
	want := "@Вадим — привет"
	if out != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestFormatMessage_Full(t *testing.T) {
	body := FormatMessage("PUL-479", "Vadim", "Fixed [@bot](mention://agent/x)")
	if !strings.HasPrefix(body, "PUL-479 · Vadim\n\n") {
		t.Errorf("header wrong: %q", body)
	}
	if !strings.HasSuffix(body, "Fixed @bot") {
		t.Errorf("body/unwrap wrong: %q", body)
	}
}

func TestFormatMessage_NoHeaderIfBothEmpty(t *testing.T) {
	body := FormatMessage("", "", "hi")
	if body != "hi" {
		t.Errorf("bare-content message must have no header: %q", body)
	}
}

func TestFormatMessage_OnlyPUL(t *testing.T) {
	body := FormatMessage("PUL-1", "", "hello")
	if body != "PUL-1\n\nhello" {
		t.Errorf("got %q", body)
	}
}

func TestFormatTopicName(t *testing.T) {
	cases := []struct{ id, title, want string }{
		{"PUL-1", "Fix bug", "PUL-1 · Fix bug"},
		{"PUL-1", "", "PUL-1"},
		{"", "Just title", "Just title"},
		{"", "", "multica issue"},
	}
	for _, tc := range cases {
		if got := FormatTopicName(tc.id, tc.title); got != tc.want {
			t.Errorf("FormatTopicName(%q,%q)=%q want %q", tc.id, tc.title, got, tc.want)
		}
	}
}
