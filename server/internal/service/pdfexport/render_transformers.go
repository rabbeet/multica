package pdfexport

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// This file brings backend Markdown rendering into parity with the
// React frontend pipeline (packages/ui/markdown/Markdown.tsx). The
// frontend uses three preprocessors — preprocessMentionShortcodes,
// preprocessFileCards, preprocessLinks — that rewrite plain Markdown
// nodes into rich span/anchor markup. Without parity work the PDF
// would show "[@Vadim](mention://member/<uuid>)" as a literal Markdown
// link instead of an "@Vadim" pill, which vadim flagged as a UX gap
// in the /plan-eng-review (finding 4A).
//
// Approach: instead of mutating the AST (fragile — goldmark's RawHTML
// nodes assume a backing source reader), we override the renderer
// for *ast.Link and *ast.Image. The override checks for our custom
// URL schemes (mention://*, upload://*); if matched, emits the rich
// markup; otherwise falls through to goldmark's default rendering.
// This is the same pattern goldmark itself uses for the Strikethrough
// and Linkify extensions.

// Scheme prefixes that the frontend's preprocessors produce. They are
// also the URI shapes the multica web app stores in comment bodies —
// see packages/ui/markdown/mentions.ts and file-cards.ts.
const (
	schemeMentionMember = "mention://member/"
	schemeMentionAgent  = "mention://agent/"
	schemeMentionIssue  = "mention://issue/"
	schemeUploadFile    = "upload://"
)

// linkRenderer overrides goldmark's default link/image renderers to
// emit mention and file-card markup for our custom URL schemes. For
// everything else it delegates to the embedded default renderer so
// HTML escaping, autolink handling, and title-attr emission stay
// consistent with the rest of the document.
type linkRenderer struct {
	html.Config
}

func newLinkRenderer(opts ...html.Option) renderer.NodeRenderer {
	r := &linkRenderer{Config: html.NewConfig()}
	for _, opt := range opts {
		opt.SetHTMLOption(&r.Config)
	}
	return r
}

func (r *linkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindImage, r.renderImage)
}

func (r *linkRenderer) renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	dest := string(n.Destination)

	if kind, uuid, ok := parseMentionURL(dest); ok {
		return r.renderMention(w, source, n, kind, uuid, entering)
	}
	if uuid, ok := parseUploadURL(dest); ok {
		return r.renderFileCardLink(w, source, n, uuid, entering)
	}

	// Default link rendering — copied from goldmark's html package.
	if entering {
		_, _ = w.WriteString(`<a href="`)
		writeURLEscaped(w, n.Destination)
		_, _ = w.WriteString(`"`)
		if n.Title != nil {
			_, _ = w.WriteString(` title="`)
			writeAttrEscaped(w, n.Title)
			_ = w.WriteByte('"')
		}
		_ = w.WriteByte('>')
	} else {
		_, _ = w.WriteString("</a>")
	}
	return ast.WalkContinue, nil
}

func (r *linkRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Image)
	dest := string(n.Destination)

	if uuid, ok := parseUploadURL(dest); ok {
		return r.renderFileCardImage(w, source, n, uuid)
	}

	// Default image rendering. Images are leaf-ish in goldmark — the
	// renderer is called only once (entering=true).
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<img src="`)
	writeURLEscaped(w, n.Destination)
	_, _ = w.WriteString(`" alt="`)
	writeChildText(w, n, source)
	_ = w.WriteByte('"')
	if n.Title != nil {
		_, _ = w.WriteString(` title="`)
		writeAttrEscaped(w, n.Title)
		_ = w.WriteByte('"')
	}
	if r.Config.XHTML {
		_, _ = w.WriteString(" />")
	} else {
		_ = w.WriteByte('>')
	}
	return ast.WalkSkipChildren, nil
}

// renderMention emits <span class="mention mention-<kind>" data-…>
// label </span>. The label is the link's inline children — we let the
// default node walker emit them by returning WalkContinue with the
// span tags written around the children.
func (r *linkRenderer) renderMention(w util.BufWriter, _ []byte, _ *ast.Link, kind, uuid string, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<span class="mention mention-`)
		_, _ = w.WriteString(kind)
		_, _ = w.WriteString(`" data-mention-type="`)
		_, _ = w.WriteString(kind)
		_, _ = w.WriteString(`" data-mention-id="`)
		_, _ = w.WriteString(uuid)
		_, _ = w.WriteString(`">`)
	} else {
		_, _ = w.WriteString(`</span>`)
	}
	return ast.WalkContinue, nil
}

