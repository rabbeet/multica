# githubpoll testdata

Test fixtures for the GitHub `/events` polling path.

## Two fixture shapes

There are two intentionally-different fixture shapes here, do not mix them:

### Per-event fixtures (`pull_request_*.json`, `push.json`, `workflow_run_success.json`)

One JSON object = one event payload, as the inbound webhook adapter sees it.
These cover `classify.go` unit tests that focus on a single event-shape boundary.

PUL-212 removed the `workflow_run_failure.json`, `workflow_run_failure_no_prs.json`,
`check_run_failure.json`, and `pull_request_review_changes_requested.json` fixtures
when the classifier branches for those event types were dropped (autofix-pipeline
in rabbeet/Pulse owns CI / review-comment fixes now). The remaining
`workflow_run_success.json` and `pull_request_review_approved.json` files stay
as regression guards — they assert "still ErrSkip after handlers removed"
across future refactors.

### Full-page captures (`first_run_events_capture.json`)

One JSON array = one full `/repos/{owner}/{repo}/events?per_page=100` response
from the REST API. Used as the literal input to tests that exercise the
poller's tick-level behaviour (PUL-186 first-run cursor seeding). The
fixture is the redacted output of `gh api`, not a hand-rolled approximation.

**Do not** write new full-page fixtures by hand. The PUL-185 incident root-
caused to exactly that pattern: a PR claimed "byte-equivalent to real
`/events`" but shipped a hand-rolled approximation that drifted from real
GitHub payloads. The byte-equivalence rule is enforced structurally now —
tests read the on-disk capture verbatim, and re-capture overwrites the file.

## Re-capturing `first_run_events_capture.json`

The capture has a 90-day GitHub-side window, so the file naturally goes
stale. Re-capture quarterly, or after any GitHub API version bump.

```
cd server
go test -tags=fixturecheck -run=Recapture -recapture ./internal/githubpoll
git diff internal/githubpoll/testdata/first_run_events_capture.json
```

`-tags=fixturecheck` keeps the harness out of the default `go test ./...`
run, since it needs network access and a `GITHUB_TOKEN` (or `gh auth token`
in scope).

A non-empty diff signals GitHub `/events` shape drift since the last capture.
Investigate before committing: the structural shift may invalidate
assumptions in `classify.go` (see [PUL-183](https://multica.ai/issues/PUL-183),
[PUL-185](https://multica.ai/issues/PUL-185)).

If the diff updates the max event id, also update
`firstRunFixtureMaxID` in `poller_test.go` — it is asserted directly by the
first-run tests. The constant has a comment with the `jq` recipe to recompute
it.

## Redaction

`fixture_capture_test.go`'s redaction function clears PII fields on `actor`
and `org` blocks (`login`, `gravatar_id`, `avatar_url`, `display_login`,
per-user `url`) to placeholder strings. Everything else passes through.
Identifiers (`id`, `type`, `created_at`, `repo`, `payload.*`) stay intact
since those are what the classifier reads.

Re-capturing applies the same redaction, so the only fields that change
between captures are the events themselves (and one timestamp on a `created_at`
field somewhere if you stare at the diff long enough).

## Provenance

`first_run_events_capture.json` was first captured 2026-05-19 by `agent-2`
during PUL-186 implementation. Source repository: `rabbeet/multica` `main`.
