// Package github implements the GitHub webhook source adapter for the
// cascade subsystem. Replaces the GitHubStub shipped in PR2.
//
// Subscribes to four event types — the union of events the PUL-102
// cascade reacts to:
//
//	workflow_run.completed  conclusion=failure → ci_failure
//	check_run.completed     conclusion=failure → ci_failure
//	pull_request.closed     merged=true        → pr_merged
//	pull_request.edited     title change only  → pr_title_edit (G4 fallback)
//	pull_request_review.submitted state=changes_requested → pr_review_change (E2)
//
// Success / pending / ignored variants of every event return
// ErrUnsupportedEvent → router responds 204.
//
// Schema version pin: the adapter rejects payloads that do not match
// the pinned shape (validated by required-field presence). The
// constraint "Schema mismatch — явный fail, не молчаливый парсинг"
// from the plan: a future GitHub schema change surfaces as a 400 +
// alert, not a silent miss.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/webhooks"
)

// SourceName is the registry key (and URL path segment). Exposed as a
// constant so the wiring in webhooks.MountFromEnv and tests reference
// the same string.
const SourceName = "github"

// SignatureHeaderName is the HTTP header GitHub uses for HMAC-SHA256
// signatures. The Verification scheme is implemented generically in
// webhooks.VerifyHMACSHA256.
const SignatureHeaderName = "X-Hub-Signature-256"

// eventTypeHeader is the GitHub event type. Always present on real
// deliveries; missing → schema mismatch.
const eventTypeHeader = "X-GitHub-Event"

// deliveryHeader is the GitHub delivery GUID. Always present; used as
// the seed for event_id (deterministic UUIDv5 → re-deliveries collide
// in cascade_retrigger).
const deliveryHeader = "X-GitHub-Delivery"

// deliveryNamespace is the UUIDv5 namespace for GitHub delivery IDs.
// Chosen once and pinned — never edit, or every existing
// cascade_retrigger.event_id becomes orphaned. The namespace is a
// random UUID I generated for this PR; tying it to a non-secret
// constant means dedup survives across multica restarts and
// horizontal scaling.
var deliveryNamespace = uuid.MustParse("a3b6f8e2-72c5-4b8b-9d1f-8d3b9c4f5a10")

// Config is the runtime config for the GitHub adapter. Loaded once
// at MountFromEnv time from env vars and held by the Source instance.
type Config struct {
	// SecretCurrent is the active HMAC secret. Required for HMAC
	// verification to succeed — register-time validation in
	// webhooks.Router panics if this is empty.
	SecretCurrent string

	// SecretPrevious is the rotated-out secret kept warm for up to
	// 24h so in-flight retries from GitHub still verify after a key
	// rotation. May be empty in steady state.
	SecretPrevious string

	// Resolver, when non-nil, supplies a fallback PR lookup for
	// workflow_run / check_run deliveries that arrive with an empty
	// pull_requests array. GitHub frequently omits the array even for
	// same-repo PR-attached runs (the data is only populated for a
	// narrow set of fork/security scenarios), so without this fallback
	// the adapter silently drops every CI-failure cascade event whose
	// run is a same-repo PR run. nil disables the fallback — the
	// adapter then keeps the pre-fallback behavior (skip on empty).
	Resolver PRResolver

	// Logger is used for fallback-path warnings (resolver errors,
	// 0 / >1 PR responses). nil falls back to slog.Default().
	Logger *slog.Logger
}

// PRRef is the minimal PR shape the fallback resolver needs to return.
// Mirrors the fields workflow_run / check_run normally carry inline
// when GitHub populates pull_requests.
type PRRef struct {
	Number  int
	HTMLURL string
	Title   string
	Ref     string // head ref / branch name
}

// PRResolver resolves the set of PRs that a commit SHA belongs to.
// Implemented in production by HTTPResolver (GitHub REST API); tests
// substitute a fake. Returning an empty slice means "no PRs match
// this SHA" (e.g. push on the default branch) — distinct from an
// error, which means "lookup failed, try again later".
type PRResolver interface {
	LookupPRsByCommit(ctx context.Context, repoFullName, headSHA string) ([]PRRef, error)
}

// Source implements webhooks.Source for GitHub.
type Source struct {
	cfg Config
}

// New returns a Source configured for the given secrets.
func New(cfg Config) *Source {
	return &Source{cfg: cfg}
}

// Name implements webhooks.Source.
func (*Source) Name() string { return SourceName }

