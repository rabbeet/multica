# Cascade rollout runbook (PUL-102 PR8)

End-to-end deploy procedure for the event-driven multi-PR cascade. Assumes PR1-PR7 + PR8 are merged. Sections build on each other — do them in order.

> **PUL-212 update (2026-05-20):** the cascade now consumes only `pr_merged` events from the poll path. The `ci_failure` / `pr_review_change` event types are still in the `cascade_retrigger.event_type` CHECK constraint for legacy-row compatibility, but the classifier no longer emits them (autofix-pipeline in `rabbeet/Pulse` — PUL-209 `pr-test-autofix.yml` + `code-review-fix.yml` — owns CI / review-comment fixes inside the PR). The worker has a defensive short-circuit (`worker.go::processOne` early-exit) for legacy rows; migration `087_drain_legacy_failure_events` marks any pending rows `scope_filter_skip` on startup. Phases 1 / 2 below were originally written against the inbound-webhook flow (`MULTICA_CASCADE_WEBHOOK_ENABLED`); the steady-state production flow is now poll-based (`MULTICA_GITHUB_POLL_ENABLED=true`, `MULTICA_GITHUB_POLL_REPOS=<csv>`) per PUL-166. Treat the inbound-webhook references as historical context; the poll-side wiring is documented in `cmd/server/githubpoll_scheduler.go`.

## Pre-flight checklist

- [ ] All migrations through `074` applied: `make migrate-up`.
- [ ] `make sqlc` is current (no diffs in `server/pkg/db/generated/`).
- [ ] `make test` is green on the cascade packages (`cd server && go test ./internal/cascade/... ./internal/webhooks/...`).
- [ ] At least one workspace has an active agent assignee for the cascade-owning issues.
- [ ] GitHub App created and installed per `docs/cascade-github-webhook-setup.md` (org-wide, four events subscribed, secret stored in 1Password).

## Phase 1 — Staging dry-run

1. Deploy to staging.
2. Set env vars on the staging deployment:
   - `MULTICA_CASCADE_WEBHOOK_ENABLED=true`
   - `MULTICA_GITHUB_WEBHOOK_SECRET_CURRENT=<staging-only secret>`
   - (Optional) `MULTICA_CASCADE_SLACK_WEBHOOK_URL`, `MULTICA_CASCADE_TELEGRAM_BOT_TOKEN`, `MULTICA_CASCADE_TELEGRAM_CHAT_ID` if you want push notifications during staging.
3. Verify the GitHub App's webhook delivery URL points at the staging hostname (NOT prod).
4. Pick one test issue assigned to an agent. POST `/api/issues/{id}/cascade/approve` (or invoke `/plan-and-implement`). Confirm:
   - `issue.cascade_state = 'approved'`
   - `issue.cascade_started_at` set to now
   - `cascade_last_event_at` set to now
5. Have the agent create a test PR with branch `agent-N/TEST-1-…` or title `[TEST-1] …` on a small repo the App is installed on.
6. Watch the staging logs for `webhooks.persisted` events as GitHub delivers `pull_request.opened` (skipped — 204), then `workflow_run.completed conclusion=success` (skipped — 204), then a deliberately-broken commit triggering `workflow_run.completed conclusion=failure` (200 + INSERT).
7. Confirm a `cascade_retrigger` row landed.
8. Wait one `PollInterval` (2s). Worker should pick it up, log `cascade.worker.spawned`, and you should see a new agent task in `agent_task_queue` for the issue.

## Phase 2 — Smoke tests

Run each happy-path scenario from the plan's Test plan section:

- **Happy path** — 3-PR test plan executed end-to-end with no manual intervention between PRs.
- **Failure path** — CI fail → agent fixes → CI green; verify the worker spawned the fix-agent run.
- **Loop guard** — Push three different head_sha that all fail CI within 6h; the fourth must trip `cascade_state='loop_guarded'` and not spawn.
- **G3 amend mid-cascade** — `/amend-plan` between PR2 and PR3; verify cascade pauses on the next wake-up (this assertion requires the G3 follow-up to land; until then, document as gap).
- **G2 concurrency** — Send a webhook while a run is active; verify `cascade_pending_event` accumulates and the drain hook fires after the run terminates.

Each scenario should produce structured log lines under the `cascade.*` namespace; spot-check with `journalctl` / `loki` for typos / missing fields.

## Phase 3 — Production cut-over

Only after Phase 1 + 2 succeed for at least one week.

