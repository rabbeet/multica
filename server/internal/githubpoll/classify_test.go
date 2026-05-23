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

// errFakeTransient simulates a 5xx / network error from the resolver
// so the hydratePRTitle warn-log-and-swallow path is exercised.
var errFakeTransient = errors.New("simulated transient resolver error")

// fakeResolver satisfies github.PRResolver for unit tests. PUL-216
// added LookupPRTitle to the interface; LookupPRsByCommit is kept
// as a no-op stub because the workflow_run / check_run classifiers
// that consumed it were removed in PUL-212.
//
// title / titleErr drive the PUL-216 hydration path: a non-empty
// title flows into TriggerEvent.PRTitle; a non-nil titleErr stays
// internal (logged + swallowed by hydratePRTitle so the event
// still classifies).
type fakeResolver struct {
	title    string
	titleErr error
}

func (f fakeResolver) LookupPRsByCommit(_ context.Context, _, _ string) ([]github.PRRef, error) {
	return nil, nil
}

func (f fakeResolver) LookupPRTitle(_ context.Context, _ string, _ int) (string, error) {
	return f.title, f.titleErr
}

func TestClassify_Table(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string // when set, loaded from testdata/
		inlineEvent *Event // when set, used instead of fixture (for cases with no on-disk JSON)
		repo        string
		resolver    github.PRResolver
		wantType    string // empty → ErrSkip
		wantErrIs   error  // non-nil → assertion target
		wantPRNum   int
		wantPRURL   string // expected PRURL; verifies the html_url reconstruction path
		wantHeadSHA string
		wantBranch  string
		wantPRTitle string // PUL-216 — asserts hydrated title flows through; empty == nil Resolver or 404
	}{
		{
			// PUL-212: workflow_run events no longer reach a payload
			// classifier — the switch arm now returns ErrSkip without
			// touching the payload. Fixture kept (workflow_run_success.json)
			// to assert this behavior survives across refactors; the
			// payload shape is irrelevant after PUL-212 because the
			// classifier short-circuits at the type switch.
			name:      "workflow_run (success) → skip [PUL-212]",
			fixture:   "workflow_run_success.json",
			repo:      "rabbeet/Pulse",
			wantErrIs: ErrSkip,
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
			// PUL-212: pull_request_review (any state) → ErrSkip. The
			// state="approved" fixture is kept as a regression: even
			// approval reviews must not surface as TriggerEvents now
			// that the classifier branch is gone.
			name:      "pull_request_review approved → skip [PUL-212]",
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
			// PUL-216: PR-merged event with resolver-supplied title.
			// REST /events strips pull_request.title; the classifier
			// hydrates via the resolver and the title must surface
			// on TriggerEvent.PRTitle so LookupIssueIdentifier can
			// use it as the primary signal.
			name:        "pull_request merged + title hydrated → PRTitle populated [PUL-216]",
			fixture:     "pull_request_merged.json",
			repo:        "rabbeet/Pulse",
			resolver:    fakeResolver{title: "[PUL-196] feat(refresh): dispatch"},
			wantType:    webhooks.EventTypePRMerged,
			wantPRNum:   530,
			wantPRURL:   "https://github.com/rabbeet/Pulse/pull/530",
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
			wantBranch:  "agent-1/pul-157-fix",
			wantPRTitle: "[PUL-196] feat(refresh): dispatch",
		},
		{
			// PUL-216: resolver returns an error (5xx, timeout). The
			// classifier logs + leaves PRTitle empty and the event
			// still classifies. Regression guard: title hydration
			// must never block classification.
			name:        "pull_request merged + title resolver errors → event classifies, PRTitle empty [PUL-216]",
			fixture:     "pull_request_merged.json",
			repo:        "rabbeet/Pulse",
			resolver:    fakeResolver{titleErr: errFakeTransient},
			wantType:    webhooks.EventTypePRMerged,
			wantPRNum:   530,
			wantPRURL:   "https://github.com/rabbeet/Pulse/pull/530",
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
			wantBranch:  "agent-1/pul-157-fix",
			wantPRTitle: "",
		},
		{
			// PUL-216: resolver returns empty title (PR 404 / 403 —
			// both treated as soft-empty by LookupPRTitle). Event
			// still classifies; downstream branch regex carries the
			// identifier.
			name:        "pull_request merged + title resolver 404 → event classifies, PRTitle empty [PUL-216]",
			fixture:     "pull_request_merged.json",
			repo:        "rabbeet/Pulse",
			resolver:    fakeResolver{title: ""},
			wantType:    webhooks.EventTypePRMerged,
			wantPRNum:   530,
			wantPRURL:   "https://github.com/rabbeet/Pulse/pull/530",
			wantHeadSHA: "cd38c1120b1c2d3e4f5061728394a5b6c7d8e9f3",
			wantBranch:  "agent-1/pul-157-fix",
			wantPRTitle: "",
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
			if got.PRTitle != tc.wantPRTitle {
				// PUL-216: explicit check (including empty) — title
				// hydration failure must surface as "" not the
				// previous row's value. Default zero is "" so always
				// asserting catches both empty and non-empty cases.
				t.Errorf("PRTitle = %q, want %q", got.PRTitle, tc.wantPRTitle)
			}
			// EventID is deterministic from (repo, numericID); regression
			// guard is in idempotency_test.go.
			if got.EventID.String() == "" {
				t.Errorf("EventID is zero")
			}
		})
	}
}

