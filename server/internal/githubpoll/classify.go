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
)

// ErrSkip is returned by Classify when the event is structurally
// valid but is not a cascade trigger (non-merge close, irrelevant
// event type, etc.). Callers count it as "saw and ignored" —
// distinct from a parse failure. Same role as
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
// PUL-212: cascade now emits only pr_merged from the poll path. CI
// failure (workflow_run / check_run) and review-changes-requested
// (pull_request_review) events used to wake the agent in the
// multica issue, which double-Claude'd with PUL-209's
// pr-test-autofix.yml + code-review-fix.yml inside the PR. Those
// classifiers were removed; the switch arms remain (returning
// ErrSkip) so a future re-enablement is a literal one-line revert.
// The Resolver field used by the removed classifiers' commit→PRs
// fallback was removed too — PullRequestEvent has the PR number
// inline and does not need resolver assistance.
type Classifier struct {
	Logger *slog.Logger
}

func (c Classifier) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// Classify maps a single REST /events item to a TriggerEvent.
func (c Classifier) Classify(ctx context.Context, repo string, e Event) (*webhooks.TriggerEvent, error) {
	_ = ctx // accepted for future use; the current arms do not need it
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
	case "PullRequestEvent":
		return c.classifyPullRequest(repo, eventID, e.Payload)
	case "WorkflowRunEvent", "CheckRunEvent", "PullRequestReviewEvent":
		// PUL-212: CI failure and review-changes-requested events used
		// to wake the agent in the multica issue, which double-Claude'd
		// with PUL-209's pr-test-autofix.yml + code-review-fix.yml in
		// rabbeet/Pulse. Autofix-pipeline now owns CI / review-comment
		// fixes inside the PR; cascade keeps the merged → deployed
		// transition (pr_merged via PullRequestEvent) and stays out of
		// failure-mode flows. The arms remain in the switch so a future
		// re-enablement is a literal one-line revert; the payload
		// classifiers (classifyWorkflowRun / classifyCheckRun /
		// classifyPullRequestReview) and their testdata fixtures were
		// deleted alongside this change.
		return nil, ErrSkip
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

// pullRequestPayload models the REST /events PullRequestEvent payload.
//
// Webhook-only fields (html_url, title, merged) are deliberately NOT
// declared — the poll classifier must reflect the shape it actually
// receives. PUL-183 dropped html_url at the validation layer but kept
// it in the struct; PUL-185 finishes the job and drops title + merged
// (along with the `changes` block stripped by /events on edited
// actions). The webhook adapter (server/internal/webhooks/github)
// keeps its own struct with the full webhook shape.
type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int32  `json:"number"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
}

func (c Classifier) classifyPullRequest(repo string, eventID uuid.UUID, raw json.RawMessage) (*webhooks.TriggerEvent, error) {
	var p pullRequestPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: pull_request: %v", ErrSchemaMismatch, err)
	}
	if p.Number == 0 {
		return nil, fmt.Errorf("%w: pull_request missing number", ErrSchemaMismatch)
	}
	// REST /events strips html_url from the pull_request object; the
	// API `url` is present but not used. Reconstruct from repo + number
	// the same way resolveOrInlinePRs used to for workflow_run /
	// check_run inline PRs. See PUL-183.
	prURL := p.PullRequest.HTMLURL
	if prURL == "" {
		prURL = htmlURLForPR(repo, int(p.Number))
	}
	common := webhooks.TriggerEvent{
		EventID:  eventID,
		PRURL:    prURL,
		PRNumber: int(p.Number),
		// PRTitle deliberately omitted: REST /events strips
		// pull_request.title. cascade.Worker.resolveIssue
		// (server/internal/cascade/lookup.go) falls back to the
		// agent-<N>/<prefix>-<n>-<slug> branch regex when title is
		// empty, so agent-driven PRs (the only ones the cascade
		// scope filter accepts via InScope) still resolve correctly.
		// Human PRs with `[PUL-N]` in title but a non-agent branch
		// would scope-skip on the poll path; webhook channel remains
		// canonical for that narrow edge case. See PUL-185.
		HeadSHA: p.PullRequest.Head.SHA,
		Branch:  p.PullRequest.Head.Ref,
	}
	switch p.Action {
	case "merged":
		// REST /events synthesizes a "merged" action for completed
		// PR merges (the webhook channel emits action="closed" with
		// pull_request.merged=true). PUL-185.
		common.EventType = webhooks.EventTypePRMerged
		return &common, nil
	default:
		// Includes "opened", "closed" (unmerged), "reopened",
		// "labeled", "edited", etc. The "edited" arm was previously
		// wired to EventTypePRTitleEdit via changes.title.from, but
		// REST /events strips the `changes` field, so that branch was
		// poll-blind by construction. Webhook channel was the
		// canonical source for pr_title_edit but the inbound adapter
		// was removed in PUL-166 PR5, so EventTypePRTitleEdit has no
		// live source and is marked Deprecated in webhooks/event.go.
		// PUL-185 + PUL-212.
		return nil, ErrSkip
	}
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
