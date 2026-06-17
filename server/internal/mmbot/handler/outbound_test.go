package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/mmbot/multicacli"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// fakeMulticaPoller pretends to be the multica CLI for outbound polling.
// Tests pre-load comments + issue status; outbound consumes them.
type fakeMulticaPoller struct {
	mu       sync.Mutex
	comments map[string][]multicacli.Comment
	issue    multicacli.Issue
}

func (f *fakeMulticaPoller) GetIssue(ctx context.Context, issueID string) (multicacli.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issue.ID == "" {
		// Default: return empty status — tests that don't care about status
		// transitions thus see no extra notice posts. Tests that DO care
		// set f.issue explicitly.
		return multicacli.Issue{ID: issueID, Identifier: "PUL-X"}, nil
	}
	return f.issue, nil
}

func (f *fakeMulticaPoller) ListComments(ctx context.Context, issueID string, since time.Time) ([]multicacli.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []multicacli.Comment{}
	for _, c := range f.comments[issueID] {
		if c.CreatedAt != "" {
			t, err := time.Parse(time.RFC3339, c.CreatedAt)
			if err == nil && !since.IsZero() && !t.After(since) {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// fakeMMUploader records every MM call so tests can assert post bodies,
// author overrides, and attachment counts.
type fakeMMUploader struct {
	mu          sync.Mutex
	posts       []rest.PostRequest
	postIDSeq   int32
	uploadCalls int32
	uploadErr   error
	uploadFID   string
}

func (f *fakeMMUploader) CreatePost(ctx context.Context, req rest.PostRequest) (rest.Post, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := atomic.AddInt32(&f.postIDSeq, 1)
	f.posts = append(f.posts, req)
	return rest.Post{
		ID:        "mm-out-" + itoa(int(id)),
		ChannelID: req.ChannelID,
		RootID:    req.RootID,
		Message:   req.Message,
		CreateAt:  time.Now().UnixMilli(),
	}, nil
}

func (f *fakeMMUploader) UploadFile(ctx context.Context, channelID, filename string, data io.Reader) (string, error) {
	atomic.AddInt32(&f.uploadCalls, 1)
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	_, _ = io.Copy(io.Discard, data)
	if f.uploadFID == "" {
		f.uploadFID = "file-default"
	}
	return f.uploadFID, nil
}

type fakeRenderer struct {
	pngs map[string][]byte
	err  error
}

func (r *fakeRenderer) Screenshot(ctx context.Context, notebook string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	if b, ok := r.pngs[notebook]; ok {
		return b, nil
	}
	return []byte{0x89, 0x50, 0x4e, 0x47}, nil
}

func newOutboundFixture(t *testing.T) (*Outbound, *fakeMulticaPoller, *fakeMMUploader, *fakeRenderer, *state.Store) {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mp := &fakeMulticaPoller{comments: map[string][]multicacli.Comment{}}
	mm := &fakeMMUploader{}
	rend := &fakeRenderer{pngs: map[string][]byte{}}

	o := NewOutbound(OutboundConfig{
		Store:            st,
		Multica:          mp,
		MM:               mm,
		Render:           rend,
		AgentMulticaID:   "agent-uuid",
		TailnetHostHint:  "ts.net",
		ScreenshotWindow: 60 * time.Second,
	})
	return o, mp, mm, rend, st
}

func TestOutbound_ForwardsNewCommentToMM(t *testing.T) {
	o, mp, mm, _, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "root1", ChannelID: "c1", MulticaIssueID: "issue-A"})
	mp.comments["issue-A"] = []multicacli.Comment{
		{ID: "comment-1", IssueID: "issue-A", AuthorID: "agent-uuid", Content: "Привет", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(mm.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(mm.posts))
	}
	got := mm.posts[0]
	if got.RootID != "root1" {
		t.Errorf("RootID = %q", got.RootID)
	}
	if got.Message != "Привет" {
		t.Errorf("Message = %q", got.Message)
	}
	// Finding #8: agent comments get the "agent-1 ↪ marimo-pair" author.
	if got.AuthorOverride != "agent-1 ↪ marimo-pair" {
		t.Errorf("AuthorOverride = %q, want agent-1 ↪ marimo-pair", got.AuthorOverride)
	}
	// dedup recorded for both directions.
	seenComment, _ := st.IsSyncedComment(context.Background(), "comment-1")
	if !seenComment {
		t.Error("synced_comment not recorded")
	}
	seenPost, _ := st.IsSyncedPost(context.Background(), "mm-out-1")
	if !seenPost {
		t.Error("synced_post (outbound direction) not recorded — finding #7 dedup missing")
	}
}

func TestOutbound_DoesNotDoubleForward(t *testing.T) {
	o, mp, mm, _, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "root", ChannelID: "c", MulticaIssueID: "i"})
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "first", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll1: %v", err)
	}
	// Second poll with the same comment present — must not double-post.
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll2: %v", err)
	}
	if len(mm.posts) != 1 {
		t.Errorf("posts = %d, want 1 (idempotent across polls)", len(mm.posts))
	}
}

