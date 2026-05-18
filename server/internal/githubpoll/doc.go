// Package githubpoll implements outbound polling of the GitHub REST
// /events endpoint. It is the replacement ingress for the inbound
// webhook receiver in server/internal/webhooks/github that PUL-166 is
// retiring (root cause: Tailscale Funnel publishing
// multica.tail38d0e3.ts.net in public DNS broke iPhone Safari per
// PUL-160).
//
// The poller is multi-PR by design:
//
//   - PR1 (already shipped): migration 080 adds github_poll_cursor.
//   - PR2 (this package, dry-run): every piece end-to-end except for
//     persistence. classify-the-event lives here, with the same
//     EventType constants as the webhook adapter, so the cutover in
//     PR4 is one config flag flip. The sink is replaceable; in PR2 it
//     is the LoggingSink that just slogs.
//   - PR3: swap LoggingSink for CascadeRetriggerSink that writes the
//     same rows the webhook adapter writes today.
//   - PR4: flip webhook OFF, poll ON, snip Funnel.
//   - PR5: delete the webhook adapter after 7 days of stability.
//
// See plans://Multica/2026-05-18-pul-166-github-polling.md for the
// per-PR gate criteria. The eng review attached to PUL-166 lists the
// failure modes the supervisor + cursor design closes off.
//
// Feature flags read at startup:
//
//	MULTICA_GITHUB_POLL_ENABLED        bool (default false)
//	MULTICA_GITHUB_POLL_DRY_RUN        bool (default true; PR3 flips)
//	MULTICA_GITHUB_POLL_REPOS          csv of "owner/name" (required)
//	MULTICA_GITHUB_POLL_INTERVAL_SEC   int  (default 30)
//	MULTICA_GITHUB_API_TOKEN           string (PAT; PUL-141 swaps source)
//
// Auth is hidden behind TokenSource so PUL-141's GitHub App migration
// replaces a single implementation without touching the poller.
package githubpoll
