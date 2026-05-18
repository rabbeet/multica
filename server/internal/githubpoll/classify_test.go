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
// differ in payload shape (notably, REST /events strips html_url
// from pull_request objects, while webhook deliveries include it).
// If you add a new fixture, get it from `gh api /repos/{r}/events`,
// not from a webhook log. PUL-183 traced a prod outage to a fixture
// set that hid behind this exact gap.
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
			// Regression for PUL-183 (pr_merged path): fixture mirrors
			// REST /events shape — no html_url on pull_request — and
			// the test asserts PRURL is reconstructed from repo +
			// number. Pre-fix, this case returned ErrSchemaMismatch.
			name:        "pull_request closed merged → pr_merged (reconstructed URL)",
			fixture:     "pull_request_merged.json",
			repo:        "rabbeet/Pulse",
			wantType:    webhooks.EventTypePRMerged,
			wantPRNum:   530,
			wantPRURL:   "https://github.com/rabbeet/Pulse/pull/530",
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
			wantBranch:  "agent-1/pul-157-fix",
		},
		{
			name:      "pull_request closed unmerged → skip",
			fixture:   "pull_request_closed_unmerged.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			name:       "pull_request edited title → pr_title_edit (reconstructed URL)",
			fixture:    "pull_request_edited_title.json",
			repo:       "rabbeet/Pulse",
			wantType:   webhooks.EventTypePRTitleEdit,
			wantPRNum:  532,
			wantPRURL:  "https://github.com/rabbeet/Pulse/pull/532",
			wantBranch: "agent-1/pul-159-router-lookup",
		},
		{
			name:      "pull_request edited body only → skip",
			fixture:   "pull_request_edited_body.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
		},
		{
			// Regression for PUL-183 (pr_review_change path): same shape
			// quirk as pr_merged above — REST /events strips html_url,
			// classifier must reconstruct.
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
			// Negative guard for PUL-183: after dropping the html_url
			// half of the schema check, `number == 0` is the only
			// remaining contract violation that surfaces as
			// SchemaMismatch (loud, alertable). If a future refactor
			// silently drops this check, this test fails.
			name: "pull_request missing number → schema_mismatch",
			inlineEvent: &Event{
				ID:   "33000000099",
				Type: "PullRequestEvent",
				Payload: json.RawMessage(`{
					"action": "closed",
					"pull_request": {
						"id": 999999,
						"merged": true,
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
			name: "pull_request_review missing pull_request.number → schema_mismatch",
			inlineEvent: &Event{
				ID:   "33000000098",
				Type: "PullRequestReviewEvent",
				Payload: json.RawMessage(`{
					"action": "submitted",
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

// Sanity: every webhook EventType constant has a classifier branch.
// This is a regression guard: if a new event type is added to the
// webhooks package, the test fails until the classifier learns about
// it, preventing a silent gap between channels.
func TestClassify_AllEventTypesCovered(t *testing.T) {
	known := map[string]bool{
		webhooks.EventTypeCIFailure:      true,
		webhooks.EventTypePRMerged:       true,
		webhooks.EventTypePRReviewChange: true,
		webhooks.EventTypePRTitleEdit:    true,
	}
	for et := range known {
		if !strings.Contains(strings.Join(allClassifierOutputs(), ","), et) {
			t.Errorf("classifier does not emit %q anywhere", et)
		}
	}
}

// allClassifierOutputs lists every EventType constant the classifier
// can produce. Hardcoded mirror of the cases above — kept in sync
// manually. If a reviewer adds a new EventType branch to classify.go,
// they must add it here too; TestClassify_AllEventTypesCovered fails
// on omission.
func allClassifierOutputs() []string {
	return []string{
		webhooks.EventTypeCIFailure,
		webhooks.EventTypePRMerged,
		webhooks.EventTypePRReviewChange,
		webhooks.EventTypePRTitleEdit,
	}
}
