package githubpoll

import (
	"strconv"

	"github.com/google/uuid"
)

// pollNamespace is the UUIDv5 namespace for poll-derived event IDs.
// Pinned once and forever — change it and existing cascade_retrigger
// rows would never match new ones, so re-deliveries through the poll
// channel after a namespace rotation would dedup-fail and double-fire
// the cascade.
//
// Distinct from server/internal/webhooks/github.deliveryNamespace so
// the two channels can run in parallel (during PR3 dry-run staging)
// without colliding on each other's idempotency keys. Hard cutover
// in PR4 (webhook OFF → drain → poll ON) means parallel write is not
// the steady-state, but the namespaces stay separate as a
// belt-and-braces precaution.
var pollNamespace = uuid.MustParse("9e2d4f1c-8a7b-4e3f-b6c2-1f5a8d9e0c3a")

// EventID derives the deterministic UUIDv5 used as the idempotency
// key for cascade_retrigger.event_id when an event was discovered via
// polling. Seed shape: "{repo}:{eventID}" where eventID is the
// GitHub REST /events numeric id. Returns the same UUID for the same
// (repo, eventID) tuple on every call — so PR3's ON CONFLICT DO
// NOTHING absorbs re-polled events as silent no-ops.
func EventID(repo string, ghEventID int64) uuid.UUID {
	seed := repo + ":" + strconv.FormatInt(ghEventID, 10)
	return uuid.NewSHA1(pollNamespace, []byte(seed))
}
