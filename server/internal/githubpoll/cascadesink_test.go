package githubpoll

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/cascade"
	"github.com/multica-ai/multica/server/internal/webhooks"
)

// fakeStore implements retriggerInserter for the sink tests without
// touching Postgres. inserts records every RetriggerInsert it sees
// so assertions can verify shape. errOnce + errAlways simulate the
// two error paths the sink contract documents (idempotent
// collision and arbitrary failure).
type fakeStore struct {
	inserts   []cascade.RetriggerInsert
	errOnce   error
	errAlways error
}

func (f *fakeStore) InsertRetrigger(_ context.Context, p cascade.RetriggerInsert) (int64, error) {
	if f.errAlways != nil {
		return 0, f.errAlways
	}
	if f.errOnce != nil {
		err := f.errOnce
		f.errOnce = nil
		return 0, err
	}
	f.inserts = append(f.inserts, p)
	return int64(len(f.inserts)), nil
}

func TestCascadeRetriggerSink_Submit_Inserts(t *testing.T) {
	store := &fakeStore{}
	sink := NewCascadeRetriggerSink(store, nil, nil)
	id := uuid.New()
	err := sink.Submit(context.Background(), "rabbeet/Pulse", webhooks.TriggerEvent{
		EventID:   id,
		EventType: webhooks.EventTypeCIFailure,
		PRURL:     "https://github.com/rabbeet/Pulse/pull/530",
		PRNumber:  530,
		PRTitle:   "[PUL-157] fix",
		HeadSHA:   "abc123",
		Branch:    "agent-1/pul-157",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(store.inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(store.inserts))
	}
	got := store.inserts[0]
	if got.EventID != id {
		t.Errorf("EventID = %v, want %v", got.EventID, id)
	}
	if got.EventType != webhooks.EventTypeCIFailure {
		t.Errorf("EventType = %q, want %q", got.EventType, webhooks.EventTypeCIFailure)
	}
	if got.PRNumber != 530 {
		t.Errorf("PRNumber = %d, want 530", got.PRNumber)
	}
	if got.PRTitle != "[PUL-157] fix" {
		t.Errorf("PRTitle = %q", got.PRTitle)
	}
	if !got.FiredAt.IsZero() {
		t.Errorf("FiredAt = %v, want zero (defaults to DB now())", got.FiredAt)
	}
}

func TestCascadeRetriggerSink_Submit_TreatsDuplicateAsSuccess(t *testing.T) {
	store := &fakeStore{errOnce: cascade.ErrRetriggerAlreadyExists}
	sink := NewCascadeRetriggerSink(store, nil, nil)
	err := sink.Submit(context.Background(), "rabbeet/Pulse", webhooks.TriggerEvent{
		EventID:   uuid.New(),
		EventType: webhooks.EventTypePRMerged,
	})
	if err != nil {
		t.Errorf("Submit on duplicate err = %v, want nil (idempotent no-op)", err)
	}
	if len(store.inserts) != 0 {
		t.Errorf("inserts on duplicate = %d, want 0", len(store.inserts))
	}
}

func TestCascadeRetriggerSink_Submit_PropagatesRealError(t *testing.T) {
	sentinel := errors.New("connection refused")
	store := &fakeStore{errAlways: sentinel}
	sink := NewCascadeRetriggerSink(store, nil, nil)
	err := sink.Submit(context.Background(), "rabbeet/Pulse", webhooks.TriggerEvent{
		EventID:   uuid.New(),
		EventType: webhooks.EventTypePRMerged,
	})
	if err == nil {
		t.Fatal("Submit on real error returned nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err chain does not include sentinel: %v", err)
	}
}

func TestCascadeRetriggerSink_Submit_NoStore(t *testing.T) {
	// Defensive — constructor doesn't reject nil, but Submit should
	// fail fast rather than nil-panic in production.
	sink := NewCascadeRetriggerSink(nil, nil, nil)
	err := sink.Submit(context.Background(), "x/y", webhooks.TriggerEvent{})
	if err == nil {
		t.Error("Submit with nil store returned nil, want error")
	}
}
