package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(Config{
		BaseURL:     srv.URL,
		Token:       "bot-pat-token",
		BotUserID:   "bot-user-id",
		HTTPClient:  srv.Client(),
		MaxAttempts: 4,
		BaseDelay:   1 * time.Millisecond, // fast tests
		MaxDelay:    4 * time.Millisecond,
		Sleep:       func(context.Context, time.Duration) {}, // synchronous, no real sleep
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c, srv
}

func TestNew_RequiresBaseURLAndToken(t *testing.T) {
	if _, err := New(Config{Token: "x"}); err == nil {
		t.Error("expected error on empty BaseURL")
	}
	if _, err := New(Config{BaseURL: "http://x"}); err == nil {
		t.Error("expected error on empty Token")
	}
}

func TestCreatePost_TopLevelHappyPath(t *testing.T) {
	var captured map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/posts" {
			t.Errorf("path = %s, want /api/v4/posts", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer bot-pat-token" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(Post{
			ID:        "post-xyz",
			UserID:    "bot-user-id",
			ChannelID: "chan-1",
			Message:   "hello",
			CreateAt:  1718500000000,
		})
	}))
	got, err := c.CreatePost(context.Background(), PostRequest{
		ChannelID: "chan-1",
		Message:   "hello",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.ID != "post-xyz" {
		t.Errorf("post id = %q, want post-xyz", got.ID)
	}
	if captured["message"] != "hello" {
		t.Errorf("captured message = %v, want hello", captured["message"])
	}
	if _, ok := captured["root_id"]; ok {
		t.Errorf("expected no root_id on top-level post, got %v", captured["root_id"])
	}
	if _, ok := captured["props"]; ok {
		t.Errorf("expected no props on plain post, got %v", captured["props"])
	}
}

func TestCreatePost_ReplyIncludesRootID(t *testing.T) {
	var captured map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(Post{ID: "p1"})
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{
		ChannelID: "c",
		RootID:    "root-abc",
		Message:   "reply",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if captured["root_id"] != "root-abc" {
		t.Errorf("root_id = %v, want root-abc", captured["root_id"])
	}
}

// Finding #8: per-message MM author attribution via props.attachments.
// When AuthorOverride is set, the top-level message field is left empty and
// the content moves into attachments[0].text.
func TestCreatePost_AuthorOverrideUsesAttachments(t *testing.T) {
	var captured map[string]any
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(Post{ID: "p1"})
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{
		ChannelID:      "c",
		RootID:         "root",
		Message:        "Hello, Лина — let me check the data.",
		AuthorOverride: "agent-1 ↪ marimo-pair",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if captured["message"] != "" {
		t.Errorf("expected empty top-level message with override, got %q", captured["message"])
	}
	props, ok := captured["props"].(map[string]any)
	if !ok {
		t.Fatalf("missing props in payload, got %v", captured)
	}
	attachments, ok := props["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %v", props["attachments"])
	}
	att, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("attachment not an object: %T", attachments[0])
	}
	if att["author_name"] != "agent-1 ↪ marimo-pair" {
		t.Errorf("author_name = %q, want agent-1 ↪ marimo-pair", att["author_name"])
	}
	if att["text"] != "Hello, Лина — let me check the data." {
		t.Errorf("attachment text = %q, want the user message", att["text"])
	}
}

func TestCreatePost_RetriesOn5xxAndSucceeds(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"upstream down"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(Post{ID: "p1"})
	}))
	got, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.ID != "p1" {
		t.Errorf("post id = %q", got.ID)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 attempts (2 fails + 1 success), got %d", calls)
	}
}

func TestCreatePost_ExhaustsRetriesOn5xx(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
	if !errors.Is(err, ErrTransient) {
		t.Errorf("error class = %v, want ErrTransient", err)
	}
	if atomic.LoadInt32(&calls) != 4 {
		t.Errorf("expected 4 attempts, got %d", calls)
	}
}

func TestCreatePost_UnauthorizedIsTerminal(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"id":"api.context.session_expired.app_error"}`))
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 attempt (no retry on 401), got %d", calls)
	}
}

func TestCreatePost_BadRequestIsTerminal(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"id":"api.post.create_post.app_error"}`))
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("error = %v, want ErrBadRequest", err)
	}
}

func TestCreatePost_NetworkErrorRetried(t *testing.T) {
	// Server that hangs up the connection. httptest.NewServer + close gives
	// us net-error semantics without standing up TLS.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL:     srv.URL,
		Token:       "t",
		BotUserID:   "b",
		HTTPClient:  srv.Client(),
		MaxAttempts: 3,
		BaseDelay:   1 * time.Millisecond,
		Sleep:       func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	_, err = c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrTransient) {
		t.Errorf("error class = %v, want ErrTransient", err)
	}
}

func TestCreatePost_ContextCancellationStops(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := New(Config{
		BaseURL:     srv.URL,
		Token:       "t",
		BotUserID:   "b",
		HTTPClient:  srv.Client(),
		MaxAttempts: 10,
		BaseDelay:   1 * time.Millisecond,
		Sleep:       func(context.Context, time.Duration) {},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.CreatePost(ctx, PostRequest{ChannelID: "c", Message: "x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled (got %T)", err, err)
	}
}

func TestUploadFile_HappyPathReturnsFirstID(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/files" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("channel_id") != "chan-Q" {
			t.Errorf("channel_id qs = %q", r.URL.Query().Get("channel_id"))
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("content-type = %q", ct)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"file_infos": []map[string]any{{"id": "file-1"}},
		})
	}))
	id, err := c.UploadFile(context.Background(), "chan-Q", "cell.png", strings.NewReader("PNGDATA"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if id != "file-1" {
		t.Errorf("file id = %q, want file-1", id)
	}
}

func TestUploadFile_EmptyFileInfosIsError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"file_infos": []any{}})
	}))
	_, err := c.UploadFile(context.Background(), "c", "x.png", strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error on empty file_infos")
	}
}

