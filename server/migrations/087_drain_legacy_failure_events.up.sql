-- PUL-212: drain pending rows with event_types dropped by classifier.
--
-- After this PR, classify.go no longer emits ci_failure or
-- pr_review_change. Any rows INSERTed before this migration runs
-- that the worker hasn't yet processed are now orphaned by design
-- (the autofix-pipeline in rabbeet/Pulse is the canonical channel
-- for these events — PUL-209). Mark them scope_filter_skip with an
-- explicit action_reason so operators reading `multica issue
-- cascade` see why they didn't spawn.
--
-- The deploy → migration race window for rows the worker grabs FIRST
-- (between the new code starting up and this migration running) is
-- closed by the CQ2 guard in server/internal/cascade/worker.go's
-- processOne — see PUL-212. This migration handles the rows the
-- worker hasn't gotten to yet.
--
-- WHERE processed_at IS NULL: idempotent — re-running the migration
-- would touch 0 rows. Safe to re-apply.
--
-- Does NOT alter the CHECK constraint on event_type. Already-processed
-- rows keep their legacy event_type in the audit log. The CHECK drop
-- is a separate follow-up PR through 1-2 months when retention has
-- cleaned the legacy rows. See webhooks/event.go's `Deprecated:`
-- comments for the full plan.

BEGIN;

UPDATE cascade_retrigger
SET processed_at = now(),
    action = 'scope_filter_skip',
    action_reason = 'PUL-212: legacy event_type dropped by classifier'
WHERE event_type IN ('ci_failure', 'pr_review_change')
  AND processed_at IS NULL;

COMMIT;
