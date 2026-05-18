package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/webhooks"
	"github.com/multica-ai/multica/server/internal/webhooks/github"
)

// ErrSkip is returned by Classify when the event is structurally
// valid but is not a cascade trigger (success conclusion, non-merge
// close, approval review, etc.). Callers count it as "saw and
// ignored" — distinct from a parse failure. Same role as
// webhooks.ErrUnsupportedEvent in the inbound path; using a separate
// sentinel here so the poller can record it in its own metric label
// without depending on the webhooks package's error vocabulary
// outside of TriggerEvent.
var ErrSkip = errors.New("githubpoll: event not a cascade trigger")

// ErrSchemaMismatch is returned when the event's payload does not
// match the pinned shape. Surfaces as a metric (alert on rate > 0
// in steady state) and a structured log so a schema drift on
// GitHub's side becomes loud rather than silent.
var ErrSchemaMismatch = errors.New("githubpoll: schema mismatch")

// Event is the input shape Classify takes — a single item from the
// /repos/{owner}/{repo}/events response. Only fields the classifier
// reads are declared; everything else is dropped during JSON
// unmarshalling.
//
// id is a string in GitHub's JSON ("12345"), so we model it as
// string and parse during classification — keeping the int64 hot
// path inside this package and out of the cursor / sink layers.
type Event struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
	Repo    struct {
		Name string `json:"name"` // "owner/repo"
	} `json:"repo"`
}

// NumericID parses the GitHub id string into int64. Errors map to
// ErrSchemaMismatch — a non-numeric id is a contract break we want
// loud, not silent.
func (e Event) NumericID() (int64, error) {
	id, err := strconv.ParseInt(e.ID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: id %q is not a number: %v", ErrSchemaMismatch, e.ID, err)
	}
	return id, nil
}

// Classifier turns an Event into a webhooks.TriggerEvent or returns
// ErrSkip / ErrSchemaMismatch. The repo parameter is the canonical
// "owner/name" the poller is iterating — it must match the Event's
// inner repo.name; mismatch is treated as schema_mismatch (defensive
// against a bug in our pagination logic).
//
// Resolver, when non-nil, supplies the same commit→PRs fallback the
// webhook adapter uses. Without it, workflow_run / check_run events
// arriving with an empty pull_requests array are skipped (matching
// the webhook adapter's pre-resolver behavior). Tests inject a fake
// resolver; production wires github.HTTPResolver via PR2's main.go.
type Classifier struct {
	Resolver github.PRResolver
	Logger   *slog.Logger
}

func (c Classifier) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Classify maps a single REST /events item to a TriggerEvent.
func (c Classifier) Classify(ctx context.Context, repo string, e Event) (*webhooks.TriggerEvent, error) {
	if repo == "" {
		return nil, fmt.Errorf("%w: empty repo", ErrSchemaMismatch)
	}
	if !strings.EqualFold(e.Repo.Name, repo) {
		// Mismatched repo on a per-repo /events request is a programmer
		// error — log loudly so a future pagination refactor that
		// crosses repos surfaces immediately rather than silently
		// writing rows under the wrong PR.
		return nil, fmt.Errorf("%w: event repo %q ≠ poller repo %q",
			ErrSchemaMismatch, e.Repo.Name, repo)
	}
	numericID, err := e.NumericID()
	if err != nil {
		return nil, err
	}
	eventID := EventID(repo, numericID)

	switch e.Type {
	case "WorkflowRunEvent":
		return c.classifyWorkflowRun(ctx, repo, eventID, e.Payload)
	case "CheckRunEvent":
		return c.classifyCheckRun(ctx, repo, eventID, e.Payload)
	case "PullRequestEvent":
		return c.classifyPullRequest(eventID, e.Payload)
	case "PullRequestReviewEvent":
		return c.classifyPullRequestReview(eventID, e.Payload)
	default:
		// PushEvent, IssueCommentEvent, ForkEvent, etc. — not cascade
		// triggers. Skip silently.
		return nil, ErrSkip
	}
}