func TestUploadFile_RetriesUploadOn5xx(t *testing.T) {
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the multipart body so we can assert each attempt re-uploaded.
		_, _ = io.Copy(io.Discard, r.Body)
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"file_infos": []map[string]any{{"id": "fid"}},
		})
	}))
	id, err := c.UploadFile(context.Background(), "c", "f.png", strings.NewReader("DATA"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if id != "fid" || atomic.LoadInt32(&calls) != 3 {
		t.Errorf("got id=%q calls=%d, want fid/3", id, calls)
	}
}

// PostsAfter is the catch-up endpoint that runs after every WS reconnect
// (finding #5 of /plan-eng-review). It must page through MM's output and
// strictly exclude the sinceMs anchor itself.
func TestPostsAfter_PagesAndOrdersAscending(t *testing.T) {
	// Pretend MM returns three pages: ages 1000, 2000, 3000 ms; sinceMs = 1500
	// → expect 2 posts back (at 2000 and 3000), sorted ascending.
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"order": []string{"p3", "p2", "p1"},
				"posts": map[string]Post{
					"p1": {ID: "p1", CreateAt: 1000, Message: "anchor-or-before"},
					"p2": {ID: "p2", CreateAt: 2000, Message: "two"},
					"p3": {ID: "p3", CreateAt: 3000, Message: "three"},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"order": []string{}, "posts": map[string]Post{}})
		}
	}))
	posts, err := c.PostsAfter(context.Background(), "chan-A", 1500)
	if err != nil {
		t.Fatalf("postsAfter: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2: %#v", len(posts), posts)
	}
	if posts[0].ID != "p2" || posts[1].ID != "p3" {
		t.Errorf("order: got [%s, %s], want [p2, p3]", posts[0].ID, posts[1].ID)
	}
}

func TestPostsAfter_ExcludesSinceAnchor(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"order": []string{"p1"},
			"posts": map[string]Post{
				"p1": {ID: "p1", CreateAt: 1500, Message: "exactly at anchor"},
			},
		})
	}))
	posts, err := c.PostsAfter(context.Background(), "c", 1500)
	if err != nil {
		t.Fatalf("postsAfter: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("expected 0 posts (anchor excluded), got %d", len(posts))
	}
}

func TestPostsAfter_StopsWhenPageHasNoNewPosts(t *testing.T) {
	// First page returns one fresh + one duplicate from prior page.
	// Second page returns only duplicates → stop.
	var calls int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"order": []string{"p1"},
				"posts": map[string]Post{
					"p1": {ID: "p1", CreateAt: 2000},
				},
			})
		case 2:
			// Same id again, no new content.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"order": []string{"p1"},
				"posts": map[string]Post{
					"p1": {ID: "p1", CreateAt: 2000},
				},
			})
		default:
			t.Fatalf("PostsAfter kept paginating past empty page (call #%d)", n)
		}
	}))
	posts, err := c.PostsAfter(context.Background(), "c", 1000)
	if err != nil {
		t.Fatalf("postsAfter: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "p1" {
		t.Errorf("got %#v, want one post p1", posts)
	}
}

func TestNextDelay_CapsAtMax(t *testing.T) {
	cases := []struct {
		in, max, want time.Duration
	}{
		{1 * time.Second, 60 * time.Second, 2 * time.Second},
		{2 * time.Second, 60 * time.Second, 4 * time.Second},
		{40 * time.Second, 60 * time.Second, 60 * time.Second},
		{60 * time.Second, 60 * time.Second, 60 * time.Second},
	}
	for _, c := range cases {
		got := nextDelay(c.in, c.max)
		if got != c.want {
			t.Errorf("nextDelay(%v, %v) = %v, want %v", c.in, c.max, got, c.want)
		}
	}
}

func TestPost_CreatedAtConverts(t *testing.T) {
	p := Post{CreateAt: 1718500000000}
	want := time.UnixMilli(1718500000000)
	if !p.CreatedAt().Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", p.CreatedAt(), want)
	}
}

func TestTruncate(t *testing.T) {
	got := truncate([]byte("hello world"), 5)
	if got != "hello…" {
		t.Errorf("truncate = %q", got)
	}
	got2 := truncate([]byte("short"), 100)
	if got2 != "short" {
		t.Errorf("truncate short = %q", got2)
	}
}

// Smoke-test that a 200 with malformed JSON yields a sensible decode error
// rather than corrupting `out`.
func TestDecodeError_Surfaces(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	_, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "x"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

// Light end-to-end check that the recorded ID and create-time round-trip
// matter for the handler use-case (mm_synced_posts insert + meta update).
func TestCreatePost_ReturnsFieldsForStateUpdate(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"p-final","user_id":"bot-user-id","channel_id":"c","message":"ok","create_at":1718500000000}`)
	}))
	got, err := c.CreatePost(context.Background(), PostRequest{ChannelID: "c", Message: "ok"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.ID != "p-final" || got.UserID != "bot-user-id" || got.ChannelID != "c" {
		t.Errorf("fields lost in round-trip: %+v", got)
	}
}
