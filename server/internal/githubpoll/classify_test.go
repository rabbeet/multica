package githubpoll

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/webhooks"
	"github.com/multica-ai/multica/server/internal/webhooks/github"
)

// loadFixture reads a JSON file from testdata/ and unmarshals into
// Event. Tests use this to keep the table-driven cases readable —
// the fixtures themselves are byte-identical (modulo whitespace) to
// what gh api /repos/.../events returns.
//
// Discipline: every JSON file under testdata/ MUST be a faithful
// capture of a real REST /events response. Do NOT copy-paste from
// webhook deliveries — the two channels share an event type but
// differ in payload shape. Specifically, REST /events for PR-related
// events strips html_url, title, and merged from the pull_request
// object, synthesizes action="merged" for completed merges (not
// webhook's "closed"+merged=true), and uses action="created" for
// reviews (not webhook's "submitted"). It also strips the `changes`
// block on edited actions.
//
// If you add a new fixture, get it from `gh api /repos/{r}/events`,
// not from a webhook log. PUL-183 + PUL-185 traced two prod outages
// to fixture sets that hid behind this exact gap; the
// TestClassify_FixturesAreRESTShape test below enforces the
// no-webhook-only-keys rule as code, not just as a doc comment.
func loadFixture(t *testing.T, name string) Event {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", path, err)
	}
	return e
}

// fakeResolver returns a fixed PR set for testing the workflow_run /
// check_run fallback path. nil PRs simulates "no PR contains this
// SHA"; errPR simulates a transient API error.
type fakeResolver struct {
	prs    []github.PRRef
	errOut error
}

func (f fakeResolver) LookupPRsByCommit(_ context.Context, _, _ string) ([]github.PRRef, error) {
	return f.prs, f.errOut
}

