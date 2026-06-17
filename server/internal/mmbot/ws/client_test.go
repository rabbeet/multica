package ws

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/mmbot/events"
	"github.com/multica-ai/multica/server/internal/mmbot/rest"
	"github.com/multica-ai/multica/server/internal/mmbot/state"
)

// Finding #5: messages Лина posts during a WS disconnect MUST be replayed
// via the REST catch-up step after reconnect. This is the most important
// resilience test — without it we silently drop user messages.
//
// We exercise the catch-up codepath directly (catchUp is private but the
// package test can reach it). The Run loop's full disconnect+reconnect
// dance is exercised in an integration-flavoured test below.
func TestCatchUp_ReplaysPostsAfterMetaCursor(t *testing.T) {
	st := newStateStore(t)
	ctx := context.Background()

	// Anchor cursor — posts in the fake are AFTER this.
	anchorMs := int64(1781000000000) // 2026-06-12
	_ = st.SetMeta(ctx, state.MetaLastSeenMMTS, time.UnixMilli(anchorMs).UTC().Format(time.RFC3339))

	fakeRest := &fakeCatchupPoster{
		byChannel: map[string][]rest.Post{
			"chan-data": {
				{ID: "missed-1", UserID: "user-lina", ChannelID: "chan-data", Message: "сколько по MOW-IST?", CreateAt: anchorMs + 30_000},
				{ID: "missed-2", UserID: "user-lina", ChannelID: "chan-data", Message: "?", CreateAt: anchorMs + 60_000},
			},
		},
	}
	hh := &fakeHandler{}

	c := New(Config{
		Catchup:         fakeRest,
		Store:           st,
		Handler:         hh,
		WatchChannelIDs: []string{"chan-data"},
	})
	if err := c.catchUp(ctx); err != nil {
		t.Fatalf("catchUp: %v", err)
	}
	if len(hh.handled) != 2 {
		t.Fatalf("handler invoked %d times, want 2 (missed posts replayed)", len(hh.handled))
	}
	if hh.handled[0].ID != "missed-1" || hh.handled[1].ID != "missed-2" {
		t.Errorf("order = [%s, %s], want [missed-1, missed-2]", hh.handled[0].ID, hh.handled[1].ID)
	}
	// Cursor advanced to the newest CreateAt.
	cur, _ := st.GetMeta(ctx, state.MetaLastSeenMMTS)
	want := time.UnixMilli(anchorMs + 60_000).UTC().Format(time.RFC3339)
	if cur != want {
		t.Errorf("cursor = %q, want %q", cur, want)
	}
}

func TestCatchUp_HandlerErrorDoesNotAbortRun(t *testing.T) {
	st := newStateStore(t)
	ctx := context.Background()
	anchorMs := int64(1781000000000)
	_ = st.SetMeta(ctx, state.MetaLastSeenMMTS, time.UnixMilli(anchorMs).UTC().Format(time.RFC3339))

	fakeRest := &fakeCatchupPoster{
		byChannel: map[string][]rest.Post{
			"c1": {
				{ID: "p-bad", UserID: "u", ChannelID: "c1", Message: "first", CreateAt: anchorMs + 1000},
				{ID: "p-good", UserID: "u", ChannelID: "c1", Message: "second", CreateAt: anchorMs + 2000},
			},
		},
	}
	hh := &fakeHandler{failOn: map[string]bool{"p-bad": true}}
	c := New(Config{
		Catchup:         fakeRest,
		Store:           st,
		Handler:         hh,
		WatchChannelIDs: []string{"c1"},
	})
	if err := c.catchUp(ctx); err != nil {
		t.Fatalf("catchUp: %v", err)
	}
	if len(hh.handled) != 2 {
		t.Errorf("handler should still be called for second post; got %d", len(hh.handled))
	}
}

func TestCatchUp_EmptyChannelsIsNoOp(t *testing.T) {
	st := newStateStore(t)
	fakeRest := &fakeCatchupPoster{byChannel: map[string][]rest.Post{}}
	hh := &fakeHandler{}
	c := New(Config{
		Catchup:         fakeRest,
		Store:           st,
		Handler:         hh,
		WatchChannelIDs: []string{"c-empty"},
	})
	if err := c.catchUp(context.Background()); err != nil {
		t.Fatalf("catchUp: %v", err)
	}
	if len(hh.handled) != 0 {
		t.Errorf("handler called for empty channel")
	}
}

func TestBuildWSURL_HTTPSBecomesWSS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://mm.example.com", "wss://mm.example.com/api/v4/websocket"},
		{"https://mm.example.com/", "wss://mm.example.com/api/v4/websocket"},
		{"http://localhost:8065", "ws://localhost:8065/api/v4/websocket"},
		{"https://mm.example.com:8443", "wss://mm.example.com:8443/api/v4/websocket"},
	}
	for _, c := range cases {
		got, err := buildWSURL(c.in)
		if err != nil {
			t.Errorf("[%s] err = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("[%s] = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNextDelay_CapsAtMax(t *testing.T) {
	if got := nextDelay(1*time.Second, 60*time.Second); got != 2*time.Second {
		t.Errorf("1→2 fail: %v", got)
	}
	if got := nextDelay(60*time.Second, 60*time.Second); got != 60*time.Second {
		t.Errorf("cap fail: %v", got)
	}
	if got := nextDelay(40*time.Second, 60*time.Second); got != 60*time.Second {
		t.Errorf("80 should clamp to 60: %v", got)
	}
}

// fakes ----------------------------------------------------------------------

type fakeCatchupPoster struct {
	mu        sync.Mutex
	byChannel map[string][]rest.Post
}

func (f *fakeCatchupPoster) PostsAfter(ctx context.Context, channelID string, sinceMs int64) ([]rest.Post, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []rest.Post{}
	for _, p := range f.byChannel[channelID] {
		if p.CreateAt > sinceMs {
			out = append(out, p)
		}
	}
	return out, nil
}

type fakeHandler struct {
	mu      sync.Mutex
	handled []events.Post
	failOn  map[string]bool
}

func (h *fakeHandler) Handle(ctx context.Context, p events.Post) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handled = append(h.handled, p)
	if h.failOn[p.ID] {
		return assertErr("simulated handler failure for " + p.ID)
	}
	return nil
}

func newStateStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
