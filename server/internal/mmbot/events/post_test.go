package events

import (
	"encoding/json"
	"errors"
	"testing"
)

// Real-world MM `posted` event shape — verified against MM 9.x payloads.
// data.post arrives as a JSON-encoded STRING, not an inline object.
const samplePostedEvent = `{
  "event": "posted",
  "data": {
    "channel_display_name": "data-requests",
    "channel_name": "data-requests",
    "channel_type": "O",
    "post": "{\"id\":\"abc123\",\"create_at\":1718500000000,\"user_id\":\"user-lina\",\"channel_id\":\"chan-data\",\"root_id\":\"\",\"message\":\"сколько мы продали MOW-IST за май?\"}",
    "sender_name": "@lina",
    "team_id": "team-pulse"
  },
  "broadcast": {"channel_id": "chan-data", "team_id": "team-pulse"},
  "seq": 42
}`

func TestDecodePosted_HappyPath(t *testing.T) {
	p, err := DecodePosted([]byte(samplePostedEvent))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.ID != "abc123" {
		t.Errorf("ID = %q", p.ID)
	}
	if p.UserID != "user-lina" {
		t.Errorf("UserID = %q", p.UserID)
	}
	if p.ChannelID != "chan-data" {
		t.Errorf("ChannelID = %q", p.ChannelID)
	}
	if p.RootID != "" {
		t.Errorf("RootID = %q, want empty (top-level)", p.RootID)
	}
	if p.Message != "сколько мы продали MOW-IST за май?" {
		t.Errorf("Message = %q", p.Message)
	}
	if p.Username != "lina" {
		t.Errorf("Username = %q, want lina", p.Username)
	}
	if !p.IsTopLevel() {
		t.Error("expected IsTopLevel=true for empty RootID")
	}
	if p.RootOrSelf() != "abc123" {
		t.Errorf("RootOrSelf = %q, want abc123", p.RootOrSelf())
	}
}

func TestDecodePosted_ThreadReplyKeepsRootID(t *testing.T) {
	event := `{
        "event": "posted",
        "data": {"post": "{\"id\":\"r1\",\"user_id\":\"u\",\"channel_id\":\"c\",\"root_id\":\"thread-root-zzz\",\"message\":\"reply\"}", "sender_name": "@lina"},
        "seq": 1
    }`
	p, err := DecodePosted([]byte(event))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.RootID != "thread-root-zzz" {
		t.Errorf("RootID = %q", p.RootID)
	}
	if p.IsTopLevel() {
		t.Error("reply should not report IsTopLevel=true")
	}
	if p.RootOrSelf() != "thread-root-zzz" {
		t.Errorf("RootOrSelf = %q, want thread-root-zzz", p.RootOrSelf())
	}
}

func TestDecodePosted_InlinePostObjectAlsoWorks(t *testing.T) {
	// Some MM versions deliver post as a nested object rather than a JSON
	// string. Decoder must tolerate both.
	event := `{
        "event": "posted",
        "data": {"post": {"id": "x", "user_id": "u", "channel_id": "c", "message": "inline"}},
        "seq": 5
    }`
	p, err := DecodePosted([]byte(event))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.ID != "x" || p.Message != "inline" {
		t.Errorf("inline post lost: %+v", p)
	}
}

func TestDecodePosted_NonPostedReturnsSentinel(t *testing.T) {
	cases := []string{
		`{"event":"typing","seq":1}`,
		`{"event":"channel_viewed","seq":2}`,
		`{"event":"hello","seq":0}`,
		`{"event":"status_change","seq":7}`,
	}
	for _, c := range cases {
		_, err := DecodePosted([]byte(c))
		if !errors.Is(err, ErrNotPosted) {
			t.Errorf("[%s] err = %v, want ErrNotPosted", c, err)
		}
	}
}

func TestDecodePosted_MalformedJSONIsError(t *testing.T) {
	_, err := DecodePosted([]byte(`{event:"posted",data:`))
	if err == nil {
		t.Fatal("expected JSON error")
	}
}

func TestDecodePosted_MissingPostInData(t *testing.T) {
	_, err := DecodePosted([]byte(`{"event":"posted","data":{"sender_name":"@bot"},"seq":1}`))
	if err == nil || !errBodyContains(err, "missing data.post") {
		t.Errorf("err = %v, want missing-post error", err)
	}
}

func TestDecodePosted_PostNeitherStringNorObject(t *testing.T) {
	// data.post is a number — neither shape we support.
	raw := []byte(`{"event":"posted","data":{"post":12345},"seq":1}`)
	_, err := DecodePosted(raw)
	if err == nil {
		t.Fatal("expected error on non-string non-object data.post")
	}
}

func TestDecodePosted_SenderNameStripsLeadingAt(t *testing.T) {
	event := `{
        "event": "posted",
        "data": {"post": "{\"id\":\"a\",\"user_id\":\"u\",\"channel_id\":\"c\"}", "sender_name": "@vadim"},
        "seq": 0
    }`
	p, _ := DecodePosted([]byte(event))
	if p.Username != "vadim" {
		t.Errorf("Username = %q, want vadim (no @)", p.Username)
	}
}

func TestDecodePosted_NoSenderName(t *testing.T) {
	event := `{"event":"posted","data":{"post":"{\"id\":\"a\",\"user_id\":\"u\",\"channel_id\":\"c\"}"},"seq":0}`
	p, _ := DecodePosted([]byte(event))
	if p.Username != "" {
		t.Errorf("Username should be empty without sender_name, got %q", p.Username)
	}
}

func TestEventEnvelopeFields(t *testing.T) {
	var env Event
	_ = json.Unmarshal([]byte(samplePostedEvent), &env)
	if env.Event != "posted" {
		t.Errorf("Event = %q", env.Event)
	}
	if env.Seq != 42 {
		t.Errorf("Seq = %d, want 42", env.Seq)
	}
	if env.Broadcast.ChannelID != "chan-data" {
		t.Errorf("Broadcast.ChannelID = %q", env.Broadcast.ChannelID)
	}
}

func errBodyContains(err error, want string) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), want)
}

func contains(s, want string) bool {
	for i := 0; i+len(want) <= len(s); i++ {
		if s[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
