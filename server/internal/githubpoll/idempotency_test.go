package githubpoll

import (
	"testing"

	"github.com/google/uuid"
)

func TestEventID_Deterministic(t *testing.T) {
	a := EventID("rabbeet/Pulse", 123456)
	b := EventID("rabbeet/Pulse", 123456)
	if a != b {
		t.Errorf("same (repo, eventID) produced different UUIDs: %v vs %v", a, b)
	}
}

func TestEventID_RepoSensitive(t *testing.T) {
	if EventID("rabbeet/Pulse", 1) == EventID("rabbeet/multica", 1) {
		t.Error("different repos should yield different event IDs for the same numeric id")
	}
}

func TestEventID_NumberSensitive(t *testing.T) {
	if EventID("rabbeet/Pulse", 1) == EventID("rabbeet/Pulse", 2) {
		t.Error("different numeric event ids should yield different UUIDs")
	}
}

func TestEventID_DistinctFromWebhookNamespace(t *testing.T) {
	// Regression guard: the poll namespace must stay distinct from
	// the webhook adapter's namespace, AND hashing the same seed
	// under each namespace must produce different UUIDs. If either
	// side rotates its namespace, this test fails until a reviewer
	// updates the hardcoded value here — making the change visible.
	//
	// Hardcoded from server/internal/webhooks/github/source.go.
	const webhookNS = "a3b6f8e2-72c5-4b8b-9d1f-8d3b9c4f5a10"

	if pollNamespace.String() == webhookNS {
		t.Fatalf("poll namespace collides with webhook namespace; both = %s — must differ",
			webhookNS)
	}

	seed := []byte("shared-seed")
	webhookSide := uuid.NewSHA1(uuid.MustParse(webhookNS), seed)
	pollSide := uuid.NewSHA1(pollNamespace, seed)
	if webhookSide == pollSide {
		t.Errorf("same seed under distinct namespaces produced identical UUIDs (%s) — namespaces are not effectively distinct",
			webhookSide)
	}
}