// renderFileCardLink emits an inline file-card for an upload:// link
// node (as opposed to an upload:// image node). Used when someone
// pastes the URL directly in body text rather than as a markdown
// image. The label comes from the link's inline children.
func (r *linkRenderer) renderFileCardLink(w util.BufWriter, _ []byte, _ *ast.Link, uuid string, entering bool) (ast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<span class="file-card file-card-generic" data-attachment-id="`)
		_, _ = w.WriteString(uuid)
		_, _ = w.WriteString(`">📎 `)
	} else {
		_, _ = w.WriteString(`</span>`)
	}
	return ast.WalkContinue, nil
}

// renderFileCardImage emits a file-card for an upload:// image node.
// We always render the card form here (not an actual <img>) — the
// rich image preview path runs in the template layer with pre-fetched
// attachment bytes via the loader's Document.Attachments map, not
// inline from comment Markdown.
func (r *linkRenderer) renderFileCardImage(w util.BufWriter, source []byte, n *ast.Image, uuid string) (ast.WalkStatus, error) {
	_, _ = w.WriteString(`<span class="file-card file-card-image" data-attachment-id="`)
	_, _ = w.WriteString(uuid)
	_, _ = w.WriteString(`">📎 `)
	writeChildText(w, n, source)
	_, _ = w.WriteString(`</span>`)
	return ast.WalkSkipChildren, nil
}

// parseMentionURL splits a mention:// URL into (kind, uuid). Returns
// (_, _, false) for anything we don't recognize so the caller can
// fall through to default rendering.
func parseMentionURL(dest string) (kind, uuid string, ok bool) {
	switch {
	case strings.HasPrefix(dest, schemeMentionMember):
		uuid = strings.TrimPrefix(dest, schemeMentionMember)
		kind = "member"
	case strings.HasPrefix(dest, schemeMentionAgent):
		uuid = strings.TrimPrefix(dest, schemeMentionAgent)
		kind = "agent"
	case strings.HasPrefix(dest, schemeMentionIssue):
		uuid = strings.TrimPrefix(dest, schemeMentionIssue)
		kind = "issue"
	default:
		return "", "", false
	}
	if i := strings.IndexAny(uuid, "/?#"); i > 0 {
		uuid = uuid[:i]
	}
	if !uuidLikeRe.MatchString(uuid) {
		return "", "", false
	}
	return kind, uuid, true
}

// parseUploadURL splits an upload://<uuid>[/<filename>] URL into the
// bare UUID. The trailing filename segment is informational; the
// renderer ignores it.
func parseUploadURL(dest string) (uuid string, ok bool) {
	if !strings.HasPrefix(dest, schemeUploadFile) {
		return "", false
	}
	uuid = strings.TrimPrefix(dest, schemeUploadFile)
	if i := strings.IndexAny(uuid, "/?#"); i > 0 {
		uuid = uuid[:i]
	}
	if !uuidLikeRe.MatchString(uuid) {
		return "", false
	}
	return uuid, true
}

// writeURLEscaped writes dst with URL-fragment-safe escaping. We mirror
// the goldmark internal helper rather than importing it (it's not
// exported) — the rules below match its behaviour for our renderer's
// purposes: pass through alphanumerics and a small set of safe punct,
// percent-encode everything else.
func writeURLEscaped(w util.BufWriter, dst []byte) {
	for _, b := range dst {
		if isURLSafe(b) {
			_ = w.WriteByte(b)
			continue
		}
		_, _ = w.WriteString(percentEncode(b))
	}
}

func isURLSafe(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z',
		b >= 'A' && b <= 'Z',
		b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '-', '_', '.', '~', '!', '$', '&', '\'', '(', ')', '*', '+',
		',', ';', '=', ':', '@', '/', '?', '#', '%':
		return true
	}
	return false
}

func percentEncode(b byte) string {
	const hex = "0123456789ABCDEF"
	return "%" + string(hex[b>>4]) + string(hex[b&0x0F])
}

// writeAttrEscaped writes dst with HTML attribute-safe escaping.
func writeAttrEscaped(w util.BufWriter, dst []byte) {
	for _, b := range dst {
		switch b {
		case '&':
			_, _ = w.WriteString("&amp;")
		case '<':
			_, _ = w.WriteString("&lt;")
		case '>':
			_, _ = w.WriteString("&gt;")
		case '"':
			_, _ = w.WriteString("&#34;")
		case '\'':
			_, _ = w.WriteString("&#39;")
		default:
			_ = w.WriteByte(b)
		}
	}
}

// writeChildText emits the concatenated visible text of node into w,
// HTML-escaping as needed. Used for image alt text and file-card
// labels when the renderer doesn't traverse children itself.
func writeChildText(w util.BufWriter, node ast.Node, source []byte) {
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok {
			writeAttrEscaped(w, t.Segment.Value(source))
		}
	}
}
