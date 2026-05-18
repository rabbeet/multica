package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestInboxOwnership_EndToEnd exercises the full ListInbox path for
// the PUL-180 ownership chip — inserting fixture rows in the real
// schema, running the query that the handler runs in prod, mapping
// through inboxRowToResponse, and asserting the JSON-wire shape the
// frontend depends on. This is the integration counterpart of the
// pure-fn unit tests in inbox_ownership_test.go (TestDeriveOwnership);
// together they cover the test plan post-eng-review.
//
// Test plan cases covered here:
//   1. active task running   → ownership=agent + meta.agent_name + since=started_at
//   2. status=waiting, no task → ownership=waiting + reason=review
//   3. complete active task  → ownership transitions to me (when no waiting)
//   4. status=cancelled      → ownership=null (phase hide)
//   5. T4 JSON shape         → ownership/ownership_meta are explicit null,
//                              not missing keys, for old-client compatibility
func TestInboxOwnership_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// Each scenario gets its own issue + inbox_item so assertions stay
	// independent. Distinct `number` values dodge the workspace+number
	// unique constraint.
	type scenario struct {
		name        string
		issueID     string
		inboxItemID string
		// fixture knobs
		issueStatus     string
		hasActiveTask   bool
		activeTaskState string // queued | dispatched | running | completed
	}
	scenarios := []scenario{
		{name: "1.running-task", issueStatus: "in_progress", hasActiveTask: true, activeTaskState: "running"},
		{name: "2.waiting-status", issueStatus: "waiting", hasActiveTask: false},
		{name: "3.task-completed", issueStatus: "in_progress", hasActiveTask: true, activeTaskState: "completed"},
		{name: "4.cancelled", issueStatus: "cancelled", hasActiveTask: false},
	}

	// Reuse the global handler test runtime (testRuntimeID) so we
	// don't fight with the ON CONFLICT shape of agent_runtime — the
	// (workspace_id, daemon_id, provider) UNIQUE includes daemon_id
	// which is NULL for handler test fixtures, making an upsert
	// awkward. We only need a stable agent reference for the
	// agent_task_queue.agent_id FK.
	var agentID string
	err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'workspace', 1, $4)
		RETURNING id
	`, testWorkspaceID, "pul-180-agent", testRuntimeID, testUserID).Scan(&agentID)
	if err != nil {
		t.Fatalf("setup: create agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})

	for i := range scenarios {
		err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, updated_at)
			VALUES ($1, $2, $3, 'medium', $4, 'member', $5, now())
			RETURNING id
		`, testWorkspaceID, "PUL-180 "+scenarios[i].name, scenarios[i].issueStatus, testUserID, 91000+i).Scan(&scenarios[i].issueID)
		if err != nil {
			t.Fatalf("setup: insert issue %s: %v", scenarios[i].name, err)
		}

		err = testPool.QueryRow(ctx, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, issue_id, title, read, archived)
			VALUES ($1, 'member', $2, 'new_comment', $3, $4, false, false)
			RETURNING id
		`, testWorkspaceID, testUserID, scenarios[i].issueID, "PUL-180 "+scenarios[i].name+" inbox").Scan(&scenarios[i].inboxItemID)
		if err != nil {
			t.Fatalf("setup: insert inbox_item %s: %v", scenarios[i].name, err)
		}

		if scenarios[i].hasActiveTask {
			started := time.Now().Add(-5 * time.Minute)
			var dispatched, startedAt *time.Time
			switch scenarios[i].activeTaskState {
			case "running":
				d := started.Add(-time.Minute)
				dispatched = &d
				startedAt = &started
			case "dispatched":
				d := started
				dispatched = &d
			case "completed":
				d := started.Add(-time.Minute)
				dispatched = &d
				startedAt = &started
			}
			_, err := testPool.Exec(ctx, `
				INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, dispatched_at, started_at)
				VALUES ($1, $2, $3, $4, $5, $6)
			`, agentID, testRuntimeID, scenarios[i].issueID, scenarios[i].activeTaskState, dispatched, startedAt)
			if err != nil {
				t.Fatalf("setup: insert agent_task_queue %s: %v", scenarios[i].name, err)
			}
		}
	}

	t.Cleanup(func() {
		for _, sc := range scenarios {
			testPool.Exec(ctx, `DELETE FROM inbox_item WHERE id = $1`, sc.inboxItemID)
			testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, sc.issueID)
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, sc.issueID)
		}
	})

	rows, err := testHandler.Queries.ListInboxItems(ctx, db.ListInboxItemsParams{
		WorkspaceID:   parseUUID(testWorkspaceID),
		RecipientType: "member",
		RecipientID:   parseUUID(testUserID),
	})
	if err != nil {
		t.Fatalf("ListInboxItems: %v", err)
	}

	// Index responses by inbox_item id so we can assert per-scenario.
	got := map[string]InboxItemResponse{}
	for _, r := range rows {
		resp := inboxRowToResponse(r)
		got[resp.ID] = resp
	}

	cases := []struct {
		scenario      scenario
		wantOwnership *string
		wantReason    *string
		wantAgentName *string
	}{
		{
			scenario:      scenarios[0],
			wantOwnership: stringPtr("agent"),
			wantAgentName: stringPtr("pul-180-agent"),
		},
		{
			scenario:      scenarios[1],
			wantOwnership: stringPtr("waiting"),
			wantReason:    stringPtr("review"),
		},
		{
			// Completed task is not in the active-task CTE window
			// (status NOT IN queued/dispatched/running), so the
			// ownership falls through to "me".
			scenario:      scenarios[2],
			wantOwnership: stringPtr("me"),
		},
		{
			// phase=cancelled hides the chip entirely → both
			// ownership and meta marshal as JSON null.
			scenario:      scenarios[3],
			wantOwnership: nil,
		},
	}

	for _, tc := range cases {
		resp, ok := got[tc.scenario.inboxItemID]
		if !ok {
			t.Errorf("%s: inbox response missing", tc.scenario.name)
			continue
		}
		if !ptrEqual(resp.Ownership, tc.wantOwnership) {
			t.Errorf("%s: ownership = %v, want %v",
				tc.scenario.name, ptrDeref(resp.Ownership), ptrDeref(tc.wantOwnership))
		}
		if tc.wantOwnership == nil {
			if resp.OwnershipMeta != nil {
				t.Errorf("%s: ownership_meta must be nil when ownership is hidden, got %+v",
					tc.scenario.name, resp.OwnershipMeta)
			}
			continue
		}
		if resp.OwnershipMeta == nil {
			t.Errorf("%s: ownership_meta unexpectedly nil (ownership=%s)",
				tc.scenario.name, *resp.Ownership)
			continue
		}
		if !ptrEqual(resp.OwnershipMeta.Reason, tc.wantReason) {
			t.Errorf("%s: reason = %v, want %v",
				tc.scenario.name, ptrDeref(resp.OwnershipMeta.Reason), ptrDeref(tc.wantReason))
		}
		if !ptrEqual(resp.OwnershipMeta.AgentName, tc.wantAgentName) {
			t.Errorf("%s: agent_name = %v, want %v",
				tc.scenario.name, ptrDeref(resp.OwnershipMeta.AgentName), ptrDeref(tc.wantAgentName))
		}
	}

	// T4: JSON-shape regression. When the chip is hidden (case 4),
	// the wire payload must encode `ownership: null` and
	// `ownership_meta: null` — NOT omit the keys. Old TypeScript
	// clients declare `ownership: OwnershipSlug | null` (not
	// `ownership?: OwnershipSlug`), so a missing key would surface
	// as `undefined` and slip past the null-guard in OwnershipChip.
	resp4, ok := got[scenarios[3].inboxItemID]
	if !ok {
		t.Fatalf("T4: missing response for cancelled scenario")
	}
	encoded, err := json.Marshal(resp4)
	if err != nil {
		t.Fatalf("T4: json marshal: %v", err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"ownership":null`) {
		t.Errorf("T4: expected literal `\"ownership\":null` in payload, got: %s", body)
	}
	if !strings.Contains(body, `"ownership_meta":null`) {
		t.Errorf("T4: expected literal `\"ownership_meta\":null` in payload, got: %s", body)
	}
}

func ptrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func ptrDeref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