// SignatureHeader implements webhooks.Source.
func (*Source) SignatureHeader() string { return SignatureHeaderName }

// Secrets implements webhooks.Source.
func (s *Source) Secrets() (string, string) {
	return s.cfg.SecretCurrent, s.cfg.SecretPrevious
}

func (s *Source) logger() *slog.Logger {
	if s.cfg.Logger != nil {
		return s.cfg.Logger
	}
	return slog.Default()
}

// Normalize parses the incoming GitHub webhook and produces a
// TriggerEvent. Validates schema by requiring the fields the cascade
// pipeline reads. Anything missing → ErrSchemaMismatch. Success /
// ignored variants → ErrUnsupportedEvent.
func (s *Source) Normalize(r *http.Request) (*webhooks.TriggerEvent, error) {
	eventType := r.Header.Get(eventTypeHeader)
	if eventType == "" {
		return nil, fmt.Errorf("%w: missing %s header", webhooks.ErrSchemaMismatch, eventTypeHeader)
	}
	deliveryID := r.Header.Get(deliveryHeader)
	if deliveryID == "" {
		return nil, fmt.Errorf("%w: missing %s header", webhooks.ErrSchemaMismatch, deliveryHeader)
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	switch eventType {
	case "workflow_run":
		return s.normalizeWorkflowRun(r.Context(), body, deliveryID)
	case "check_run":
		return s.normalizeCheckRun(r.Context(), body, deliveryID)
	case "pull_request":
		return s.normalizePullRequest(body, deliveryID)
	case "pull_request_review":
		return s.normalizePullRequestReview(body, deliveryID)
	case "ping":
		// GitHub sends a ping on App install / webhook re-config so
		// the operator can verify wiring. Always answer 204.
		return nil, webhooks.ErrUnsupportedEvent
	default:
		// Any event type we did not subscribe to → 204. GitHub
		// re-delivery policy treats 2xx as "do not retry", so this
		// is the right answer.
		return nil, webhooks.ErrUnsupportedEvent
	}
}

// EventID derives the deterministic event_id from a GitHub delivery
// GUID. Exported so PR4 worker tests and PR8 reconciliation can
// compute the same UUID without re-instantiating the Source.
func EventID(deliveryID string) uuid.UUID {
	return uuid.NewSHA1(deliveryNamespace, []byte(deliveryID))
}

// --- payload structs ---
//
// Each event type has a minimal struct holding only the fields the
// cascade pipeline reads. Unmarshalling the whole GitHub payload
// would be wasteful (each delivery is ~50KB) and brittle (GitHub
// adds fields routinely). Only required fields → ErrSchemaMismatch
// on missing.

type workflowRunPayload struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		Conclusion string `json:"conclusion"`
		HeadSHA    string `json:"head_sha"`
		HeadBranch string `json:"head_branch"`
		PullRequests []struct {
			Number  int32  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Source) normalizeWorkflowRun(ctx context.Context, body []byte, deliveryID string) (*webhooks.TriggerEvent, error) {
	var p workflowRunPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", webhooks.ErrSchemaMismatch, err)
	}
	if p.Action != "completed" {
		return nil, webhooks.ErrUnsupportedEvent
	}
	if p.WorkflowRun.Conclusion != "failure" {
		// Only failures drive the cascade — success / cancelled /
		// neutral / timed_out are all "do not wake the agent".
		return nil, webhooks.ErrUnsupportedEvent
	}
	if p.WorkflowRun.HeadSHA == "" || p.Repository.FullName == "" {
		return nil, fmt.Errorf("%w: workflow_run missing head_sha or repository.full_name", webhooks.ErrSchemaMismatch)
	}

	// GitHub frequently delivers workflow_run with pull_requests=[]
	// even when the run is attached to a same-repo PR (the array is
	// only populated in a narrow set of fork/security contexts). Fall
	// back to a commits/{sha}/pulls API lookup when a resolver is
	// configured; without it, preserve the original skip behavior so
	// dev / test deployments that omit the API token still build.
	prs := make([]PRRef, 0, len(p.WorkflowRun.PullRequests))
	for _, pr := range p.WorkflowRun.PullRequests {
		prs = append(prs, PRRef{Number: int(pr.Number), HTMLURL: pr.HTMLURL})
	}
	if len(prs) == 0 && s.cfg.Resolver != nil {
		resolved, err := s.cfg.Resolver.LookupPRsByCommit(ctx, p.Repository.FullName, p.WorkflowRun.HeadSHA)
		if err != nil {
			s.logger().Warn("webhooks.github.workflow_run.pr_lookup_failed",
				"repo", p.Repository.FullName,
				"head_sha", p.WorkflowRun.HeadSHA,
				"error", err,
			)
			return nil, webhooks.ErrUnsupportedEvent
		}
		prs = resolved
	}
	if len(prs) != 1 {
		return nil, webhooks.ErrUnsupportedEvent
	}
	pr := prs[0]
	if pr.HTMLURL == "" || pr.Number == 0 {
		return nil, fmt.Errorf("%w: workflow_run.pull_requests missing html_url or number", webhooks.ErrSchemaMismatch)
	}
	branch := p.WorkflowRun.HeadBranch
	if branch == "" {
		// API fallback returns the head ref; use it when the payload
		// omits head_branch (rare, but check_run shares this path).
		branch = pr.Ref
	}
	return &webhooks.TriggerEvent{
		EventID:   EventID(deliveryID),
		EventType: webhooks.EventTypeCIFailure,
		PRURL:     pr.HTMLURL,
		PRNumber:  pr.Number,
		PRTitle:   pr.Title, // empty for inline-payload PRs; populated by resolver
		HeadSHA:   p.WorkflowRun.HeadSHA,
		Branch:    branch,
	}, nil
}