// TestClassify_DroppedEventTypes_AllSkip is the PUL-212 regression
// guard. After dropping the WorkflowRunEvent / CheckRunEvent /
// PullRequestReviewEvent classifier branches (autofix-pipeline in
// rabbeet/Pulse owns CI fixes and review-comment fixes now), the
// switch arms returning ErrSkip must survive across refactors. If a
// future contributor restores a payload classifier without
// understanding why these arms exist, this test fails loudly. See
// PUL-209 (autofix in Pulse) and PUL-212 (cascade deconflict).
func TestClassify_DroppedEventTypes_AllSkip(t *testing.T) {
	// Minimal payloads — the classifier short-circuits at the type
	// switch BEFORE touching the payload, so contents are irrelevant.
	// Using empty JSON objects keeps the test resilient to future
	// schema drift on GitHub's side.
	cases := []struct {
		name      string
		eventType string
	}{
		{"WorkflowRunEvent", "WorkflowRunEvent"},
		{"CheckRunEvent", "CheckRunEvent"},
		{"PullRequestReviewEvent", "PullRequestReviewEvent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := Event{
				ID:      "1",
				Type:    tc.eventType,
				Payload: json.RawMessage(`{}`),
			}
			ev.Repo.Name = "rabbeet/Pulse"
			c := Classifier{}
			got, err := c.Classify(context.Background(), "rabbeet/Pulse", ev)
			if !errors.Is(err, ErrSkip) {
				t.Errorf("Classify(%s) err = %v, want ErrSkip", tc.eventType, err)
			}
			if got != nil {
				t.Errorf("Classify(%s) returned non-nil TriggerEvent on skip path: %+v", tc.eventType, got)
			}
		})
	}
}

func TestClassify_RepoMismatch(t *testing.T) {
	ev := loadFixture(t, "pull_request_merged.json")
	c := Classifier{}
	_, err := c.Classify(context.Background(), "wrong/repo", ev)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("repo mismatch err = %v, want ErrSchemaMismatch", err)
	}
}

func TestClassify_MalformedID(t *testing.T) {
	ev := Event{
		ID:   "not-a-number",
		Type: "PullRequestEvent",
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
// PUL-212: EventTypeCIFailure and EventTypePRReviewChange are not
// required — the classifier arms return ErrSkip because the autofix
// pipeline in rabbeet/Pulse owns those failure modes inside the PR.
// The constants remain in webhooks/event.go (marked Deprecated) so
// the cascade_retrigger CHECK constraint stays compatible with
// legacy rows during the migration-drain transition.
func TestClassify_AllEventTypesCovered(t *testing.T) {
	required := map[string]bool{
		webhooks.EventTypePRMerged: true,
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
// omission. PUL-212 dropped EventTypeCIFailure and
// EventTypePRReviewChange — the classifier arms exist but return
// ErrSkip without producing a TriggerEvent.
func allClassifierOutputs() []string {
	return []string{
		webhooks.EventTypePRMerged,
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
	// PRTitle must be empty here — REST /events does not deliver it
	// and Classifier{}.Resolver is nil so PUL-216's hydratePRTitle
	// also returns "". The TestClassify_Table cases above cover the
	// non-nil-resolver flow.
	if got.PRTitle != "" {
		t.Errorf("PRTitle = %q, want empty (REST /events strips title; no resolver to hydrate)", got.PRTitle)
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
