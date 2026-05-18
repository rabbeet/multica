package githubpoll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/cascade"
	"github.com/multica-ai/multica/server/internal/webhooks"
)

// retriggerInserter is the subset of cascade.Store the sink needs.
// Defined here (not in cascade) so tests can fake the dependency
// without standing up a real Postgres pool. Production callers pass
// a *cascade.Store, which satisfies this interface naturally.
type retriggerInserter interface {
	InsertRetrigger(ctx context.Context, p cascade.RetriggerInsert) (int64, error)
}

// CascadeRetriggerSink writes classified poller events into the
// cascade_retrigger table — the same destination the inbound webhook
// adapter writes to. This is the PR3 swap that PR2 reserved a seam
// for (LoggingSink → CascadeRetriggerSink).
//
// Behavior on collision: the underlying cascade.Store INSERT uses
// `ON CONFLICT (event_id) DO NOTHING` and returns
// ErrRetriggerAlreadyExists when the row was a duplicate. Sink
// treats that as a benign no-op (logged at debug) so a re-tick that
// re-fetches the same events from /events doesn't double-fire the
// cascade — the idempotency contract the cursor depends on.
type CascadeRetriggerSink struct {
	store   retriggerInserter
	logger  *slog.Logger
	metrics *Metrics
}

// NewCascadeRetriggerSink wraps a cascade.Store. logger nil falls
// back to slog.Default. metrics nil is tolerated for tests that
// don't observe metrics.
func NewCascadeRetriggerSink(store retriggerInserter, logger *slog.Logger, metrics *Metrics) *CascadeRetriggerSink {
	return &CascadeRetriggerSink{store: store, logger: logger, metrics: metrics}
}

// Submit implements Sink. Returns nil on fresh insert AND on
// idempotent-collision: the cascade worker has already seen this
// event_id, so the duplicate from a re-tick is by design. Any other
// store error surfaces unchanged so the poller can decide whether
// to advance the cursor (it does not — see poller.tickOne sink
// failure path).
func (s *CascadeRetriggerSink) Submit(ctx context.Context, repo string, e webhooks.TriggerEvent) error {
	if s.store == nil {
		return fmt.Errorf("githubpoll.cascadesink: store not configured")
	}
	insert := cascade.RetriggerInsert{
		EventID:   e.EventID,
		PRURL:     e.PRURL,
		PRNumber:  int32(e.PRNumber),
		PRTitle:   e.PRTitle,
		HeadSHA:   e.HeadSHA,
		Branch:    e.Branch,
		EventType: e.EventType,
		// FiredAt left zero → DB default now() at the cascade.Store
		// SQL level. The TriggerEvent.ReceivedAt was set by the
		// webhook router for its INSERT; the poll path doesn't
		// have a comparable "wall-clock the GitHub edge stamped"
		// signal, so leaving FiredAt zero is the honest answer.
	}
	if _, err := s.store.InsertRetrigger(ctx, insert); err != nil {
		if errors.Is(err, cascade.ErrRetriggerAlreadyExists) {
			s.log().Debug("githubpoll.cascadesink.duplicate",
				"repo", repo,
				"event_id", e.EventID.String(),
				"event_type", e.EventType,
			)
			// Treat as success: the cascade has already accepted
			// this event_id, so we are idempotent on re-tick.
			return nil
		}
		// Real failure — DB outage, connection refused, etc.
		// Caller's tickOne will stop the cursor from advancing so
		// the next tick retries from the same id.
		return fmt.Errorf("githubpoll.cascadesink: insert %s: %w", e.EventID, err)
	}
	s.log().Info("githubpoll.cascadesink.inserted",
		"repo", repo,
		"event_id", e.EventID.String(),
		"event_type", e.EventType,
		"pr_number", e.PRNumber,
	)
	return nil
}

func (s *CascadeRetriggerSink) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}
