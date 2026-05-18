-- PUL-166 PR1: foundation for github-events polling that replaces the
-- public webhook ingress on /webhooks/github. DDL only — no service
-- consumers in this migration. PR2 wires the poller behind a feature
-- flag; PR3 starts persisting to cascade_retrigger; PR4 cuts traffic
-- over and removes Tailscale Funnel; PR5 deletes the webhook adapter.
--
-- See plans://Multica/2026-05-18-pul-166-github-polling.md for the
-- per-PR plan, gate criteria, and rollback steps. Originating issue
-- is PUL-166 (rooted in PUL-160: iPhone Safari broken by Funnel
-- publishing multica.tail38d0e3.ts.net in public DNS).
--
-- One row per polled repository. PR2 seeds the table on startup from
-- MULTICA_GITHUB_POLL_REPOS via INSERT ... ON CONFLICT (repo) DO
-- NOTHING. Reads/writes are by primary key only — no secondary index
-- needed at this scale (≤10 watched repos in any realistic config).

BEGIN;

CREATE TABLE github_poll_cursor (
    -- "owner/name" form, e.g. "rabbeet/Pulse". TEXT (not VARCHAR(N)) so
    -- a repo rename doesn't require a migration. PRIMARY KEY gives us
    -- the per-repo upsert path the poller uses every tick.
    repo TEXT PRIMARY KEY,

    -- GitHub REST /events returns numeric event ids (string-encoded in
    -- JSON, but always integral). BIGINT is wide enough for the
    -- foreseeable future and matches GitHub's documented behavior.
    -- NULL means "no successful poll yet"; PR2's poller treats that
    -- as "start from the most recent /events page" rather than
    -- backfilling history.
    last_event_id BIGINT NULL,

    -- Observability only. Alerts watch
    -- now() - last_polled_at > tick * 2 to catch stuck pollers.
    -- Cheap to update; written once per successful tick regardless
    -- of whether new events were found.
    last_polled_at TIMESTAMPTZ NULL,

    -- HTTP ETag of the last 200 response. PR2's poller sends it back
    -- as `If-None-Match` so steady-state ticks return 304 and do not
    -- spend rate budget (GitHub does not decrement the limit on 304).
    -- NULL until the first 200 lands; cleared if a 4xx invalidates
    -- the cursor.
    etag TEXT NULL
);

COMMIT;