func TestOutbound_StatusChangeEmitsOneServiceMessage(t *testing.T) {
	o, mp, mm, _, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	mp.issue = multicacli.Issue{ID: "i", Identifier: "PUL-1", Status: "waiting"}

	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll1: %v", err)
	}
	if len(mm.posts) != 1 {
		t.Fatalf("posts = %d, want 1 (status notice)", len(mm.posts))
	}
	if !strings.Contains(mm.posts[0].Message, "Статус:") {
		t.Errorf("status post = %q", mm.posts[0].Message)
	}
	// Repeat without status change — no new posts.
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll2: %v", err)
	}
	if len(mm.posts) != 1 {
		t.Errorf("status post repeated; total posts = %d", len(mm.posts))
	}

	// Now flip the status; expect one more notice.
	mp.issue.Status = "deployed"
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll3: %v", err)
	}
	if len(mm.posts) != 2 {
		t.Errorf("expected new status post after change, got total %d", len(mm.posts))
	}
}

// Finding #1: idempotent per-comment screenshot. Three agent comments with
// tailnet URLs at intervals of 10s → only one PNG (rate-limited). 65s later,
// another comment → second PNG.
func TestOutbound_ScreenshotIdempotentRateLimit(t *testing.T) {
	o, mp, mm, _, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})

	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "See https://x.ts.net/?file=PUL-1.py", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll1: %v", err)
	}
	if atomic.LoadInt32(&mm.uploadCalls) != 1 {
		t.Errorf("first poll uploads = %d, want 1", mm.uploadCalls)
	}

	// Second comment inside the 60s window → screenshot must be skipped.
	mp.comments["i"] = append(mp.comments["i"], multicacli.Comment{
		ID: "c-2", IssueID: "i", AuthorID: "agent-uuid",
		Content:   "Update https://x.ts.net/?file=PUL-1.py",
		CreatedAt: "2026-06-17T05:00:30Z",
	})
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll2: %v", err)
	}
	if atomic.LoadInt32(&mm.uploadCalls) != 1 {
		t.Errorf("inside-window upload count = %d, want still 1 (rate-limited)", mm.uploadCalls)
	}

	// Move the rate-limit cursor backward to simulate >60s passing.
	_ = st.MarkRendered(context.Background(), "i", time.Now().Add(-2*time.Minute))

	mp.comments["i"] = append(mp.comments["i"], multicacli.Comment{
		ID: "c-3", IssueID: "i", AuthorID: "agent-uuid",
		Content:   "Third https://x.ts.net/?file=PUL-1.py",
		CreatedAt: "2026-06-17T05:02:00Z",
	})
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll3: %v", err)
	}
	if atomic.LoadInt32(&mm.uploadCalls) != 2 {
		t.Errorf("after window upload count = %d, want 2", mm.uploadCalls)
	}
}

func TestOutbound_NoChartCommentIsTextOnly(t *testing.T) {
	o, mp, mm, rend, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "просто текст без URL", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	rend.err = nil
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if mm.uploadCalls != 0 {
		t.Errorf("no tailnet URL → upload count = %d, want 0", mm.uploadCalls)
	}
	if len(mm.posts) != 1 {
		t.Fatalf("posts = %d", len(mm.posts))
	}
	if len(mm.posts[0].FileIDs) != 0 {
		t.Errorf("FileIDs = %v, want empty", mm.posts[0].FileIDs)
	}
}

