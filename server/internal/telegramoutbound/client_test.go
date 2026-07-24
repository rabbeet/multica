package telegramoutbound

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient starts an httptest server that answers a single Bot API
// method with the caller-supplied handler, and returns a Client wired
// to it.
func newTestClient(t *testing.T, method string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/botTEST/" + method
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path %q, want %q", r.URL.Path, wantPath)
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "TEST", srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClient_MissingToken(t *testing.T) {
	if _, err := NewClient("", "", nil); err != ErrConfig {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}

func TestSendMessage_HappyPath(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("bad body: %v", err)
		}
		if want := float64(-100123); got["chat_id"] != want {
			t.Errorf("chat_id got %v want %v", got["chat_id"], want)
		}
		if want := float64(7); got["message_thread_id"] != want {
			t.Errorf("message_thread_id got %v want %v", got["message_thread_id"], want)
		}
		if got["text"] != "hello world" {
			t.Errorf("text got %v", got["text"])
		}
		// Ensure we did NOT send parse_mode (plain-text mode).
		if _, present := got["parse_mode"]; present {
			t.Errorf("parse_mode should be absent, got %v", got["parse_mode"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	})
	res := c.SendMessage(context.Background(), -100123, 7, "hello world")
	if res.Outcome != OutcomeOK {
		t.Fatalf("outcome=%v result=%+v", res.Outcome, res)
	}
	if res.MessageID != 42 {
		t.Errorf("message_id got %d want 42", res.MessageID)
	}
}

func TestSendMessage_RateLimit(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":42}}`))
	})
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.Outcome != OutcomeRateLimit {
		t.Fatalf("outcome=%v", res.Outcome)
	}
	if res.RetryAfter != 42*time.Second {
		t.Errorf("retry_after got %v want 42s", res.RetryAfter)
	}
}

func TestSendMessage_RateLimit_ZeroBecomesOneSecond(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		// retry_after=0 seen in "flood control" responses; must not
		// turn into a 0s wait that spin-loops the scheduler.
		_, _ = w.Write([]byte(`{"ok":false,"parameters":{"retry_after":0}}`))
	})
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.RetryAfter != time.Second {
		t.Errorf("retry_after got %v want 1s (zero must clamp)", res.RetryAfter)
	}
}

func TestSendMessage_ServerError_Transient(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>oops</html>`))
	})
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.Outcome != OutcomeTransient {
		t.Fatalf("outcome=%v", res.Outcome)
	}
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("status got %d", res.StatusCode)
	}
}

func TestSendMessage_BotKicked_Fatal(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked from the supergroup chat"}`))
	})
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.Outcome != OutcomeFatal {
		t.Fatalf("outcome=%v", res.Outcome)
	}
	if !strings.Contains(res.LastError(), "kicked") {
		t.Errorf("last error should mention kicked: %q", res.LastError())
	}
}

func TestSendMessage_TopicDeleted(t *testing.T) {
	c := newTestClient(t, "sendMessage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`))
	})
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.Outcome != OutcomeTopicDeleted {
		t.Fatalf("outcome=%v, want OutcomeTopicDeleted", res.Outcome)
	}
}

func TestCreateForumTopic_HappyPath(t *testing.T) {
	c := newTestClient(t, "createForumTopic", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		if got["name"] != "PUL-1 · title" {
			t.Errorf("name got %v", got["name"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_thread_id":99}}`))
	})
	res := c.CreateForumTopic(context.Background(), -100123, "PUL-1 · title")
	if res.Outcome != OutcomeOK {
		t.Fatalf("outcome=%v", res.Outcome)
	}
	if res.TopicID != 99 {
		t.Errorf("topic_id got %d", res.TopicID)
	}
}

func TestCreateForumTopic_LimitExceeded_Fatal(t *testing.T) {
	c := newTestClient(t, "createForumTopic", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: TOPICS_LIMIT_EXCEEDED"}`))
	})
	res := c.CreateForumTopic(context.Background(), -1, "any")
	if res.Outcome != OutcomeFatal {
		t.Fatalf("outcome=%v", res.Outcome)
	}
}

func TestSendMessage_TransportError(t *testing.T) {
	// Point client at an address that will refuse the connection to
	// force a transport-layer error path.
	c, err := NewClient("http://127.0.0.1:1", "TEST", &http.Client{Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	res := c.SendMessage(context.Background(), -1, 1, "x")
	if res.Outcome != OutcomeTransient {
		t.Fatalf("outcome=%v", res.Outcome)
	}
	if res.Err == nil {
		t.Errorf("expected transport err set")
	}
}

func TestTruncateTopicName(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-ten", 11, "exactly-ten"},
		{"too-long-name", 8, "too-lon…"},
		{"кириллица длинная", 5, "кири…"},
	}
	for _, tc := range tests {
		if got := truncateTopicName(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateTopicName(%q, %d) = %q want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