// --- payload structs ---
//
// Duplicated (intentionally) from server/internal/webhooks/github so
// the poll path does not depend on unexported types in that package.
// The shapes mirror what GitHub emits via both webhook delivery and
// the REST /events feed — they share the same payload schema. Only
// the fields the classifier reads are declared; unknown fields are
// dropped by encoding/json without error.

type workflowRunPayload struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		Conclusion   string `json:"conclusion"`
		HeadSHA      string `json:"head_sha"`
		HeadBranch   string `json:"head_branch"`
		PullRequests []struct {
			Number  int32  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
}

func (c Classifier) classifyWorkflowRun(ctx context.Context, repo string, eventID uuid.UUID, raw json.RawMessage) (*webhooks.TriggerEvent, error) {
	var p workflowRunPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: workflow_run: %v", ErrSchemaMismatch, err)
	}
	if p.Action != "completed" {
		return nil, ErrSkip
	}
	if p.WorkflowRun.Conclusion != "failure" {
		return nil, ErrSkip
	}
	if p.WorkflowRun.HeadSHA == "" {
		return nil, fmt.Errorf("%w: workflow_run missing head_sha", ErrSchemaMismatch)
	}
	prs, err := c.resolveOrInlinePRs(ctx, repo, p.WorkflowRun.HeadSHA, p.WorkflowRun.PullRequests)
	if err != nil {
		return nil, err
	}
	if len(prs) != 1 {
		return nil, ErrSkip
	}
	pr := prs[0]
	branch := p.WorkflowRun.HeadBranch
	if branch == "" {
		branch = pr.Ref
	}
	return &webhooks.TriggerEvent{
		EventID:   eventID,
		EventType: webhooks.EventTypeCIFailure,
		PRURL:     pr.HTMLURL,
		PRNumber:  pr.Number,
		PRTitle:   pr.Title,
		HeadSHA:   p.WorkflowRun.HeadSHA,
		Branch:    branch,
	}, nil
}