func TestOutbound_ScreenshotRenderFailureFallsBackToText(t *testing.T) {
	o, mp, mm, rend, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	rend.err = errors.New("chrome crash")
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "Look: https://x.ts.net/?file=PUL-1.py", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(mm.posts) != 1 {
		t.Fatalf("posts = %d", len(mm.posts))
	}
	if len(mm.posts[0].FileIDs) != 0 {
		t.Errorf("FileIDs should be empty on render failure, got %v", mm.posts[0].FileIDs)
	}
}

func TestOutbound_BotAuthoredCommentsSkipped(t *testing.T) {
	o, mp, mm, _, st := newOutboundFixture(t)
	o.cfg.MMBotMulticaID = "bot-author-uuid"
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-bot", IssueID: "i", AuthorID: "bot-author-uuid", Content: "echo", CreatedAt: "2026-06-17T05:00:00Z"},
		{ID: "c-user", IssueID: "i", AuthorID: "agent-uuid", Content: "real", CreatedAt: "2026-06-17T05:00:01Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(mm.posts) != 1 || mm.posts[0].Message != "real" {
		t.Errorf("expected only the real comment, got %#v", mm.posts)
	}
}

func TestOutbound_NoActiveThreads_NoOp(t *testing.T) {
	o, _, mm, _, _ := newOutboundFixture(t)
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(mm.posts) != 0 || mm.uploadCalls != 0 {
		t.Errorf("clean state should produce no MM activity")
	}
}

func TestOutbound_PerIssueCursorAdvances(t *testing.T) {
	o, mp, _, _, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "first", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll1: %v", err)
	}
	cur, _ := st.GetMeta(context.Background(), state.MetaKey("issue_cursor_i"))
	if cur != "2026-06-17T05:00:00Z" {
		t.Errorf("cursor = %q, want 2026-06-17T05:00:00Z", cur)
	}

	// Add a comment older than the cursor — must not re-forward.
	mp.comments["i"] = append(mp.comments["i"], multicacli.Comment{
		ID: "c-stale", IssueID: "i", AuthorID: "agent-uuid",
		Content: "older", CreatedAt: "2026-06-17T04:00:00Z",
	})
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll2: %v", err)
	}
	// And one newer — must forward.
	mp.comments["i"] = append(mp.comments["i"], multicacli.Comment{
		ID: "c-2", IssueID: "i", AuthorID: "agent-uuid",
		Content: "second", CreatedAt: "2026-06-17T05:30:00Z",
	})
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll3: %v", err)
	}
	cur, _ = st.GetMeta(context.Background(), state.MetaKey("issue_cursor_i"))
	if cur != "2026-06-17T05:30:00Z" {
		t.Errorf("cursor = %q, want 2026-06-17T05:30:00Z (advanced)", cur)
	}
}

// Confirm the renderer reader sees the PNG bytes (basic plumbing test).
func TestOutbound_PNGBytesFlowToUpload(t *testing.T) {
	o, mp, mm, rend, st := newOutboundFixture(t)
	_ = st.RecordThread(context.Background(), state.Thread{RootPostID: "r", ChannelID: "c", MulticaIssueID: "i"})
	rend.pngs = map[string][]byte{"PUL-X.py": {0xAA, 0xBB, 0xCC}}
	mp.comments["i"] = []multicacli.Comment{
		{ID: "c-1", IssueID: "i", AuthorID: "agent-uuid", Content: "see https://x.ts.net/?file=PUL-X.py", CreatedAt: "2026-06-17T05:00:00Z"},
	}
	mm.uploadFID = "file-uuid"
	if err := o.PollOnce(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if atomic.LoadInt32(&mm.uploadCalls) != 1 {
		t.Errorf("upload calls = %d, want 1", mm.uploadCalls)
	}
	if len(mm.posts) != 1 || len(mm.posts[0].FileIDs) != 1 || mm.posts[0].FileIDs[0] != "file-uuid" {
		t.Errorf("FileIDs = %v, want [file-uuid]", mm.posts[0].FileIDs)
	}
}

// Drain the io.Reader interface match for the fakeMMUploader signature.
var _ io.Reader = bytes.NewReader(nil)

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
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
