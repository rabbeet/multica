package pdfexport

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LoaderQueries is the small subset of the sqlc-generated *db.Queries
// API that the pdfexport loader needs. We restate it as a tight
// interface (rather than depend on the full *db.Queries surface) for
// two reasons:
//
//  1. Tests inject a hand-rolled fake without standing up Postgres —
//     see loader_test.go.
//  2. The shape of "what does PDF export read from the DB" is now
//     explicit and reviewable, so future schema work has a clear
//     blast-radius signal when one of these methods changes.
//
// *db.Queries satisfies this interface as-is; callers in the handler
// layer pass h.Queries directly.
type LoaderQueries interface {
	ListCommentsLatest(ctx context.Context, arg db.ListCommentsLatestParams) ([]db.Comment, error)
	ListActivitiesLatest(ctx context.Context, arg db.ListActivitiesLatestParams) ([]db.ActivityLog, error)
	ListReactionsByCommentIDs(ctx context.Context, dollar1 []pgtype.UUID) ([]db.CommentReaction, error)
	ListAttachmentsByCommentIDs(ctx context.Context, arg db.ListAttachmentsByCommentIDsParams) ([]db.Attachment, error)
	ListMembersWithUser(ctx context.Context, workspaceID pgtype.UUID) ([]db.ListMembersWithUserRow, error)
	ListAgents(ctx context.Context, workspaceID pgtype.UUID) ([]db.Agent, error)
	GetProject(ctx context.Context, id pgtype.UUID) (db.Project, error)
}

// ErrThreadRootNotFound is returned when LoadDocument is called in
// ModeThread but the requested root comment is missing or belongs to
// a different issue. Handlers should map this to HTTP 404 — the
// caller asked for a thread that doesn't exist.
var ErrThreadRootNotFound = errors.New("pdfexport: thread root comment not found")

// ErrInvalidThreadRoot signals a thread root ID that isn't a valid
// UUID. Handlers should map to 400.
var ErrInvalidThreadRoot = errors.New("pdfexport: invalid thread root id")

// loadCap caps how many rows we ask the DB for. We don't paginate the
// timeline for export — the user wants the WHOLE ticket in one PDF —
// so we ask for "everything" and trust the 50MB HTML cap downstream
// (render.go's MaxHTMLSize) to block runaway tickets. math.MaxInt32
// matches the sqlc Limit column type and is effectively unbounded
// at any realistic ticket size.
const loadCap = math.MaxInt32

