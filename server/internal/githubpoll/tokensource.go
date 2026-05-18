package githubpoll

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrNoToken is returned by TokenSource implementations when no token
// is currently available — e.g. the env var is empty, or the App
// installation private key failed to mint a token. Callers treat this
// as a transient error and retry on the next tick.
var ErrNoToken = errors.New("githubpoll: no token available")

// TokenSource abstracts the GitHub auth credential. PR2 ships
// EnvPATSource which reads a fine-grained PAT from an env var; PUL-141
// will replace the implementation with an App-installation source
// that mints short-lived tokens from a private key. The poller does
// not change.
//
// Token returns the credential to put in the Authorization header and
// its expiry. Expiry is informational — the poller calls Token on
// every tick anyway. A zero expiry means "never expires" (PATs) and
// is fine. The returned token MUST NOT include the "Bearer " prefix.
type TokenSource interface {
	Token(ctx context.Context) (token string, expiresAt time.Time, err error)
}

// EnvPATSource reads a PAT from an environment variable on every
// call. Trivially cheap. Concurrency-safe.
//
// The lookup is per-call so an operator rotating the secret in the
// process env (via `kill -USR2` style reload, or a sidecar that
// rewrites the env file before restart) is picked up on the next
// tick — no daemon recycle needed for token rotation. PR3 keeps this
// behavior; PUL-141 replaces it with the App source.
type EnvPATSource struct {
	// EnvVar is the name of the env var holding the PAT. Empty
	// defaults to "MULTICA_GITHUB_API_TOKEN".
	EnvVar string

	// Getenv is overridable for tests. nil falls back to os.Getenv.
	Getenv func(string) string
}

// Token implements TokenSource.
func (s EnvPATSource) Token(_ context.Context) (string, time.Time, error) {
	name := s.EnvVar
	if name == "" {
		name = "MULTICA_GITHUB_API_TOKEN"
	}
	get := s.Getenv
	if get == nil {
		get = osGetenv
	}
	token := strings.TrimSpace(get(name))
	if token == "" {
		return "", time.Time{}, ErrNoToken
	}
	return token, time.Time{}, nil
}

// osGetenv is a package-level indirection so tests of EnvPATSource
// using its zero value (no explicit Getenv field set) don't depend on
// the process environment. Production callers leave Getenv nil and
// pick up os.Getenv via this var.
var osGetenv = stdGetenv

// staticTokenSource is the no-rotation source used by tests. Exposed
// via NewStaticTokenSource so test files in other packages can use it
// without importing internals.
type staticTokenSource struct {
	value string
}

// NewStaticTokenSource returns a TokenSource that always returns the
// given value. Used by tests and by the resolver when a static PAT
// has already been resolved at construction time.
func NewStaticTokenSource(value string) TokenSource {
	return staticTokenSource{value: value}
}

// Token implements TokenSource.
func (s staticTokenSource) Token(_ context.Context) (string, time.Time, error) {
	if s.value == "" {
		return "", time.Time{}, ErrNoToken
	}
	return s.value, time.Time{}, nil
}