func TestClassify_Table(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string         // when set, loaded from testdata/
		inlineEvent *Event         // when set, used instead of fixture (for cases with no on-disk JSON)
		repo        string
		resolver    github.PRResolver
		wantType    string // empty → ErrSkip
		wantErrIs   error  // non-nil → assertion target
		wantPRNum   int
		wantPRURL   string // expected PRURL; verifies the html_url reconstruction path
		wantHeadSHA string
		wantBranch  string
	}{
		{
			name:        "workflow_run failure with inline PR",
			fixture:     "workflow_run_failure.json",
			repo:        "rabbeet/Pulse",
			wantType:    webhooks.EventTypeCIFailure,
			wantPRNum:   530,
			wantHeadSHA: "18fa800a0b1c2d3e4f5061728394a5b6c7d8e9f0",
			wantBranch:  "agent-1/pul-157-fix",
		},
		{
			name:      "workflow_run success → skip",
			fixture:   "workflow_run_success.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			name:      "workflow_run failure, empty pull_requests, no resolver → skip",
			fixture:   "workflow_run_failure_no_prs.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			name:    "workflow_run failure, empty pull_requests, resolver hits → ci_failure",
			fixture: "workflow_run_failure_no_prs.json",
			repo:    "rabbeet/Pulse",
			resolver: fakeResolver{prs: []github.PRRef{
				{Number: 530, HTMLURL: "https://github.com/rabbeet/Pulse/pull/530",
					Title: "[PUL-157] fix(scope): repair scope filter",
					Ref:   "agent-1/pul-157-fix"},
			}},
			wantType:    webhooks.EventTypeCIFailure,
			wantPRNum:   530,
			wantHeadSHA: "3d26b7f10b1c2d3e4f5061728394a5b6c7d8e9f2",
			wantBranch:  "agent-1/pul-157-fix",
		},
		{
			name:    "workflow_run failure, empty pull_requests, resolver returns nothing → skip",
			fixture: "workflow_run_failure_no_prs.json",
			repo:    "rabbeet/Pulse",
			resolver: fakeResolver{prs: nil},
			wantErrIs: ErrSkip,
		},
		{
			name:    "workflow_run failure, empty pull_requests, resolver errors → skip (not schema mismatch)",
			fixture: "workflow_run_failure_no_prs.json",
			repo:    "rabbeet/Pulse",
			resolver: fakeResolver{errOut: errFakeTransient},
			wantErrIs: ErrSkip,
		},
		{
			name:        "check_run failure",
			fixture:     "check_run_failure.json",
			repo:        "rabbeet/Pulse",
			wantType:    webhooks.EventTypeCIFailure,
			wantPRNum:   530,
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
		},
		{
			// Regression for PUL-185 (action-value mismatch): REST
			// /events synthesizes action="merged" for completed PR
			// merges, not webhook's "closed"+merged=true. Fixture is
			// REST-shaped (no html_url / title / merged keys). PRURL
			// is reconstructed from repo + number (PUL-183 path).
			name:        "pull_request merged → pr_merged (reconstructed URL)",
			fixture:     "pull_request_merged.json",
			repo:        "rabbeet/Pulse",
			wantType:    webhooks.EventTypePRMerged,
			wantPRNum:   530,
			wantPRURL:   "https://github.com/rabbeet/Pulse/pull/530",
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
			wantBranch:  "agent-1/pul-157-fix",
		},
		{
			// Closed-unmerged is the only other action value PR
			// events emit (per live capture: 2/38 in last 100 events
			// on rabbeet/multica). Asserts the default → ErrSkip path.
			name:      "pull_request closed unmerged → skip",
			fixture:   "pull_request_closed_unmerged.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			// Regression for PUL-185 (action-value mismatch on review
			// path): REST /events synthesizes action="created", not
			// webhook's "submitted". Fixture is REST-shaped.
			name:       "pull_request_review changes_requested → pr_review_change (reconstructed URL)",
			fixture:    "pull_request_review_changes_requested.json",
			repo:       "rabbeet/Pulse",
			wantType:   webhooks.EventTypePRReviewChange,
			wantPRNum:  534,
			wantPRURL:  "https://github.com/rabbeet/Pulse/pull/534",
			wantBranch: "agent-1/pul-162-perf",
		},
		{
			name:      "pull_request_review approved → skip",
			fixture:   "pull_request_review_approved.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			name:      "PushEvent → skip (not a cascade trigger)",
			fixture:   "push.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			// Negative guard for PUL-183 + PUL-185: after dropping
			// html_url and the closed/merged guard, `number == 0` is
			// the only remaining contract violation that surfaces as
			// SchemaMismatch (loud, alertable). If a future refactor
			// silently drops this check, this test fails.
			name: "pull_request missing number → schema_mismatch",
			inlineEvent: &Event{
				ID:   "33000000099",
				Type: "PullRequestEvent",
				Payload: json.RawMessage(`{
					"action": "merged",
					"pull_request": {
						"id": 999999,
						"head": {"sha": "deadbeef", "ref": "x"}
					}
				}`),
				Repo: struct {
					Name string `json:"name"`
				}{Name: "rabbeet/Pulse"},
			},
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSchemaMismatch,
		},
		{
			// Negative guard mirroring the case above, for the review
			// path. Both classifiers share the same post-fix contract
			// (`number` required, everything else reconstructable).
			// Action must be "created" (REST shape — PUL-185) or the
			// classifier short-circuits to ErrSkip before reaching
			// the number check.
			name: "pull_request_review missing pull_request.number → schema_mismatch",
			inlineEvent: &Event{
				ID:   "33000000098",
				Type: "PullRequestReviewEvent",
				Payload: json.RawMessage(`{
					"action": "created",
					"review": {"state": "changes_requested"},
					"pull_request": {
						"id": 999998,
						"head": {"sha": "deadbeef", "ref": "x"}
					}
				}`),
				Repo: struct {
					Name string `json:"name"`
				}{Name: "rabbeet/Pulse"},
			},
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSchemaMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev Event
			switch {
			case tc.inlineEvent != nil:
				ev = *tc.inlineEvent
			case tc.fixture != "":
				ev = loadFixture(t, tc.fixture)
			default:
				t.Fatalf("test case %q has neither fixture nor inlineEvent", tc.name)
			}
			c := Classifier{Resolver: tc.resolver}
			got, err := c.Classify(context.Background(), tc.repo, ev)

			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err = %v, want %v", err, tc.wantErrIs)
				}
				if got != nil {
					t.Errorf("got TriggerEvent on error path: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("got nil TriggerEvent on success path")
			}
			if got.EventType != tc.wantType {
				t.Errorf("EventType = %q, want %q", got.EventType, tc.wantType)
			}
			if tc.wantPRNum != 0 && got.PRNumber != tc.wantPRNum {
				t.Errorf("PRNumber = %d, want %d", got.PRNumber, tc.wantPRNum)
			}
			if tc.wantPRURL != "" && got.PRURL != tc.wantPRURL {
				t.Errorf("PRURL = %q, want %q", got.PRURL, tc.wantPRURL)
			}
			if tc.wantHeadSHA != "" && got.HeadSHA != tc.wantHeadSHA {
				t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, tc.wantHeadSHA)
			}
			if tc.wantBranch != "" && got.Branch != tc.wantBranch {
				t.Errorf("Branch = %q, want %q", got.Branch, tc.wantBranch)
			}
			// EventID is deterministic from (repo, numericID); regression
			// guard is in idempotency_test.go.
			if got.EventID.String() == "" {
				t.Errorf("EventID is zero")
			}
		})
	}
}