// LoadDocument fetches every timeline entry for the issue, resolves
// actor display names, and assembles a Document suitable for
// RenderHTML.
//
// Mode rules:
//
//   - ModeFull — returns header + description + all comments + all
//     activities, chronological ASC (earliest at top, matching what
//     vadim asked for in PUL-266 + finding 3A from /plan-eng-review).
//   - ModeThread — returns header + ONLY the comments in the subtree
//     rooted at threadRootID. Activity entries are suppressed (they
//     belong to the ticket, not to one conversation). The root and
//     its descendants are returned ASC by creation order.
//
// threadRootID may be either a UUID string or the empty string in
// ModeFull. Passing a non-empty value in ModeFull is treated as a
// bug — the function returns an error rather than silently ignoring
// the field.
func LoadDocument(
	ctx context.Context,
	q LoaderQueries,
	issue db.Issue,
	mode Mode,
	threadRootID string,
) (Document, error) {
	if mode == ModeFull && threadRootID != "" {
		return Document{}, fmt.Errorf("pdfexport: threadRootID set in ModeFull")
	}
	if mode == ModeThread && threadRootID == "" {
		return Document{}, fmt.Errorf("pdfexport: empty threadRootID in ModeThread")
	}

	doc := Document{Mode: mode, ThreadRootID: threadRootID}

	// 1. Header. ProjectTitle is optional and tolerates a missing
	// project gracefully — if the FK is broken, we still want a PDF
	// rather than a 500.
	header := mapHeader(issue)
	if issue.ProjectID.Valid {
		if p, err := q.GetProject(ctx, issue.ProjectID); err == nil {
			header.ProjectTitle = p.Title
		}
	}

	// 2. Actor name resolver. One workspace-scoped fetch for members
	// (joined with the user table for display name) and one for
	// agents. The map lookup is O(actors) — cheap even on a 5000-
	// comment ticket.
	resolver, err := newActorResolver(ctx, q, issue.WorkspaceID)
	if err != nil {
		return Document{}, fmt.Errorf("resolve actors: %w", err)
	}

	header.CreatorName = resolver.lookup(issue.CreatorType, issue.CreatorID)
	if issue.AssigneeID.Valid && issue.AssigneeType.Valid {
		header.AssigneeName = resolver.lookup(issue.AssigneeType.String, issue.AssigneeID)
	}
	doc.Header = header

	// Description from issue body (text column; may be NULL).
	if issue.Description.Valid {
		doc.Description = issue.Description.String
	}

	// 3. Comments. We always need them (both modes). One query covers
	// the whole ticket; we'll filter / re-thread in memory.
	comments, err := q.ListCommentsLatest(ctx, db.ListCommentsLatestParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       loadCap,
	})
	if err != nil {
		return Document{}, fmt.Errorf("list comments: %w", err)
	}

	commentIDs := make([]pgtype.UUID, len(comments))
	for i, c := range comments {
		commentIDs[i] = c.ID
	}

	// Reactions + attachments come back ungrouped from the batch
	// queries; we fan them out into per-comment maps for the mapper.
	// Missing data (e.g. ListReactions returns an error) is logged
	// upstream and falls back to "no reactions" rather than failing
	// the whole export — the PDF is still useful without them.
	reactions := groupReactionsByComment(ctx, q, commentIDs)
	attachments := groupAttachmentsByComment(ctx, q, commentIDs, issue.WorkspaceID)

	// 4. Build the timeline. In thread mode we need to know which
	// comment IDs descend from threadRootID. We resolve that here
	// because activities are skipped in thread mode and we never
	// have to load them.
	var threadKeep map[string]int // commentID → indent depth
	if mode == ModeThread {
		rootUUID, err := parseUUID(threadRootID)
		if err != nil {
			return Document{}, ErrInvalidThreadRoot
		}
		threadKeep = computeThreadIndents(comments, rootUUID, issue.ID)
		if _, ok := threadKeep[threadRootID]; !ok {
			return Document{}, ErrThreadRootNotFound
		}
	}

	// 5. Walk comments oldest-first (we got them DESC from the
	// query). For each, decide whether to include and at what indent.
	commentItems := make([]CommentItem, 0, len(comments))
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		cid := uuidString(c.ID)

		indent := 0
		if mode == ModeThread {
			d, keep := threadKeep[cid]
			if !keep {
				continue
			}
			indent = d
		} else if c.ParentID.Valid {
			// In full mode we still want a 1-deep visual indent for
			// replies. Computing the exact ancestor chain depth
			// (matching the indentCap=5 cap from the plan) requires
			// a parent-map walk; do that once.
			indent = indentDepth(comments, c.ParentID)
		}

		ci := mapComment(c, indent, resolver, reactions[cid], attachments[cid])
		commentItems = append(commentItems, ci)
	}

	// 6. Activities. Skipped in thread mode (finding 3A: activities
	// are ticket-scoped, not thread-scoped).
	var activityItems []ActivityItem
	if mode == ModeFull {
		acts, err := q.ListActivitiesLatest(ctx, db.ListActivitiesLatestParams{
			IssueID: issue.ID,
			Limit:   loadCap,
		})
		if err != nil {
			return Document{}, fmt.Errorf("list activities: %w", err)
		}
		activityItems = make([]ActivityItem, 0, len(acts))
		for i := len(acts) - 1; i >= 0; i-- {
			activityItems = append(activityItems, mapActivity(acts[i], resolver))
		}
	}

	// 7. Merge ASC by created_at, id for stable ordering. Both slices
	// are already ASC; an in-place stable merge would be faster, but
	// for the size of the ticket (and the post-render bottleneck
	// being chromium, not Go) the simple append-and-sort is fine.
	doc.Items = mergeTimeline(commentItems, activityItems)

	return doc, nil
}

