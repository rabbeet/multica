package pdfexport

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Render tests for the pdfexport package (PUL-266).
//
// PR-1 ships structural / substring assertions rather than full-file
// goldens so the corpus stays maintainable during the rapid iteration
// expected before the PDF pipeline lands in PR-2. Each case carries an
// explicit list of "must contain" and "must not contain" markers tied
// to a specific behavior or finding from the eng review.
//
// Why not full goldens here:
//   - CSS in template.html is still being tuned for PDF; touching one
//     rule shouldn't ripple into 11 broken golden files.
//   - Substring assertions read better in code review — each case
//     documents the behavior it pins.
//   - PR-2 will add full-file goldens once the gotenberg pipeline
//     freezes the HTML surface.

// helper: build a baseline header for fixtures.
func fixtureHeader() TicketHeader {
	t0 := time.Date(2026, 6, 2, 5, 30, 0, 0, time.UTC)
	return TicketHeader{
		Identifier:   "PUL-266",
		Title:        "Экспорт в PDF",
		ProjectTitle: "Multica",
		Status:       "in_progress",
		Priority:     "none",
		CreatorName:  "Vadim",
		CreatedAt:    t0,
		UpdatedAt:    t0.Add(15 * time.Minute),
		URL:          "https://multica.ai/issues/PUL-266",
	}
}

func TestRenderHTML_MarkdownBasics(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		"Текст с **bold** и *italic* и ~~strike~~.",
		"",
		"| col1 | col2 |",
		"| --- | --- |",
		"| a    | b    |",
		"",
		"```go",
		"package main",
		"func main() {}",
		"```",
		"",
		"hard line",
		"break here",
	}, "\n")
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt.Add(time.Minute),
				Body:      body,
			},
		},
	}
	out := mustRender(t, doc)

	mustContain(t, out,
		"<strong>bold</strong>",
		"<em>italic</em>",
		"<del>strike</del>",
		"<table",
		"<th",
		"<td",
		"<pre><code", // fenced code block
		"package main",
		"hard line<br", // hard wrap on raw newline
	)
}

func TestRenderHTML_MixedScripts(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt,
				Body:      "Русский + English + 中文 + 🚀 + emoji 👍",
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out, "Русский", "English", "中文", "🚀", "👍")
}

func TestRenderHTML_Mentions(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt,
				Body: strings.Join([]string{
					"cc [@Vadim](mention://member/a97695dd-8838-4828-b862-7497220cd6a4)",
					"and [@Agent-1](mention://agent/15a64543-daee-49f2-861c-b3ec121c9d7e)",
					"see [PUL-266](mention://issue/9c18da81-dd3f-486a-98cc-a5020f1e91be)",
				}, "\n\n"),
			},
		},
	}
	out := mustRender(t, doc)

	mustContain(t, out,
		`class="mention mention-member"`,
		`data-mention-type="member"`,
		`data-mention-id="a97695dd-8838-4828-b862-7497220cd6a4"`,
		`>@Vadim<`,
		`class="mention mention-agent"`,
		`data-mention-type="agent"`,
		`class="mention mention-issue"`,
		`data-mention-id="9c18da81-dd3f-486a-98cc-a5020f1e91be"`,
		`>PUL-266<`,
	)

	// Confirm bluemonday didn't strip our custom classes.
	mustNotContain(t, out,
		`mention://member`, // raw scheme should not survive into HTML
		`mention://agent`,
		`mention://issue`,
	)
}

func TestRenderHTML_FileCardLink(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt,
				Body:      "see [report.pdf](upload://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee)",
			},
		},
	}
	out := mustRender(t, doc)

	mustContain(t, out,
		`class="file-card file-card-generic"`,
		`data-attachment-id="aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`,
		`📎`,
		"report.pdf",
	)
	mustNotContain(t, out, `upload://`)
}

func TestRenderHTML_DeletedReply(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:          "c1",
				ActorName:   "Vadim",
				CreatedAt:   fixtureHeader().CreatedAt,
				Body:        "top-level",
				IndentLevel: 0,
			},
			CommentItem{
				ID:          "c2",
				ParentID:    "c1",
				ActorName:   "Agent-1",
				CreatedAt:   fixtureHeader().CreatedAt.Add(time.Minute),
				Body:        "ignored when Deleted=true",
				Deleted:     true,
				IndentLevel: 1,
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out,
		`comment-deleted`,
		`(deleted comment)`,
		`thread-indent-1`,
	)
	mustNotContain(t, out, "ignored when Deleted=true")
}

func TestRenderHTML_ReactionsAndEditedPill(t *testing.T) {
	t.Parallel()
	t0 := fixtureHeader().CreatedAt
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: t0,
				UpdatedAt: t0.Add(5 * time.Minute), // → edited pill
				Body:      "thanks",
				Reactions: []Reaction{
					{Emoji: "👍", Count: 3, ActorNames: []string{"Vadim", "Agent-1", "Agent-2"}},
				},
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out,
		`edited-pill`,
		`>edited<`,
		`reactions-line`,
		`👍 ×3`,
		`Vadim, Agent-1, Agent-2`,
	)
}

func TestRenderHTML_ThreadMode(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:         ModeThread,
		ThreadRootID: "c1",
		Header:       fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt,
				Body:      "thread root",
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out,
		"PUL-266 · thread",         // header marker
		"Thread export of",         // footer copy
		`href="https://multica.ai/issues/PUL-266"`,
	)
}

func TestRenderHTML_EmptyIssue(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		// no Description, no Items
	}
	out := mustRender(t, doc)
	mustContain(t, out,
		"empty-issue",
		"No content yet",
	)
}

func TestRenderHTML_ActivityEntry(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			ActivityItem{
				ID:        "a1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt.Add(time.Minute),
				Action:    "changed status: todo → in_progress",
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out,
		`activity-row`,
		`activity-action`,
		`changed status: todo → in_progress`,
	)
}

// TestRenderHTML_StripsRawScript pins finding 7A — bluemonday must
// strip <script> from comment bodies, even when goldmark would
// otherwise emit them via WithUnsafe + rehype-raw-style passthrough.
func TestRenderHTML_StripsRawScript(t *testing.T) {
	t.Parallel()
	doc := Document{
		Mode:   ModeFull,
		Header: fixtureHeader(),
		Items: []TimelineItem{
			CommentItem{
				ID:        "c1",
				ActorName: "Vadim",
				CreatedAt: fixtureHeader().CreatedAt,
				Body:      "safe text<script>alert('xss')</script>more safe",
			},
		},
	}
	out := mustRender(t, doc)
	mustContain(t, out, "safe text", "more safe")
	mustNotContain(t, out, "<script", "alert(", "xss")
}

func mustRender(t *testing.T, doc Document) string {
	t.Helper()
	out, err := RenderHTML(context.Background(), doc)
	if err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}
	return string(out)
}

func mustContain(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Errorf("output missing %q\n--- output ---\n%s\n--- end ---", n, haystack)
		}
	}
}

func mustNotContain(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			t.Errorf("output unexpectedly contains %q\n--- output ---\n%s\n--- end ---", n, haystack)
		}
	}
}