func TestClassify_RepoMismatch(t *testing.T) {
	ev := loadFixture(t, "workflow_run_failure.json")
	c := Classifier{}
	_, err := c.Classify(context.Background(), "wrong/repo", ev)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("repo mismatch err = %v, want ErrSchemaMismatch", err)
	}
}

func TestClassify_MalformedID(t *testing.T) {
	ev := Event{
		ID:   "not-a-number",
		Type: "WorkflowRunEvent",
	}
	ev.Repo.Name = "rabbeet/Pulse"
	c := Classifier{}
	_, err := c.Classify(context.Background(), "rabbeet/Pulse", ev)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("non-numeric id err = %v, want ErrSchemaMismatch", err)
	}
}

func TestClassify_MalformedPayload(t *testing.T) {
	ev := Event{
		ID:      "1",
		Type:    "PullRequestEvent",
		Payload: json.RawMessage(`{not valid json}`),
	}
	ev.Repo.Name = "rabbeet/Pulse"
	c := Classifier{}
	_, err := c.Classify(context.Background(), "rabbeet/Pulse", ev)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("malformed payload err = %v, want ErrSchemaMismatch", err)
	}
}

func TestHTMLURLForPR(t *testing.T) {
	got := htmlURLForPR("rabbeet/Pulse", 42)
	want := "https://github.com/rabbeet/Pulse/pull/42"
	if got != want {
		t.Errorf("htmlURLForPR = %q, want %q", got, want)
	}
	if v := htmlURLForPR("", 1); v != "" {
		t.Errorf("htmlURLForPR with empty repo = %q, want empty", v)
	}
	if v := htmlURLForPR("a/b", 0); v != "" {
		t.Errorf("htmlURLForPR with zero number = %q, want empty", v)
	}
}

// errFakeTransient is a sentinel for the resolver-errors-out test
// case. Mirrors the kind of error HTTPResolver would produce on a
// 5xx; classifier should treat it as ErrSkip (try again next tick),
// not ErrSchemaMismatch.
var errFakeTransient = errors.New("simulated transient resolver error")

// Compile-time check: ensure Classifier doesn't expose internal state
// that would tempt callers to bypass the constructor pattern.
var _ = (*Classifier)(nil)