// mergeTimeline interleaves comment and activity items, stable-sorted
// ASC by CreatedAt then ID. Activities arrive nil in ModeThread.
func mergeTimeline(comments []CommentItem, activities []ActivityItem) []TimelineItem {
	out := make([]TimelineItem, 0, len(comments)+len(activities))
	for _, c := range comments {
		out = append(out, c)
	}
	for _, a := range activities {
		out = append(out, a)
	}
	// Stable sort: items with the same CreatedAt keep their original
	// (DB-ordered) sequence, which keeps multi-event activity bursts
	// readable.
	sortStable(out)
	return out
}

// sortStable sorts a TimelineItem slice ASC by (CreatedAt, ID).
// Implemented as a tiny stable insertion sort to avoid pulling
// sort.SliceStable into the hot path (and to keep the dependency
// graph of pdfexport unchanged from PR-1, which only needs goldmark
// and bluemonday).
func sortStable(items []TimelineItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && itemLess(items[j], items[j-1]); j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
}

func itemLess(a, b TimelineItem) bool {
	at, aid := itemKey(a)
	bt, bid := itemKey(b)
	if !at.Equal(bt) {
		return at.Before(bt)
	}
	return aid < bid
}

func itemKey(it TimelineItem) (time.Time, string) {
	switch v := it.(type) {
	case CommentItem:
		return v.CreatedAt, v.ID
	case ActivityItem:
		return v.CreatedAt, v.ID
	}
	return time.Time{}, ""
}

// computeThreadIndents walks the parent_id chain starting at root and
// returns a map[commentID]indentLevel for every descendant (root
// included at indent 0). Comments whose chain reaches a different
// issue are excluded — defense in depth in case the loader is later
// reused in a cross-ticket context.
func computeThreadIndents(comments []db.Comment, root pgtype.UUID, issueID pgtype.UUID) map[string]int {
	byID := make(map[string]db.Comment, len(comments))
	for _, c := range comments {
		byID[uuidString(c.ID)] = c
	}
	keep := make(map[string]int)
	rootStr := uuidString(root)
	if root, ok := byID[rootStr]; ok && uuidsEqual(root.IssueID, issueID) {
		keep[rootStr] = 0
	} else {
		return keep
	}

	// Multi-pass: any time we add a comment whose parent is in `keep`,
	// add it with parent's indent + 1, capped at 5. Repeat until no
	// new additions in a pass. Bounded by O(comments * maxDepth).
	const maxIndent = 5
	for {
		added := false
		for _, c := range comments {
			id := uuidString(c.ID)
			if _, already := keep[id]; already {
				continue
			}
			if !c.ParentID.Valid {
				continue
			}
			parentID := uuidString(c.ParentID)
			if pIndent, ok := keep[parentID]; ok {
				if pIndent+1 < maxIndent {
					keep[id] = pIndent + 1
				} else {
					keep[id] = maxIndent
				}
				added = true
			}
		}
		if !added {
			break
		}
	}
	return keep
}

// indentDepth walks parent_id chains for the full-ticket export.
// Capped at 5 to match the CSS rules in assets/template.html.
func indentDepth(comments []db.Comment, parentID pgtype.UUID) int {
	if !parentID.Valid {
		return 0
	}
	byID := make(map[string]db.Comment, len(comments))
	for _, c := range comments {
		byID[uuidString(c.ID)] = c
	}
	depth := 0
	cur := parentID
	for cur.Valid {
		depth++
		if depth >= 5 {
			return 5
		}
		c, ok := byID[uuidString(cur)]
		if !ok {
			return depth
		}
		cur = c.ParentID
	}
	return depth
}

