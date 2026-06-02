// Package pdfexport renders multica issues to PDF.
//
// PUL-266 introduces two export modes:
//
//   - Full ticket — issue header + description + every timeline entry
//     (activity records and comments) in a single chronological order,
//     earliest at top.
//   - Single thread — issue header + one top-level comment and its
//     entire parent_id-subtree.
//
// This package is responsible only for turning an in-memory Document
// into bytes. PR-1 emits HTML so a hidden /export.html?_dev=1 route can
// be used for visual review. PR-2 swaps the HTML output for a gotenberg
// roundtrip (see plans repo
// Multica/2026-06-02-pul-266-pdf-export.md, revision 2). Splitting the
// render pipeline from the loader / handler keeps each PR reviewable
// in isolation — this file ships only the data types that bridge the
// loader (which fetches DB rows) and the renderer.
//
// All types are designed to mirror the existing TimelineEntry surface
// in server/internal/handler/activity.go so the loader in the follow-up
// PR can map TimelineEntry → Document without rewriting the timeline
// semantics.
package pdfexport

import "time"

// Mode selects which subset of the timeline to render.
type Mode int

const (
	// ModeFull renders the full issue: header, description, all activity
	// entries, and all comments in chronological order.
	ModeFull Mode = iota
	// ModeThread renders only the issue header (compact) and the
	// subtree rooted at Document.ThreadRootID. Activity entries are
	// suppressed in thread mode — they belong to the ticket as a whole,
	// not to one conversation.
	ModeThread
)

// Document is the in-memory representation of what gets rendered.
//
// The loader (follow-up PR) is responsible for producing this from
// the database. The renderer treats Document as an immutable input
// and never reaches into other services.
type Document struct {
	Mode   Mode
	Header TicketHeader

	// Description is the issue's description rendered to HTML the same
	// way comment bodies are — see render.go. Stored as raw Markdown
	// here; the renderer handles the Markdown → HTML conversion.
	Description string

	// Items is the chronological timeline. In ModeFull it includes
	// CommentItem and ActivityItem values; in ModeThread it includes
	// only CommentItem values that belong to the selected subtree
	// (loader's responsibility — the renderer trusts the filter).
	Items []TimelineItem

	// ThreadRootID identifies the top-level comment selected for
	// thread mode. Empty in ModeFull. The renderer uses this to
	// short-circuit the ticket header into "thread of <id>" mode
	// and to skip activity entries.
	ThreadRootID string
}

// TicketHeader is the metadata block shown at the top of every export.
//
// Fields are pre-resolved by the loader to human-readable strings so
// the renderer never has to consult the DB. Naming mirrors the
// IssueResponse shape in server/internal/handler/issue.go to keep
// the loader's mapping straightforward.
type TicketHeader struct {
	Identifier   string // "PUL-266"
	Title        string
	ProjectTitle string // empty if no project attached
	Status       string
	Priority     string
	CreatorName  string
	AssigneeName string // empty if unassigned
	CreatedAt    time.Time
	UpdatedAt    time.Time
	URL          string // canonical multica URL — used in thread-mode footer
}

// TimelineItem is one row in the chronological timeline. It is a tagged
// union: exactly one of the concrete item methods returns a non-zero
// value.
//
// We use a small interface plus concrete struct types (rather than a
// single struct with a Type field) because the renderer dispatches on
// type via a type switch, and the field set differs sharply between
// activity rows and comment blocks. Keeping the two types apart at the
// type level prevents "what fields are valid for which type" bugs.
type TimelineItem interface {
	timelineItem()
}

// ActivityItem represents one entry from the issue's activity log
// (status change, assignee change, label attach/detach, etc.). Renders
// as a single visually-compact row.
type ActivityItem struct {
	ID        string
	ActorName string
	CreatedAt time.Time

	// Action is the verb shown in the row, already localized by the
	// loader. Example: "changed status: todo → in_progress".
	Action string
}

func (ActivityItem) timelineItem() {}

// CommentItem represents one comment block in the rendered timeline.
// Soft-deleted comments are still surfaced (Deleted = true) so that
// reply-thread structure remains visually intact — the renderer shows
// a "(deleted comment)" tombstone in their place instead of an awkward
// gap.
type CommentItem struct {
	ID        string
	ParentID  string // empty for top-level
	ActorName string
	CreatedAt time.Time
	UpdatedAt time.Time

	// Body is raw Markdown. The renderer runs it through goldmark +
	// custom AST transformers + bluemonday. Ignored when Deleted.
	Body string

	// Deleted marks a soft-deleted comment. The renderer emits a
	// tombstone and skips Body. (Edge case 10 from /plan-eng-review,
	// finding 10A.)
	Deleted bool

	// Reactions, if non-empty, render as a single line beneath the
	// body — "👍 ×3 (Vadim, Agent-1)" — per finding 10A.
	Reactions []Reaction

	// Attachments are rendered after Body. Image attachments may
	// arrive with InlineData populated; non-images render as
	// callout boxes. See attachments.go in the follow-up PR.
	Attachments []Attachment

	// IndentLevel is the visual nesting depth (0 for top-level, 1 for
	// reply, 2 for reply-of-reply…). Capped at 5 by the loader so deep
	// chains don't slide off the page. Used by CSS class
	// "thread-indent-N".
	IndentLevel int
}

func (CommentItem) timelineItem() {}

// Reaction is one emoji aggregate on a comment.
type Reaction struct {
	Emoji      string
	Count      int
	ActorNames []string
}

// Attachment is one file attached to a comment (or the issue
// description, when we later thread description attachments through).
// InlineData is populated for content types we inline directly into
// the PDF (currently images and small text files); for everything else
// only the metadata + URL are used to render a callout box.
type Attachment struct {
	ID         string
	Filename   string
	MimeType   string
	SizeBytes  int64
	URL        string // canonical multica download URL
	InlineData []byte // optional pre-fetched body for inlining
}

// IsEdited reports whether a comment was edited after creation. Used
// by the renderer to show an "(edited)" pill next to the timestamp —
// finding 10A's fourth edge case.
func (c CommentItem) IsEdited() bool {
	if c.UpdatedAt.IsZero() {
		return false
	}
	// Comments are saved with UpdatedAt == CreatedAt on insert.
	// Tolerate sub-second drift from server-side clock writes.
	return c.UpdatedAt.Sub(c.CreatedAt) > time.Second
}