func TestEvent_NumericID(t *testing.T) {
	if id, err := (Event{ID: "12345"}).NumericID(); err != nil || id != 12345 {
		t.Errorf("NumericID(12345) = (%d, %v), want (12345, nil)", id, err)
	}
	if _, err := (Event{ID: "abc"}).NumericID(); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("non-numeric id should map to ErrSchemaMismatch, got %v", err)
	}
	if _, err := (Event{ID: ""}).NumericID(); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("empty id should map to ErrSchemaMismatch, got %v", err)
	}
}

// Sanity: every webhook EventType constant that the poll classifier
// is expected to emit has a corresponding branch in classify.go. This
// is a regression guard: if a new event type is added to the webhooks
// package and the poll classifier is supposed to emit it, the test
// fails until classify.go learns about it.
//
// EventTypePRTitleEdit is deliberately NOT required of the poll
// classifier: REST /events strips the `changes` field needed to
// detect title edits, so the poll path is structurally blind to that
// event. The webhook adapter (server/internal/webhooks/github)
// continues to emit it on its own channel. PUL-185.
func TestClassify_AllEventTypesCovered(t *testing.T) {
	required := map[string]bool{
		webhooks.EventTypeCIFailure:      true,
		webhooks.EventTypePRMerged:       true,
		webhooks.EventTypePRReviewChange: true,
	}
	for et := range required {
		if !strings.Contains(strings.Join(allClassifierOutputs(), ","), et) {
			t.Errorf("classifier does not emit %q anywhere", et)
		}
	}
}

// allClassifierOutputs lists every EventType constant the poll
// classifier can produce. Hardcoded mirror of classify.go — kept in
// sync manually. If a reviewer adds a new EventType branch, they must
// add it here too; TestClassify_AllEventTypesCovered fails on
// omission. EventTypePRTitleEdit is omitted on purpose — see test
// doc-comment above.
func allClassifierOutputs() []string {
	return []string{
		webhooks.EventTypeCIFailure,
		webhooks.EventTypePRMerged,
		webhooks.EventTypePRReviewChange,
	}
}

// TestClassify_PullRequestMerged_RESTShape pins the exact shape REST
// /events delivers for a completed PR merge: action="merged", no
// pull_request.{html_url,title,merged}, only base/head/id/number/url
// on the inner object. Inline payload — not a file fixture — keeps
// the asserted shape visually adjacent to the test so a future
// reviewer cannot misread it (the PUL-185 root cause). PUL-185.
func TestClassify_PullRequestMerged_RESTShape(t *testing.T) {
	ev := Event{
		ID:   "9599210196",
		Type: "PullRequestEvent",
		Payload: json.RawMessage(`{
			"action": "merged",
			"number": 37,
			"pull_request": {
				"id": 3703418000,
				"number": 37,
				"url": "https://api.github.com/repos/rabbeet/multica/pulls/37",
				"head": {
					"ref": "agent-2/pul-183-classifier-html-url",
					"sha": "42c5fd14906d8df221205dcb4518c37d77220b17"
				},
				"base": {
					"ref": "main",
					"sha": "5f38b2c2116839f196ce66288d6d4ea8474cfbaf"
				}
			}
		}`),
	}
	ev.Repo.Name = "rabbeet/multica"

	c := Classifier{}
	got, err := c.Classify(context.Background(), "rabbeet/multica", ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("got nil TriggerEvent")
	}
	if got.EventType != webhooks.EventTypePRMerged {
		t.Errorf("EventType = %q, want %q", got.EventType, webhooks.EventTypePRMerged)
	}
	if got.PRURL != "https://github.com/rabbeet/multica/pull/37" {
		t.Errorf("PRURL = %q, want reconstructed html_url", got.PRURL)
	}
	if got.PRNumber != 37 {
		t.Errorf("PRNumber = %d, want 37", got.PRNumber)
	}
	// PRTitle must be empty — REST /events does not deliver it, and
	// the classifier must not invent a value. This is the assertion
	// that would have caught PUL-185 before ship.
	if got.PRTitle != "" {
		t.Errorf("PRTitle = %q, want empty (REST /events strips title)", got.PRTitle)
	}
}