// groupReactionsByComment fetches reactions for the given comment IDs
// in one batch and groups them into Reaction summaries keyed by
// comment ID. A failed query is treated as "no reactions" — we'd
// rather ship a PDF without reaction badges than fail the export.
func groupReactionsByComment(ctx context.Context, q LoaderQueries, commentIDs []pgtype.UUID) map[string][]Reaction {
	out := map[string][]Reaction{}
	if len(commentIDs) == 0 {
		return out
	}
	rows, err := q.ListReactionsByCommentIDs(ctx, commentIDs)
	if err != nil {
		return out
	}
	// Aggregate by (commentID, emoji).
	type key struct {
		cid, emoji string
	}
	agg := map[key]*Reaction{}
	for _, r := range rows {
		cid := uuidString(r.CommentID)
		k := key{cid: cid, emoji: r.Emoji}
		if cur, ok := agg[k]; ok {
			cur.Count++
			// ActorNames is filled in by the handler when we resolve
			// names; the loader puts the raw actor_id strings here
			// and lets mapComment swap them. To keep things simple
			// for PR-1b we just put a placeholder; the real name
			// resolution lives in mapComment and uses the shared
			// resolver.
			cur.ActorNames = append(cur.ActorNames, uuidString(r.ActorID))
		} else {
			agg[k] = &Reaction{
				Emoji:      r.Emoji,
				Count:      1,
				ActorNames: []string{uuidString(r.ActorID)},
			}
		}
	}
	for k, v := range agg {
		out[k.cid] = append(out[k.cid], *v)
	}
	return out
}

