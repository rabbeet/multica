package handler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/mmbot/events"
	"github.com/multica-ai/multica/server/internal/mmbot/multicacli"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// fakeMulticaInbound captures CreateIssue / AddComment calls so tests can
// assert the wire shape passed to the CLI.
type fakeMulticaInbound struct {
	createCalls   int
	lastCreate    multicacli.CreateIssueRequest
	createReturns multicacli.Issue
	createErr     error

	commentCalls   int
	lastCommentIID string
	lastCommentBody string
	commentReturns multicacli.Comment
	commentErr     error
}

func (f *fakeMulticaInbound) CreateIssue(ctx context.Context, req multicacli.CreateIssueRequest) (multicacli.Issue, error) {
	f.createCalls++
	f.lastCreate = req
	if f.createErr != nil {
		return multicacli.Issue{}, f.createErr
	}
	if f.createReturns.ID == "" {
		f.createReturns = multicacli.Issue{
			ID:         "issue-uuid-default",
			Identifier: "PUL-100",
			Title:      req.Title,
			ProjectID:  multicacli.MarimoProjectID,
		}
	}
	return f.createReturns, nil
}
func (f *fakeMulticaInbound) AddComment(ctx context.Context, issueID, content string) (multicacli.Comment, error) {
	f.commentCalls++
	f.lastCommentIID = issueID
	f.lastCommentBody = content
	if f.commentErr != nil {
		return multicacli.Comment{}, f.commentErr
	}
	if f.commentReturns.ID == "" {
		f.commentReturns = multicacli.Comment{
			ID:        "comment-uuid-default",
			IssueID:   issueID,
			Content:   content,
			CreatedAt: "2026-06-17T05:00:00Z",
		}
	}
	return f.commentReturns, nil
}

// fakeMM captures the rest.PostRequest the inbound handler sends as an ack.
type fakeMM struct {
	calls int
	last  rest.PostRequest
	err   error
}

func (f *fakeMM) CreatePost(ctx context.Context, req rest.PostRequest) (rest.Post, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return rest.Post{}, f.err
	}
	return rest.Post{
		ID:        "mm-ack-id",
		UserID:    "bot",
		ChannelID: req.ChannelID,
		RootID:    req.RootID,
		Message:   req.Message,
		CreateAt:  1718500000000,
	}, nil
}

func newInboundFixture(t *testing.T) (*Inbound, *fakeMulticaInbound, *fakeMM, *state.Store) {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mc := &fakeMulticaInbound{}
	mm := &fakeMM{}
	h := NewInbound(InboundConfig{
		Store:             st,
		Multica:           mc,
		MM:                mm,
		AllowedChannelIDs: map[string]struct{}{"chan-data": {}},
		AllowedUserIDs:    map[string]struct{}{"user-lina": {}, "user-vadim": {}},
		BotUserID:         "user-bot",
	})
	return h, mc, mm, st
}

// Plan finding #3 — hard guard. The MM-side description cannot influence
// `--project` or `--assignee-id`; both are constants embedded in
// multicacli.Client. The inbound handler simply forwards the message;
// multicacli.CreateIssue does the pinning. Verifying the handler passes
// through correctly is the unit-level guarantee. The pinning itself is
// tested separately in multicacli_test (TestCreateIssue_HardGuardSets...).
func TestInbound_TopLevelCreatesIssueAndAcks(t *testing.T) {
	h, mc, mm, st := newInboundFixture(t)
	post := events.Post{
		ID:        "mm-post-1",
		UserID:    "user-lina",
		ChannelID: "chan-data",
		Message:   "сколько мы продали MOW-IST за май?",
		Username:  "lina",
	}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.createCalls != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", mc.createCalls)
	}
	if !strings.Contains(mc.lastCreate.Description, "From: @lina") {
		t.Errorf("description missing MM author footer: %q", mc.lastCreate.Description)
	}
	if mm.calls != 1 {
		t.Fatalf("ack calls = %d, want 1", mm.calls)
	}
	if mm.last.AuthorOverride != "multica-bot" {
		t.Errorf("ack AuthorOverride = %q, want multica-bot", mm.last.AuthorOverride)
	}
	if mm.last.RootID != "mm-post-1" {
		t.Errorf("ack RootID = %q, want mm-post-1", mm.last.RootID)
	}
	if !strings.Contains(mm.last.Message, "Создан") || !strings.Contains(mm.last.Message, "PUL-100") {
		t.Errorf("ack message = %q", mm.last.Message)
	}
	// Thread mapping recorded.
	thread, err := st.ThreadByRoot(context.Background(), "mm-post-1")
	if err != nil {
		t.Fatalf("thread lookup: %v", err)
	}
	if thread.MulticaIssueID != "issue-uuid-default" {
		t.Errorf("thread.MulticaIssueID = %q", thread.MulticaIssueID)
	}
	// Both inbound and outbound (ack) recorded for dedup (finding #7).
	seenInbound, _ := st.IsSyncedPost(context.Background(), "mm-post-1")
	seenOutbound, _ := st.IsSyncedPost(context.Background(), "mm-ack-id")
	if !seenInbound || !seenOutbound {
		t.Errorf("sync flags: inbound=%v outbound=%v, want both true", seenInbound, seenOutbound)
	}
}

