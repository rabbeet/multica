package pdfexport

import (
	_ "embed"
	"html/template"
	"strings"
	"time"
)

// renderedDocument is what the html/template receives. Bodies are
// pre-rendered (Markdown → HTML → sanitized) by RenderHTML so the
// template only composes the timeline structure.
type renderedDocument struct {
	Header       TicketHeader
	Mode         Mode
	ThreadRootID string
	Description  string // sanitized HTML
	Items        []renderedItem
}

// renderedItem is one element of the timeline. The two implementations
// (renderedComment and renderedActivity) live next to their AST
// counterparts in document.go so the field shapes don't drift.
type renderedItem interface {
	renderedItem()
}

type renderedComment struct {
	Item     CommentItem
	BodyHTML string // sanitized HTML; empty when Item.Deleted
}

func (renderedComment) renderedItem() {}

type renderedActivity struct {
	Item ActivityItem
}

func (renderedActivity) renderedItem() {}

// documentTemplate is the parsed html/template instance used to
// assemble the final HTML. It is parsed once at package load and
// shared by all RenderHTML calls (html/template is safe for
// concurrent use).
var documentTemplate = template.Must(template.New("pdfexport").
	Funcs(template.FuncMap{
		"isComment":   func(it renderedItem) bool { _, ok := it.(renderedComment); return ok },
		"isActivity":  func(it renderedItem) bool { _, ok := it.(renderedActivity); return ok },
		"asComment":   func(it renderedItem) renderedComment { c, _ := it.(renderedComment); return c },
		"asActivity":  func(it renderedItem) renderedActivity { a, _ := it.(renderedActivity); return a },
		"safeHTML":    func(s string) template.HTML { return template.HTML(s) },
		"fmtTime":     formatTimestamp,
		"isThread":    func(m Mode) bool { return m == ModeThread },
		"isFull":      func(m Mode) bool { return m == ModeFull },
		"joinNames":   joinNames,
		"reactionsOK": func(rs []Reaction) bool { return len(rs) > 0 },
		"indentClass": func(n int) string {
			if n < 0 {
				n = 0
			}
			if n > 5 {
				n = 5
			}
			return "thread-indent-" + itoa(n)
		},
	}).
	Parse(documentTemplateText))

//go:embed assets/template.html
var documentTemplateText string

// formatTimestamp renders a time.Time the same way the React UI does
// — "2026-06-02 05:30 UTC" — so the PDF reads identically to the
// in-app comment. Using UTC and a single canonical format keeps
// renders deterministic across pods (we don't trust system locale).
func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// joinNames returns "Vadim", "Vadim, Agent-1", "Vadim, Agent-1, …+2",
// etc. — the same compact rendering UI uses for reaction lists.
func joinNames(names []string) string {
	const max = 3
	if len(names) == 0 {
		return ""
	}
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + ", …+" + itoa(len(names)-max)
}

// itoa is a tiny strconv.Itoa avoidance — template funcmap entries
// can't take *strconv.Itoa directly without an adapter, and this is
// hot enough on long timelines to justify staying out of the
// allocation path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
