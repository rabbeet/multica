// Package events decodes Mattermost WebSocket payloads into typed Go
// structs. The bot only cares about the `posted` event; everything else
// (typing, post_edited, channel_viewed, ...) is intentionally dropped at
// this layer so the handler stays focused.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package events

import (
	"encoding/json"
	"errors"
	"strings"
)

// Event is the common envelope MM puts every WS message into.
// Source: https://developers.mattermost.com/api-documentation/api-reference/#webhooks-server
type Event struct {
	Event string                 `json:"event"`
	Data  map[string]json.RawMessage `json:"data,omitempty"`
	Broadcast struct {
		ChannelID string `json:"channel_id"`
		TeamID    string `json:"team_id"`
		UserID    string `json:"user_id"`
	} `json:"broadcast,omitempty"`
	Seq int64 `json:"seq"`
}

// Post is the post object MM delivers inside data.post. Only the fields
// the bot acts on are surfaced; future fields are silently dropped by the
// JSON decoder.
type Post struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
	CreateAt  int64  `json:"create_at"`
	Username  string `json:"-"` // populated by Decode from data.sender_name
}

// ErrNotPosted means the event envelope is well-formed JSON but the event
// type isn't `posted`. Caller treats this as "not interesting" — no error
// logged.
var ErrNotPosted = errors.New("mmbot/events: not a posted event")

// DecodePosted parses a `posted` WS message into the typed Post. Returns
// ErrNotPosted when the event is well-formed but a different type;
// returns a real error only on malformed JSON.
func DecodePosted(raw []byte) (Post, error) {
	var env Event
	if err := json.Unmarshal(raw, &env); err != nil {
		return Post{}, err
	}
	if env.Event != "posted" {
		return Post{}, ErrNotPosted
	}
	postRaw, ok := env.Data["post"]
	if !ok {
		return Post{}, errors.New("mmbot/events: posted event missing data.post")
	}
	// MM serialises the Post object as a JSON-encoded STRING inside
	// data.post (not a nested object). Unquote first, then decode.
	var postStr string
	if err := json.Unmarshal(postRaw, &postStr); err != nil {
		// Some MM versions deliver it as a raw object. Tolerate both.
		var p Post
		if err2 := json.Unmarshal(postRaw, &p); err2 == nil {
			fillUsername(&p, env.Data)
			return p, nil
		}
		return Post{}, errors.New("mmbot/events: data.post neither string nor object")
	}
	var p Post
	if err := json.Unmarshal([]byte(postStr), &p); err != nil {
		return Post{}, err
	}
	fillUsername(&p, env.Data)
	return p, nil
}

// IsTopLevel reports whether the post starts a new thread (no root_id).
func (p Post) IsTopLevel() bool { return strings.TrimSpace(p.RootID) == "" }

// RootOrSelf returns the MM thread root id for this post — its own id when
// it's a top-level post, otherwise RootID. Used to index the mm_threads
// mapping table.
func (p Post) RootOrSelf() string {
	if p.IsTopLevel() {
		return p.ID
	}
	return p.RootID
}

func fillUsername(p *Post, data map[string]json.RawMessage) {
	if raw, ok := data["sender_name"]; ok {
		var name string
		if err := json.Unmarshal(raw, &name); err == nil {
			p.Username = strings.TrimPrefix(name, "@")
		}
	}
}