type checkRunPayload struct {
	Action   string `json:"action"`
	CheckRun struct {
		Conclusion   string `json:"conclusion"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number  int32  `json:"number"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

func (c Classifier) classifyCheckRun(ctx context.Context, repo string, eventID uuid.UUID, raw json.RawMessage) (*webhooks.TriggerEvent, error) {
	var p checkRunPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: check_run: %v", ErrSchemaMismatch, err)
	}
	if p.Action != "completed" {
		return nil, ErrSkip
	}
	if p.CheckRun.Conclusion != "failure" {
		return nil, ErrSkip
	}
	if p.CheckRun.HeadSHA == "" {
		return nil, fmt.Errorf("%w: check_run missing head_sha", ErrSchemaMismatch)
	}
	prs, err := c.resolveOrInlinePRs(ctx, repo, p.CheckRun.HeadSHA, p.CheckRun.PullRequests)
	if err != nil {
		return nil, err
	}
	if len(prs) != 1 {
		return nil, ErrSkip
	}
	pr := prs[0]
	return &webhooks.TriggerEvent{
		EventID:   eventID,
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

func (c Classifier) classifyPullRequest(eventID uuid.UUID, raw json.RawMessage) (*webhooks.TriggerEvent, error) {
	var p pullRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: pull_request: %v", ErrSchemaMismatch, err)
	}
	if p.PullRequest.HTMLURL == "" || p.Number == 0 {
		return nil, fmt.Errorf("%w: pull_request missing html_url or number", ErrSchemaMismatch)
	}
	common := webhooks.TriggerEvent{
		EventID:  eventID,
		PRURL:    p.PullRequest.HTMLURL,
		PRNumber: int(p.Number),
		PRTitle:  p.PullRequest.Title,
		HeadSHA:  p.PullRequest.Head.SHA,
		Branch:   p.PullRequest.Head.Ref,
	}
	switch p.Action {
	case "closed":
		if !p.PullRequest.Merged {
			return nil, ErrSkip
		}
		common.EventType = webhooks.EventTypePRMerged
		return &common, nil
	case "edited":
		if p.Changes.Title.From == "" {
			return nil, ErrSkip
		}
		common.EventType = webhooks.EventTypePRTitleEdit
		return &common, nil
	default:
		return nil, ErrSkip
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

func (c Classifier) classifyPullRequestReview(eventID uuid.UUID, raw json.RawMessage) (*webhooks.TriggerEvent, error) {
	var p pullRequestReviewPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: pull_request_review: %v", ErrSchemaMismatch, err)
	}
	if p.Action != "submitted" {
		return nil, ErrSkip
	}
	// Mirrors the webhook adapter: only changes_requested wakes the
	// agent. Approvals reduce friction; comments are signal-noise.
	if !strings.EqualFold(p.Review.State, "changes_requested") {
		return nil, ErrSkip
	}
	if p.PullRequest.HTMLURL == "" || p.PullRequest.Number == 0 {
		return nil, fmt.Errorf("%w: pull_request_review missing pull_request fields", ErrSchemaMismatch)
	}
	return &webhooks.TriggerEvent{
		EventID:   eventID,
		EventType: webhooks.EventTypePRReviewChange,
		PRURL:     p.PullRequest.HTMLURL,
		PRNumber:  int(p.PullRequest.Number),
		PRTitle:   p.PullRequest.Title,
		HeadSHA:   p.PullRequest.Head.SHA,
		Branch:    p.PullRequest.Head.Ref,
	}, nil
}

// resolveOrInlinePRs maps the truncated PR objects on
// workflow_run / check_run into github.PRRef, then falls back to the
// resolver when the inline list is empty. Returns the resolved set
// without filtering on count (caller decides whether 0 or >1 maps
// to ErrSkip — same convention as the webhook adapter).
func (c Classifier) resolveOrInlinePRs(
	ctx context.Context,
	repo string,
	headSHA string,
	inline []struct {
		Number  int32  `json:"number"`
		HTMLURL string `json:"html_url"`
	},
) ([]github.PRRef, error) {
	prs := make([]github.PRRef, 0, len(inline))
	for _, pr := range inline {
		if pr.Number <= 0 {
			continue
		}
		// REST /events truncates pull_requests the same way the webhook
		// payload does — the API URL is present but html_url is not.
		// Construct from repo.full_name + /pull/<number>, same as the
		// webhook adapter does (the URL pattern is GitHub-stable).
		prs = append(prs, github.PRRef{
			Number:  int(pr.Number),
			HTMLURL: htmlURLForPR(repo, int(pr.Number)),
		})
	}
	if len(prs) > 0 || c.Resolver == nil {
		return prs, nil
	}
	// Fallback: commits/{sha}/pulls. Returns empty when no PR contains
	// the commit (e.g. push on the default branch); same handling as
	// the webhook adapter — counted as 0 PRs upstream so the
	// classifier returns ErrSkip.
	resolved, err := c.Resolver.LookupPRsByCommit(ctx, repo, headSHA)
	if err != nil {
		c.logger().Warn("githubpoll.classify.resolver_failed",
			"repo", repo,
			"head_sha", headSHA,
			"error", err,
		)
		// Errors during resolver fallback are not classified as
		// SchemaMismatch — the schema is fine, the lookup failed.
		// Treat as skip so the poll proceeds, and the operator sees
		// the warning + retries on the next tick.
		return nil, ErrSkip
	}
	return resolved, nil
}

// htmlURLForPR builds the public PR URL from "owner/name" and the
// PR number. Mirrors webhooks/github.htmlURLForPR — kept inline here
// so the poll path has no cross-package dependency on an unexported
// helper. Returns "" on degenerate inputs so the caller's
// schema_mismatch sentinel fires loudly.
func htmlURLForPR(repoFullName string, number int) string {
	if repoFullName == "" || number <= 0 {
		return ""
	}
	return "https://github.com/" + repoFullName + "/pull/" + strconv.Itoa(number)
}