1. Generate the production webhook secret (≥32 random bytes, base64). Store in 1Password.
2. Update the GitHub App's webhook secret to the new value.
3. Roll the production server with:
   - `MULTICA_CASCADE_WEBHOOK_ENABLED=true`
   - `MULTICA_GITHUB_WEBHOOK_SECRET_CURRENT=<new secret>`
4. Watch logs for `webhooks.signature_failed`: should be zero in steady state. If present at high rate (>5/min), the secret is wrong — roll back.
5. Watch the `cascade_runs_spawned_total` counter (logs `cascade.worker.spawned`) ramp up as real agent PRs land. Compare against baseline (zero before flip).

## Phase 4 — Monitoring

Add the following metrics scrapes and alerts (no Prometheus dep in repo yet, but the log-named events line up with these names so they map cleanly to your existing observability):

```
# Volume
log_total{event="webhooks.persisted"}                — webhook events accepted
log_total{event="cascade.worker.spawned"}            — agent runs spawned

# Errors
log_total{event="webhooks.signature_failed"}         — alert > 5/min (attack signal)
log_total{event="webhooks.schema_mismatch"}          — alert > 0 (GitHub schema bumped)
log_total{event="webhooks.persist_failed"}           — alert > 0 (DB down)
log_total{event="cascade.worker.spawn_failed"}       — alert > 0 (TaskService unreachable)
log_total{event="cascade.worker.loop_guard_tripped"} — alert > 1/h

# Reconciliation
log_total{event="cascade.reconciler.nudged_stuck"}   — daily count, alert if > 5

# Notifications
log_total{event="cascade.notify.fallback_posted"}    — push channels exhausted, alert > 0.5/h
```

Dashboard endpoint p99 SLO: 2s. Webhook handler p99 SLO: 1s. Both ship with the PR — watch a typical week of traffic and confirm.

## Rollback

If something breaks badly in production:

1. Set `MULTICA_CASCADE_WEBHOOK_ENABLED=false` on the deployment.
2. Roll the server.
3. The `/webhooks/{source}` route is now 404 — vendors retry their deliveries for 8h, then give up.
4. In-flight cascades stop progressing but do not break: `issue.cascade_state='approved'` is a marker, not an active subscription. Once the flag flips back on, the worker resumes from wherever each cascade was.
5. If the issue is data-corruption-shaped, also set `cascade_state` to NULL on affected issues to force them back to per-PR approval — `UPDATE issue SET cascade_state = NULL WHERE cascade_state IN ('approved', 'paused')`.

## Daily reconciliation cron

The reconciler runs via `cascade.NewReconciler(pool, notify, logger).Run(ctx)` — a goroutine launched from `cmd/server/main.go` alongside the cascade worker. Wakes once at startup, then every 24h.

Operations: deploy as a regular pod / instance with the `MULTICA_CASCADE_WEBHOOK_ENABLED` flag on; the cron starts itself. To run as a one-shot (e.g. k8s CronJob), call `RunOnce(ctx)` directly via a thin wrapper binary — same code path.

## Secret rotation (90-day cadence)

See `docs/cascade-github-webhook-setup.md` § "Secret rotation procedure". Both `_CURRENT` and `_PREVIOUS` env vars are accepted during a 24h overlap window; drop `_PREVIOUS` after the window.

## What is NOT in scope of PR8

Documented for transparency — these are the follow-ups that close out the full cascade vision:

- **G3 plan-amend mid-cascade check** in the worker. Requires the worker to clone plans-multica + parse frontmatter; planned for the next follow-up since PR5 already documents the agent-side flow.
- **G5 GitHub state validation** — cross-check PR state against the webhook payload (handles ci-fail+merge races). Needs a Redis cache on `head_sha` (P4) to stay inside the rate budget.
- **Per-task plan-repo clone** — daemon side. PR5's template assumes `$PLAN_REPO_CLONE` is set by the daemon at spawn; that wiring belongs in PR5's data-flow follow-up.
- **Frontend `/cascades` dashboard page** in `apps/web` — PR7 shipped the backend; the Next.js page is its own PR.
- **Load test artifact** `bench/cascade-webhook-load.k6.js` — a k6 script that exercises 100 events/min sustained for 5min. Trivial to write once the staging endpoint is live; defer until after Phase 1.
- **`multica issue cascade complete` CLI subcommand** — the rendered template's hard-stop step uses admin SQL meanwhile.