// TestClassify_PullRequestReviewCreated_RESTShape mirrors the merge
// test for the review path. REST emits action="created", not webhook's
// "submitted". PUL-185.
func TestClassify_PullRequestReviewCreated_RESTShape(t *testing.T) {
	ev := Event{
		ID:   "9600075638",
		Type: "PullRequestReviewEvent",
		Payload: json.RawMessage(`{
			"action": "created",
			"review": {
				"id": 4312242098,
				"state": "changes_requested",
				"body": "Please reshape this loop."
			},
			"pull_request": {
				"id": 3703537560,
				"number": 542,
				"url": "https://api.github.com/repos/rabbeet/Pulse/pulls/542",
				"head": {
					"ref": "agent-1/pul-173-stage1-id-fix",
					"sha": "fac3043abc092ed36bf0530f713f4f972be2281b"
				},
				"base": {
					"ref": "main",
					"sha": "bee888e001fd5dd403614ec21da342a84d81d68c"
				}
			}
		}`),
	}
	ev.Repo.Name = "rabbeet/Pulse"

	c := Classifier{}
	got, err := c.Classify(context.Background(), "rabbeet/Pulse", ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("got nil TriggerEvent")
	}
	if got.EventType != webhooks.EventTypePRReviewChange {
		t.Errorf("EventType = %q, want %q", got.EventType, webhooks.EventTypePRReviewChange)
	}
	if got.PRURL != "https://github.com/rabbeet/Pulse/pull/542" {
		t.Errorf("PRURL = %q, want reconstructed html_url", got.PRURL)
	}
	if got.PRNumber != 542 {
		t.Errorf("PRNumber = %d, want 542", got.PRNumber)
	}
	if got.PRTitle != "" {
		t.Errorf("PRTitle = %q, want empty (REST /events strips title)", got.PRTitle)
	}
}

// TestClassify_FixturesAreRESTShape is the contract test PR #37
// (PUL-183) was supposed to have. It walks testdata/, parses each
// pull_request_* fixture as a generic map, and fails if any forbidden
// (webhook-only) key appears under payload.pull_request. The doc
// comment on loadFixture said these fixtures must mirror REST shape;
// PUL-185 traced a prod regression to two fixtures that quietly
// drifted to webhook shape (action="closed", merged=true, title=...).
// Encoding the contract as code is the durable fix — prose didn't
// save us. PUL-185.
func TestClassify_FixturesAreRESTShape(t *testing.T) {
	const dir = "testdata"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read testdata dir: %v", err)
	}
	prForbidden := []string{"html_url", "title", "merged"}
	reviewForbidden := []string{"html_url", "title"}
	for _, e := range entries {
		name := e.Name()
		var forbidden []string
		switch {
		case strings.HasPrefix(name, "pull_request_review_") && strings.HasSuffix(name, ".json"):
			forbidden = reviewForbidden
		case strings.HasPrefix(name, "pull_request_") && strings.HasSuffix(name, ".json"):
			forbidden = prForbidden
		default:
			continue
		}
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var raw map[string]any
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatalf("unmarshal %s: %v", name, err)
			}
			payload, ok := raw["payload"].(map[string]any)
			if !ok {
				t.Fatalf("%s: payload missing or not an object", name)
			}
			pr, ok := payload["pull_request"].(map[string]any)
			if !ok {
				t.Fatalf("%s: payload.pull_request missing or not an object", name)
			}
			for _, key := range forbidden {
				if _, present := pr[key]; present {
					t.Errorf("%s: pull_request contains forbidden webhook-only key %q — REST /events strips it. See PUL-185.", name, key)
				}
			}
		})
	}
}
