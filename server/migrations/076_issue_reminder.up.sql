-- PUL-154: «Wake up in N» button — one-shot deferred reminders on issues.
--
-- A reminder, when fire_at arrives, posts a comment of type='wake_up' on the
-- issue (handled by reminder_scheduler.go via service.CommentService.Create
-- with an InjectStatusTransition that flips waiting/backlog → todo in the
-- same transaction). If a comment was added to the issue between
-- reminder.created_at and fire_at, the reminder cancels silently with
-- cancel_reason='activity' (handled by MarkRemindersCancelledByActivity
-- prune-pass).
--
-- Idempotency: the firing_at column is a two-phase claim marker. Claim sets
-- firing_at=now() under FOR UPDATE SKIP LOCKED; finalize sets status='fired'
-- and clears firing_at. If a worker crashes between claim and finalize,
-- ResetStuckClaims clears stale firing_at values so the next tick retries.
-- Status-history dedup is handled separately by the existing
-- issue_status_history.UNIQUE(source, ref_id) constraint with ref_id =
-- reminder.id.

BEGIN;

CREATE TABLE issue_reminder (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id          UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    created_by_type   TEXT NOT NULL CHECK (created_by_type IN ('member', 'agent')),
    created_by_id     UUID NOT NULL,
    fire_at           TIMESTAMPTZ NOT NULL,
    note              TEXT,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'fired', 'cancelled', 'superseded')),
    firing_at         TIMESTAMPTZ,
    fired_at          TIMESTAMPTZ,
    fired_comment_id  UUID REFERENCES comment(id) ON DELETE SET NULL,
    cancelled_at      TIMESTAMPTZ,
    cancel_reason     TEXT CHECK (cancel_reason IN ('manual', 'activity', 'creator_gone')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Cap free-form note at 500 chars (mirrored by server-side validation).
    CHECK (note IS NULL OR length(note) <= 500),

    -- Reject reminders that would fire in the past at creation time.
    CHECK (fire_at > created_at)
);

-- Hot path: scheduler tick query. Only unclaimed pending reminders matter.
CREATE INDEX idx_issue_reminder_due
    ON issue_reminder(fire_at)
    WHERE status = 'pending' AND firing_at IS NULL;

-- Recovery path: stuck claims that need resetting.
CREATE INDEX idx_issue_reminder_recovery
    ON issue_reminder(firing_at)
    WHERE status = 'pending' AND firing_at IS NOT NULL;

-- UI chip query: list pending reminders for a given issue.
CREATE INDEX idx_issue_reminder_issue
    ON issue_reminder(issue_id, status);

COMMIT;