// groupAttachmentsByComment fetches attachments for the given comment
// IDs in one batch and groups them. PR-1b ships only the metadata —
// no S3 fetch, no inline body. PR-2 will fetch the bytes for image
// inlining; for the dev HTML route the metadata is enough to render
// file-card callouts.
func groupAttachmentsByComment(ctx context.Context, q LoaderQueries, commentIDs []pgtype.UUID, workspaceID pgtype.UUID) map[string][]Attachment {
	out := map[string][]Attachment{}
	if len(commentIDs) == 0 {
		return out
	}
	rows, err := q.ListAttachmentsByCommentIDs(ctx, db.ListAttachmentsByCommentIDsParams{
		Column1:     commentIDs,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return out
	}
	for _, a := range rows {
		cid := uuidString(a.CommentID)
		out[cid] = append(out[cid], Attachment{
			ID:        uuidString(a.ID),
			Filename:  a.Filename,
			MimeType:  a.ContentType,
			SizeBytes: a.SizeBytes,
			URL:       a.Url,
		})
	}
	return out
}

// mapHeader builds the TicketHeader's static fields from the issue
// row. The dynamic fields (ProjectTitle, CreatorName, AssigneeName)
// are filled in by LoadDocument after issuing the lookup queries.
func mapHeader(issue db.Issue) TicketHeader {
	return TicketHeader{
		Identifier: "", // filled in by handler — loader doesn't know the workspace's prefix
		Title:      issue.Title,
		Status:     issue.Status,
		Priority:   issue.Priority,
		CreatedAt:  issue.CreatedAt.Time,
		UpdatedAt:  issue.UpdatedAt.Time,
	}
}

// mapComment turns a db.Comment plus its pre-grouped reactions and
// attachments into a CommentItem. ActorName is resolved via the
// shared resolver; reactions' ActorNames are also resolved here so
// the renderer sees human-readable strings everywhere.
func mapComment(
	c db.Comment,
	indent int,
	resolver *actorResolver,
	reactions []Reaction,
	attachments []Attachment,
) CommentItem {
	rNamed := make([]Reaction, len(reactions))
	for i, r := range reactions {
		names := make([]string, 0, len(r.ActorNames))
		for _, raw := range r.ActorNames {
			names = append(names, resolver.lookupAnyByID(raw))
		}
		rNamed[i] = Reaction{Emoji: r.Emoji, Count: r.Count, ActorNames: names}
	}

	return CommentItem{
		ID:          uuidString(c.ID),
		ParentID:    optionalUUIDString(c.ParentID),
		ActorName:   resolver.lookup(c.AuthorType, c.AuthorID),
		CreatedAt:   c.CreatedAt.Time,
		UpdatedAt:   c.UpdatedAt.Time,
		Body:        c.Content,
		Deleted:     false, // multica comments are hard-deleted; no DB tombstone today
		Reactions:   rNamed,
		Attachments: attachments,
		IndentLevel: indent,
	}
}

// mapActivity turns a db.ActivityLog into an ActivityItem. The Action
// field is the raw action verb from the DB ("status_changed",
// "assignee_changed", etc.) — for v1 we surface it as-is and trust
// the renderer's CSS to keep the row visually quiet. PR-2 (when we
// have product visibility into the full action vocabulary) can swap
// in localized human strings via a switch.
func mapActivity(a db.ActivityLog, resolver *actorResolver) ActivityItem {
	actorType := ""
	if a.ActorType.Valid {
		actorType = a.ActorType.String
	}
	return ActivityItem{
		ID:        uuidString(a.ID),
		ActorName: resolver.lookup(actorType, a.ActorID),
		CreatedAt: a.CreatedAt.Time,
		Action:    humaniseAction(a.Action),
	}
}

// humaniseAction maps the small set of action verbs we know about
// into PDF-friendly phrases. Unknown verbs pass through verbatim.
//
// This is intentionally minimal — the activity row is meant to be
// compact ("Vadim · 2026-06-02 05:30 · status: todo → in_progress"),
// not a full English narration. The "→" arrows etc. are produced by
// the handler / details JSON unpacking, which is out of scope for
// PR-1b. For now we just titlecase the snake_case action and let the
// row render.
func humaniseAction(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// actorResolver looks up display names for member / agent actor IDs.
// Members come from the workspace's ListMembersWithUser query (which
// joins user.name); agents come from ListAgents (which has Name as a
// column directly on agent). Unknown IDs fall back to a truncated
// UUID so the export still surfaces *something*.
type actorResolver struct {
	members map[string]string // uuid → user_name
	agents  map[string]string // uuid → agent name
}

func newActorResolver(ctx context.Context, q LoaderQueries, workspaceID pgtype.UUID) (*actorResolver, error) {
	r := &actorResolver{
		members: map[string]string{},
		agents:  map[string]string{},
	}
	members, err := q.ListMembersWithUser(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	for _, m := range members {
		r.members[uuidString(m.UserID)] = m.UserName
		r.members[uuidString(m.ID)] = m.UserName // some activity logs reference member.id, others user.id
	}
	agents, err := q.ListAgents(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	for _, a := range agents {
		r.agents[uuidString(a.ID)] = a.Name
	}
	return r, nil
}

func (r *actorResolver) lookup(actorType string, id pgtype.UUID) string {
	idStr := uuidString(id)
	switch actorType {
	case "member", "user":
		if n, ok := r.members[idStr]; ok {
			return n
		}
	case "agent":
		if n, ok := r.agents[idStr]; ok {
			return n
		}
	}
	return r.lookupAnyByID(idStr)
}

// lookupAnyByID is the type-agnostic fallback used by reactions
// (where actor_type isn't joined to the comment row in the same
// query). Tries members first, then agents, then a truncated UUID.
func (r *actorResolver) lookupAnyByID(idStr string) string {
	if n, ok := r.members[idStr]; ok {
		return n
	}
	if n, ok := r.agents[idStr]; ok {
		return n
	}
	if len(idStr) >= 8 {
		return idStr[:8]
	}
	return idStr
}

// uuidString renders a pgtype.UUID as the standard 8-4-4-4-12 hex
// form. We could use github.com/google/uuid but the multica handlers
// generally do this conversion inline with the same shape, so we
// match.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func optionalUUIDString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuidString(u)
}

func uuidsEqual(a, b pgtype.UUID) bool {
	if !a.Valid || !b.Valid {
		return false
	}
	return a.Bytes == b.Bytes
}

// parseUUID parses a standard 8-4-4-4-12 hex string into a
// pgtype.UUID. Returns an error on malformed input — we use this for
// the thread-mode root, where bad input should surface as 400 in the
// handler rather than crash the renderer.
func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	if !u.Valid {
		return pgtype.UUID{}, errors.New("empty uuid")
	}
	return u, nil
}
