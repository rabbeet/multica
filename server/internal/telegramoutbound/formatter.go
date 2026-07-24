package telegramoutbound

import (
	"fmt"
	"regexp"
	"strings"
)

// Multica renders in-app mentions as markdown links whose href starts
// with mention://. These URLs are non-clickable in Telegram (Bot API
// mangles unknown schemes) and — worse — leak internal UUIDs into the
// rendered text if we let MarkdownV2 pass through. Convert them to
// plain "@Name" / "PUL-N" before shipping.
//
// The regex is intentionally tolerant: it accepts any label text
// (including spaces and unicode) and any mention path.
var mentionRE = regexp.MustCompile(`\[([^\]]+)\]\(mention://[^)]+\)`)

// FormatMessage builds the plain-text message body for a single
// comment. Shape:
//
//	<PUL-N> · <author>
//
//	<content with @mention → @name unwrap>
//
// The header line grounds the message when several topics scroll past
// each other on the phone; the blank line separates it from the body
// so `disable_web_page_preview` still lets a leading URL auto-link.
func FormatMessage(issueIdentifier, authorLabel, content string) string {
	var b strings.Builder
	if issueIdentifier != "" || authorLabel != "" {
		if issueIdentifier != "" {
			b.WriteString(issueIdentifier)
		}
		if issueIdentifier != "" && authorLabel != "" {
			b.WriteString(" · ")
		}
		if authorLabel != "" {
			b.WriteString(authorLabel)
		}
		b.WriteString("\n\n")
	}
	b.WriteString(unwrapMentions(content))
	return b.String()
}

// FormatTopicName builds the forum-topic title. Same shape as
// FormatMessage's header but issue title carries meaning too.
// Truncation happens in the client (Bot API 128-char cap).
func FormatTopicName(issueIdentifier, title string) string {
	switch {
	case issueIdentifier != "" && title != "":
		return fmt.Sprintf("%s · %s", issueIdentifier, title)
	case issueIdentifier != "":
		return issueIdentifier
	case title != "":
		return title
	default:
		return "multica issue"
	}
}

// unwrapMentions replaces every "[label](mention://foo/bar)" with
// "label". Leaves other markdown intact so a comment written as
// pure markdown looks reasonable in Telegram (code blocks, bold, etc.
// render as plain text with visible markers, which is acceptable in
// v1 — an actual MarkdownV2 converter is deferred to TODO-1).
func unwrapMentions(content string) string {
	return mentionRE.ReplaceAllString(content, "$1")
}
