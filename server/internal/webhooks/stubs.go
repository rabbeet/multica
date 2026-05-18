package webhooks

import (
	"net/http"
)

// PR2 of PUL-102 shipped four stub adapters so the registry shape
// was fully visible and the feature-flag-on smoke test had something
// to exercise.
//
// LinearStub / SlackStub / GitLabStub remain as placeholders for
// future generic-event-router source adapters (E1 capability). They
// are registered but always return ErrUnsupportedEvent so callers
// see a clean 204 No Content rather than a 404 — operators wiring
// up a new vendor see the route is alive while the adapter is
// being built.
//
// PUL-166 PR5 removed the GitHubStub. The GitHub source moved out
// of the webhook router entirely; outbound polling
// (internal/githubpoll) is the new ingress and the placeholder is
// no longer wired or referenced.

// stubSource is the shared implementation for the three placeholder
// adapters. Each named adapter wraps it with its public Name() so
// router.go's logs and the registry key carry the right vendor
// label.
type stubSource struct {
	name string
}

func (s stubSource) Name() string                          { return s.name }
func (s stubSource) SignatureHeader() string               { return "" } // opt out of HMAC
func (s stubSource) Secrets() (current, previous string)   { return "", "" }
func (s stubSource) Normalize(*http.Request) (*TriggerEvent, error) {
	return nil, ErrUnsupportedEvent
}

// LinearStub, SlackStub, GitLabStub: future E1 source adapters. Each
// is a TODO until a real integration is planned in its own ticket.
// Registering them now means the route shape, log surface, and
// metric labels are stable from day one — operators see
// /webhooks/linear etc. resolve to 204 No Content instead of 404,
// which is the cleanest signal of "registered, not implemented".
func LinearStub() Source { return stubSource{name: "linear"} }
func SlackStub() Source  { return stubSource{name: "slack"} }
func GitLabStub() Source { return stubSource{name: "gitlab"} }
