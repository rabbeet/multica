package pdfexport

import (
	"bytes"
	"context"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// MaxHTMLSize is the hard cap on rendered HTML before we refuse to
// hand it off to the PDF service. Tickets that would produce more
// than this much HTML almost always benefit from a thread-mode
// export instead — the cap exists primarily as an OOM-guard for the
// gotenberg sidecar (finding 12A from /plan-eng-review).
//
// The /export.pdf handler maps this overflow to HTTP 413 with a
// message instructing the caller to use the per-thread export.
const MaxHTMLSize = 50 * 1024 * 1024 // 50 MiB

// ErrHTMLTooLarge is returned by RenderHTML when the assembled
// document would exceed MaxHTMLSize. Handlers should translate this
// to HTTP 413.
var ErrHTMLTooLarge = fmt.Errorf("pdfexport: rendered HTML exceeds %d bytes", MaxHTMLSize)

// RenderHTML turns a Document into a single self-contained HTML
// document. The returned bytes are the input to the PR-2 gotenberg
// client; the PR-1 handler returns them directly behind a
// MULTICA_DEV=1 gate at /api/issues/{id}/export.html?_dev=1 for
// visual review.
//
// The renderer is stateless and safe for concurrent calls. It does
// no I/O — attachments are expected to be pre-fetched by the loader
// and present on Document.Items[*].(CommentItem).Attachments. (The
// loader lives in a follow-up PR; tests in this PR use Document
// fixtures constructed by hand.)
func RenderHTML(ctx context.Context, doc Document) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	md := newMarkdown()
	policy := sanitizationPolicy()

	// Pre-render each comment / description body to HTML so we can
	// pass already-trusted markup into the html/template. The template
	// then composes ticket header + timeline items into the final
	// document.
	rendered := renderedDocument{
		Header:       doc.Header,
		Mode:         doc.Mode,
		ThreadRootID: doc.ThreadRootID,
	}

	if doc.Description != "" {
		body, err := renderMarkdownSafe(md, policy, doc.Description)
		if err != nil {
			return nil, fmt.Errorf("render description: %w", err)
		}
		rendered.Description = body
	}

	for _, item := range doc.Items {
		switch v := item.(type) {
		case CommentItem:
			rc := renderedComment{Item: v}
			if !v.Deleted {
				body, err := renderMarkdownSafe(md, policy, v.Body)
				if err != nil {
					return nil, fmt.Errorf("render comment %s: %w", v.ID, err)
				}
				rc.BodyHTML = body
			}
			rendered.Items = append(rendered.Items, rc)
		case ActivityItem:
			rendered.Items = append(rendered.Items, renderedActivity{Item: v})
		}
	}

	var buf bytes.Buffer
	if err := documentTemplate.Execute(&buf, rendered); err != nil {
		return nil, fmt.Errorf("render template: %w", err)
	}
	if buf.Len() > MaxHTMLSize {
		return nil, ErrHTMLTooLarge
	}
	return buf.Bytes(), nil
}

// renderMarkdownSafe runs the input Markdown through goldmark, then
// through bluemonday. The two-step pipeline matches the frontend's
// remark-rehype-sanitize chain.
func renderMarkdownSafe(md goldmark.Markdown, policy mdSanitizer, src string) (string, error) {
	var out bytes.Buffer
	if err := md.Convert([]byte(src), &out); err != nil {
		return "", err
	}
	return policy.Sanitize(out.String()), nil
}

// mdSanitizer is the subset of bluemonday.Policy that we use. Keeping
// it as an interface lets tests stub the sanitizer with a pass-through
// (the policy itself is exercised by sanitize_test.go).
type mdSanitizer interface {
	Sanitize(string) string
}

// newMarkdown builds the goldmark instance with:
//   - GFM (tables, strikethrough, autolinks, task lists)
//   - hard line breaks (matches frontend remark-breaks)
//   - link/image renderer override for mention://* and upload://*
//     (matches frontend preprocessMentionShortcodes + preprocessFileCards)
//
// KaTeX math is not rendered in PR-1; the parser leaves `$x^2$` as
// inline-code-equivalent text, and PR-2 (when KaTeX server-side
// rendering ships) will swap in a math extension. The bluemonday
// policy already allowlists KaTeX classes so PR-2 doesn't need to
// revisit sanitization.
func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Strikethrough,
			extension.Linkify,
			extension.TaskList,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithAttribute(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
			html.WithUnsafe(), // bluemonday sanitizes after
			renderer.WithNodeRenderers(
				// Higher priority (lower number) than the default link
				// renderer (which registers at priority 1000); ours
				// wins for mention:// / upload:// schemes and falls
				// through for everything else.
				util.Prioritized(newLinkRenderer(html.WithUnsafe()), 100),
			),
		),
	)
}
