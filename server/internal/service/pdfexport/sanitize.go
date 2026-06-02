package pdfexport

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// sanitizationPolicy returns the bluemonday policy applied after
// goldmark renders comment markdown to HTML.
//
// We start from UGCPolicy() — the user-generated-content default that
// already disallows <script>, <iframe>, on*= handlers, javascript: URIs,
// and most exfiltration vectors — and re-allowlist the small set of
// CSS classes our renderer emits via custom AST transformers and the
// HTML template:
//
//   - mention / mention-member / mention-agent / mention-issue — added
//     by render_transformers.go::rewriteMentions.
//   - file-card / file-card-* — added by render_transformers.go::rewriteFileCards.
//   - activity-row / comment-block / comment-deleted / edited-pill /
//     reactions-line / thread-indent-N — added by the html/template
//     (see template.go).
//   - katex* / mathml* — preserved verbatim if a future PR plugs
//     KaTeX server-side rendering in. PR-1 doesn't render math yet
//     but the allowlist is forward-compatible so PR-2 doesn't have
//     to revisit sanitization.
//
// Why allowlist by regex rather than by literal value: thread-indent-N
// is a small parameterized family (N = 0..5) and KaTeX emits a long
// list of mord/mop/mbin classes. A regex pattern is the cheapest way
// to express "any class starting with these prefixes is fine".
//
// We also allow data: URIs on <img src> for inlined images (the loader
// base64-embeds attachment bodies into the HTML so the PDF is
// self-contained). Without this allowance bluemonday would strip the
// src and the image would render as a broken icon in the PDF.
func sanitizationPolicy() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()

	// Class allowlist for our renderer-emitted markup.
	p.AllowAttrs("class").Matching(classAllowRe).OnElements(
		"span", "div", "p", "a", "img", "ul", "ol", "li",
		"blockquote", "pre", "code", "table", "thead", "tbody",
		"tr", "th", "td", "h1", "h2", "h3", "h4", "h5", "h6",
		"sup", "sub", "strong", "em", "del", "br", "hr",
	)

	// Data-URI images. UGCPolicy already allows http/https on <img>;
	// we add data: for the inlined attachment path. Restrict to
	// image/* so we never accept data:text/html (which would round-trip
	// arbitrary HTML through what looks like an image tag).
	p.AllowAttrs("src").Matching(dataImageURIRe).OnElements("img")
	p.AllowAttrs("alt", "title").OnElements("img")
	p.AllowAttrs("loading").Matching(loadingAttrRe).OnElements("img")

	// Mentions and file cards carry data-* attributes for future
	// enhancement (deep-link click handlers in a viewer that re-opens
	// PDFs in multica). The id-shaped attributes match UUIDs; the
	// type-shaped one is a small enum. We keep the two matchers
	// separate so a "member" string doesn't get rejected for failing
	// the UUID pattern.
	p.AllowAttrs("data-mention-id",
		"data-file-id",
		"data-attachment-id").Matching(uuidLikeRe).OnElements(
		"span", "a", "div",
	)
	p.AllowAttrs("data-mention-type").Matching(mentionTypeRe).OnElements(
		"span", "a", "div",
	)
	p.AllowAttrs("data-comment-id", "data-activity-id").Matching(
		regexp.MustCompile(`^[A-Za-z0-9_-]+$`)).OnElements(
		"article", "div",
	)

	// Hard line breaks emitted by goldmark when WithHardWraps is on.
	// UGCPolicy allows <br>; just confirm by listing it explicitly.
	p.AllowElements("br")

	return p
}

// classAllowRe matches the CSS class strings the renderer is allowed
// to attach to its emitted elements. One class per match — bluemonday
// matches the entire attribute value, so we accept a space-separated
// list of allowed tokens.
//
// Each token must be a member of one of:
//
//   - mention, mention-member, mention-agent, mention-issue
//   - file-card, file-card-image, file-card-pdf, file-card-text, file-card-generic
//   - activity-row, activity-action
//   - comment-block, comment-header, comment-body, comment-deleted
//   - edited-pill, reactions-line, thread-indent-0..5
//   - katex, katex-html, katex-mathml, katex-display, mord, mop,
//     mbin, mrel, mopen, mclose, mpunct, minner, accent (KaTeX runtime)
//
// Anything else is dropped silently by bluemonday — the test suite
// in render_test.go verifies that emitted output round-trips through
// this policy without losing the classes we intentionally apply.
var classAllowRe = regexp.MustCompile(`^(\s*(` +
	`mention(-(member|agent|issue))?` +
	`|file-card(-(image|pdf|text|generic))?` +
	`|activity-row|activity-action` +
	`|comment-block|comment-header|comment-body|comment-deleted` +
	`|edited-pill|reactions-line|thread-indent-[0-5]` +
	`|katex(-(html|mathml|display))?|mord|mop|mbin|mrel|mopen|mclose|mpunct|minner|accent` +
	`|language-[a-z0-9_+-]+` + // for fenced code blocks goldmark emits
	`))+\s*$`)

// dataImageURIRe accepts data:image/(png|jpe?g|gif|webp|svg+xml);base64,...
// We do NOT allow data:image/svg+xml *without* base64 because raw SVG
// can carry <script> elements. Base64-encoded SVG is still risky in
// browsers but the bluemonday SVG sanitizer would have to run on the
// decoded bytes — out of scope; PR-2 may decide to drop SVG entirely.
var dataImageURIRe = regexp.MustCompile(
	`^data:image/(png|jpeg|jpg|gif|webp);base64,[A-Za-z0-9+/=]+$`,
)

// loadingAttrRe matches the <img loading> hints we emit for big images.
var loadingAttrRe = regexp.MustCompile(`^(lazy|eager)$`)

// uuidLikeRe matches multica's UUIDs (RFC 4122 v4 plus the v7 form
// some endpoints emit). We deliberately accept a slightly looser
// pattern than strict v4 — the renderer must not reject IDs that
// look correct to the loader.
var uuidLikeRe = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
)

// mentionTypeRe matches the small enum carried by data-mention-type
// on .mention spans. Adding a new kind here means updating
// render_transformers.go::parseMentionURL as well.
var mentionTypeRe = regexp.MustCompile(`^(member|agent|issue)$`)
