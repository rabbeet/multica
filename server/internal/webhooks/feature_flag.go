package webhooks

import (
	"log/slog"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
)

// FeatureFlagEnvVar is the env var that gates the entire cascade
// webhook subsystem. When unset or set to a falsy value (empty,
// "0", "false", "off", "no") the router is not mounted at all and
// /webhooks/{source} returns 404. PR8 will flip this to "true" in
// staging first, then in prod after stability.
const FeatureFlagEnvVar = "MULTICA_CASCADE_WEBHOOK_ENABLED"

// envEnabled returns true when the feature flag env var is set to a
// truthy value. The parser is deliberately strict — only the listed
// truthy values count, so a typo like "ture" is treated as off.
// Centralised here so tests, the main router wiring, and any future
// admin endpoint that surfaces the flag all read it the same way.
func envEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(FeatureFlagEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// MountOptions carries the dependencies the webhooks subsystem needs
// when wired. Kept as a struct so callers can leave optional fields
// nil — e.g. tests skip the Store.
//
// PUL-166 PR5: dropped GitHubSource. The GitHub ingress moved to
// outbound polling (internal/githubpoll); the inbound adapter is
// gone. The generic router stays so future Linear / Slack / GitLab
// sources plug in here — until then, the registered stubs all
// return 204 on every payload.
type MountOptions struct {
	// Store, when non-nil, enables persistence — router calls
	// Insert after successful Normalize. Without a Store the router
	// runs in skeleton mode (202 only). Stays available for the
	// future non-GitHub source adapters.
	Store EventStore
}

// MountFromEnv reads the feature flag and, when enabled, constructs a
// Router with the supplied options and mounts it on the parent Chi
// router. Returns the *Router so the caller can log "webhooks
// subsystem active with N sources" at startup. Returns nil when the
// flag is off — the route literally does not exist on the server.
//
// This is the single integration point cmd/server/router.go uses.
// PUL-166 PR5 removed GitHubSource wiring; only stubs remain
// registered for the placeholder vendors.
func MountFromEnv(parent chi.Router, opts MountOptions, logger *slog.Logger) *Router {
	if !envEnabled() {
		return nil
	}
	r := NewRouter(logger)
	if opts.Store != nil {
		r.WithStore(opts.Store)
	}
	r.Register(LinearStub())
	r.Register(SlackStub())
	r.Register(GitLabStub())
	r.Mount(parent)
	return r
}