type checkRunPayload struct {
	Action   string `json:"action"`
	CheckRun struct {
		Conclusion string `json:"conclusion"`
		HeadSHA    string `json:"head_sha"`
		HTMLURL    string `json:"html_url"`
		PullRequests []struct {
			Number  int32  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Source) normalizeCheckRun(ctx context.Context, body []byte, deliveryID string) (*webhooks.TriggerEvent, error) {
	var p checkRunPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", webhooks.ErrSchemaMismatch, err)
	}
	if p.Action != "completed" {
		return nil, webhooks.ErrUnsupportedEvent
	}
	if p.CheckRun.Conclusion != "failure" {
		return nil, webhooks.ErrUnsupportedEvent
	}
	if p.CheckRun.HeadSHA == "" || p.Repository.FullName == "" {
		return nil, fmt.Errorf("%w: check_run missing head_sha or repository.full_name", webhooks.ErrSchemaMismatch)
	}

	// Same payload quirk as workflow_run — check_run.pull_requests is
	// frequently empty for same-repo PR runs. Fall back to the API
	// lookup when configured.
	prs := make([]PRRef, 0, len(p.CheckRun.PullRequests))
	for _, pr := range p.CheckRun.PullRequests {
		prs = append(prs, PRRef{Number: int(pr.Number), HTMLURL: pr.HTMLURL})
	}
	if len(prs) == 0 && s.cfg.Resolver != nil {
		resolved, err := s.cfg.Resolver.LookupPRsByCommit(ctx, p.Repository.FullName, p.CheckRun.HeadSHA)
		if err != nil {
			s.logger().Warn("webhooks.github.check_run.pr_lookup_failed",
				"repo", p.Repository.FullName,
				"head_sha", p.CheckRun.HeadSHA,
				"error", err,
			)
			return nil, webhooks.ErrUnsupportedEvent
		}
		prs = resolved
	}
	if len(prs) != 1 {
		return nil, webhooks.ErrUnsupportedEvent
	}
	pr := prs[0]
	if pr.HTMLURL == "" || pr.Number == 0 {
		return nil, fmt.Errorf("%w: check_run.pull_requests missing html_url or number", webhooks.ErrSchemaMismatch)
	}
	return &webhooks.TriggerEvent{
		EventID:   EventID(deliveryID),
		EventType: webhooks.EventTypeCIFailure,
		PRURL:     pr.HTMLURL,
		PRNumber:  pr.Number,
		PRTitle:   pr.Title,
		HeadSHA:   p.CheckRun.HeadSHA,
		Branch:    pr.Ref,
	}, nil
}

type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int32  `json:"number"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Merged  bool   `json:"merged"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Changes struct {
		Title struct {
			From string `json:"from"`
		} `json:"title"`
	} `json:"changes"`
}