func TestInbound_ChannelNotAllowedIgnored(t *testing.T) {
	h, mc, mm, _ := newInboundFixture(t)
	post := events.Post{ID: "p", UserID: "user-lina", ChannelID: "chan-other", Message: "x"}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.createCalls != 0 || mm.calls != 0 {
		t.Errorf("expected no-op for non-allowlisted channel: create=%d ack=%d", mc.createCalls, mm.calls)
	}
}

func TestInbound_UserNotAllowedIgnored(t *testing.T) {
	h, mc, _, _ := newInboundFixture(t)
	post := events.Post{ID: "p", UserID: "user-trespasser", ChannelID: "chan-data", Message: "x"}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.createCalls != 0 {
		t.Errorf("expected ignore for non-whitelisted user")
	}
}

func TestInbound_BotUserIDEcho_Filtered(t *testing.T) {
	h, mc, _, _ := newInboundFixture(t)
	post := events.Post{ID: "p", UserID: "user-bot", ChannelID: "chan-data", Message: "echo"}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.createCalls != 0 {
		t.Errorf("expected echo filter (UserID==BotUserID) to drop")
	}
}

// Finding #7: even if the user_id matched (e.g. token rotation gives the bot
// a new id), the second-layer dedup via mm_synced_posts must catch the replay.
func TestInbound_DedupTableCatchesReplay(t *testing.T) {
	h, mc, mm, st := newInboundFixture(t)
	_ = st.RecordSyncedPost(context.Background(), "mm-already-seen", "", state.DirectionMMToMulitca)
	post := events.Post{ID: "mm-already-seen", UserID: "user-lina", ChannelID: "chan-data", Message: "replay"}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.createCalls != 0 || mm.calls != 0 {
		t.Errorf("replay not deduped: create=%d ack=%d", mc.createCalls, mm.calls)
	}
}

func TestInbound_ReplyAddsCommentToMappedIssue(t *testing.T) {
	h, mc, _, st := newInboundFixture(t)
	// Pre-existing thread mapping.
	_ = st.RecordThread(context.Background(), state.Thread{
		RootPostID:     "thread-root-1",
		ChannelID:      "chan-data",
		MulticaIssueID: "issue-uuid-mapped",
	})
	post := events.Post{
		ID:        "reply-1",
		UserID:    "user-lina",
		ChannelID: "chan-data",
		RootID:    "thread-root-1",
		Message:   "уточняю: gates КК тоже посчитай",
		Username:  "lina",
	}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.commentCalls != 1 {
		t.Fatalf("AddComment calls = %d", mc.commentCalls)
	}
	if mc.lastCommentIID != "issue-uuid-mapped" {
		t.Errorf("comment routed to %q, want issue-uuid-mapped", mc.lastCommentIID)
	}
	if !strings.Contains(mc.lastCommentBody, "From: @lina") {
		t.Errorf("comment missing author footer: %q", mc.lastCommentBody)
	}
}

func TestInbound_ReplyUnmappedThreadSilentlyIgnored(t *testing.T) {
	h, mc, _, _ := newInboundFixture(t)
	post := events.Post{
		ID:        "reply-2",
		UserID:    "user-lina",
		ChannelID: "chan-data",
		RootID:    "thread-not-in-state",
		Message:   "?",
	}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if mc.commentCalls != 0 {
		t.Errorf("AddComment called for unmapped thread")
	}
}

func TestInbound_CreateIssueFailureBubbles(t *testing.T) {
	h, mc, _, _ := newInboundFixture(t)
	mc.createErr = errors.New("multica API down")
	post := events.Post{ID: "p", UserID: "user-lina", ChannelID: "chan-data", Message: "x"}
	err := h.Handle(context.Background(), post)
	if err == nil {
		t.Fatal("expected error to bubble")
	}
	if !strings.Contains(err.Error(), "create issue") {
		t.Errorf("err = %v", err)
	}
}

func TestInbound_AckFailureIsNonFatal(t *testing.T) {
	h, mc, mm, st := newInboundFixture(t)
	mm.err = errors.New("MM down briefly")
	post := events.Post{ID: "mm-1", UserID: "user-lina", ChannelID: "chan-data", Message: "x"}
	if err := h.Handle(context.Background(), post); err != nil {
		t.Fatalf("handle should not fail when ack fails: %v", err)
	}
	if mc.createCalls != 1 {
		t.Errorf("issue should still be created")
	}
	// Thread mapping recorded even though ack failed.
	if _, err := st.ThreadByRoot(context.Background(), "mm-1"); err != nil {
		t.Errorf("thread should be recorded: %v", err)
	}
}