func (s *Source) normalizePullRequest(body []byte, deliveryID string) (*webhooks.TriggerEvent, error) {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", webhooks.ErrSchemaMismatch, err)
	}
	if p.PullRequest.HTMLURL == "" || p.Number == 0 {
		return nil, fmt.Errorf("%w: pull_request missing html_url or number", webhooks.ErrSchemaMismatch)
	}

	common := webhooks.TriggerEvent{
		EventID:  EventID(deliveryID),
		PRURL:    p.PullRequest.HTMLURL,
		PRNumber: int(p.Number),
		PRTitle:  p.PullRequest.Title,
		HeadSHA:  p.PullRequest.Head.SHA,
		Branch:   p.PullRequest.Head.Ref,
	}

	switch p.Action {
	case "closed":
		if !p.PullRequest.Merged {
			// Closed without merge — user cancelled, agent shouldn't
			// continue the cascade off a dead PR. Worker handles
			// cascade_state transition; the event itself is not
			// interesting to the router.
			return nil, webhooks.ErrUnsupportedEvent
		}
		evt := common
		evt.EventType = webhooks.EventTypePRMerged
		return &evt, nil

	case "edited":
		// Only title edits are interesting (G4 fallback safety net
		// for when the [PUL-N] prefix gets dropped). The `changes`
		// object holds the previous title; if not present, the edit
		// was on a non-title field and we skip.
		if p.Changes.Title.From == "" {
			return nil, webhooks.ErrUnsupportedEvent
		}
		evt := common
		evt.EventType = webhooks.EventTypePRTitleEdit
		return &evt, nil

	default:
		// pull_request.{opened,reopened,synchronize,...} are out of
		// scope — agents drive their own opens, and 'synchronize'
		// (new commits pushed) is already covered by the
		// workflow_run / check_run failure path.
		return nil, webhooks.ErrUnsupportedEvent
	}
}

type pullRequestReviewPayload struct {
	Action string `json:"action"`
	Review struct {
		State string `json:"state"`
	} `json:"review"`
	PullRequest struct {
		Number  int32  `json:"number"`
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
}

func (s *Source) normalizePullRequestReview(body []byte, deliveryID string) (*webhooks.TriggerEvent, error) {
	var p pullRequestReviewPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", webhooks.ErrSchemaMismatch, err)
	}
	if p.Action != "submitted" {
		return nil, webhooks.ErrUnsupportedEvent
	}
	// Only "changes_requested" gates the agent. Approvals don't wake
	// it — they reduce friction, not introduce it. Plain comments
	// (state="commented") also skip; if a comment matters, the
	// reviewer will request changes.
	if !strings.EqualFold(p.Review.State, "changes_requested") {
		return nil, webhooks.ErrUnsupportedEvent
	}
	if p.PullRequest.HTMLURL == "" || p.PullRequest.Number == 0 {
		return nil, fmt.Errorf("%w: pull_request_review missing pull_request fields", webhooks.ErrSchemaMismatch)
	}
	return &webhooks.TriggerEvent{
		EventID:   EventID(deliveryID),
		EventType: webhooks.EventTypePRReviewChange,
		PRURL:     p.PullRequest.HTMLURL,
		PRNumber:  int(p.PullRequest.Number),
		PRTitle:   p.PullRequest.Title,
		HeadSHA:   p.PullRequest.Head.SHA,
		Branch:    p.PullRequest.Head.Ref,
	}, nil
}

// FromEnv reads the GitHub secrets from env vars. Returns nil when
// the current secret is missing — caller falls back to the stub.
// Helper exists so webhooks.MountFromEnv can decide at registration
// time whether to wire the real adapter or leave the stub in place
// (e.g. dev box without GitHub App configured).
//
// When MULTICA_GITHUB_API_TOKEN is also set, the adapter installs a
// PRResolver that backfills empty workflow_run / check_run
// pull_requests via the commits/{sha}/pulls REST endpoint. Without
// the token the fallback is disabled and same-repo PR runs whose
// payload omits pull_requests are silently dropped (the pre-fix
// behavior).
func FromEnv(getenv func(string) string) *Source {
	current := strings.TrimSpace(getenv("MULTICA_GITHUB_WEBHOOK_SECRET_CURRENT"))
	if current == "" {
		return nil
	}
	previous := strings.TrimSpace(getenv("MULTICA_GITHUB_WEBHOOK_SECRET_PREVIOUS"))
	cfg := Config{SecretCurrent: current, SecretPrevious: previous}
	if token := strings.TrimSpace(getenv("MULTICA_GITHUB_API_TOKEN")); token != "" {
		cfg.Resolver = NewHTTPResolver(token)
	}
	return New(cfg)
}

// ensure interface satisfaction at compile time.
var _ webhooks.Source = (*Source)(nil)
