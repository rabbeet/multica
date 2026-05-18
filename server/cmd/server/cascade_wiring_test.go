package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/cascade"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestFormatCascadeWakeMessage_CIFailure locks in the exact substrings the
// agent prompt path will surface to the cascade-woken agent. The agent
// reads this literally (via daemon.buildCommentPrompt → [NEW COMMENT]
// block) and routes its first tool call on the actionable hint — vague
// wording = regressing PUL-168.
func TestFormatCascadeWakeMessage_CIFailure(t *testing.T) {
	tc := cascade.TriggerContext{
		EventType: "ci_failure",
		PRNumber:  530,
		HeadSHA:   "3d26b7f12345abcdef",
		PRURL:     "https://github.com/rabbeet/multica/pull/530",
	}
	got := formatCascadeWakeMessage(tc)
	for _, want := range []string{
		"event_type=ci_failure",
		"PR #530",
		"head_sha=3d26b7f1",            // short, exactly 8 chars
		"gh pr checks 530",             // actionable hint: exact PR number
		"gh run list --branch <branch>", // secondary hint
		"Do NOT poll the issue thread",  // anti-regression cue
		"https://github.com/rabbeet/multica/pull/530",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatCascadeWakeMessage missing %q\n--- full ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "head_sha=3d26b7f12345abcdef") {
		t.Errorf("formatCascadeWakeMessage must short-truncate head_sha to 8 chars, got full SHA in output:\n%s", got)
	}
}

// TestFormatCascadeWakeMessage_NoPR exercises the no-PR branch (a
// future direct issue event with no associated PR). The actionable
// hint must degrade gracefully — no "PR #0" garbage, no panic.
func TestFormatCascadeWakeMessage_NoPR(t *testing.T) {
	tc := cascade.TriggerContext{
		EventType: "ci_failure",
		HeadSHA:   "abc",
	}
	got := formatCascadeWakeMessage(tc)
	if strings.Contains(got, "PR #0") {
		t.Errorf("formatCascadeWakeMessage must not print 'PR #0' when PRNumber=0:\n%s", got)
	}
	if !strings.Contains(got, "gh pr checks <pr>") {
		t.Errorf("formatCascadeWakeMessage must keep the actionable hint placeholder when PRNumber=0:\n%s", got)
	}
	if !strings.Contains(got, "head_sha=abc") {
		t.Errorf("formatCascadeWakeMessage must include short head_sha as-is when <8 chars:\n%s", got)
	}
}

// TestFormatCascadeWakeMessage_UnknownEvent verifies the default branch
// doesn't emit CI-specific hints for unrelated event types.
func TestFormatCascadeWakeMessage_UnknownEvent(t *testing.T) {
	tc := cascade.TriggerContext{
		EventType: "pr_review_requested",
		PRNumber:  99,
	}
	got := formatCascadeWakeMessage(tc)
	if strings.Contains(got, "gh pr checks") {
		t.Errorf("formatCascadeWakeMessage must NOT emit gh pr checks for non-ci_failure events:\n%s", got)
	}
	if !strings.Contains(got, "event_type=pr_review_requested") {
		t.Errorf("formatCascadeWakeMessage must include event_type in default branch:\n%s", got)
	}
}

// TestCascadeSpawner_HappyPath_DBRequired covers PUL-168 criterion #1:
// the spawned task carries a non-empty trigger_summary AND the synth
// system comment exists in the thread carrying the wake-up reason.
// Requires a live test DB; skips when DATABASE_URL is unreachable.
func TestCascadeSpawner_HappyPath_DBRequired(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test DB not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)

	// Use an existing test agent + issue (fixture wired in TestMain).
	agentID := getAgentID(t)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	assignIssueToAgent(t, issueID, agentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		deleteSystemCommentsForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	taskSvc := service.NewTaskService(queries, testPool, nil, events.New())
	bus := events.New()
	spawner := &taskServiceSpawner{
		pool:    testPool,
		queries: queries,
		taskSvc: taskSvc,
		bus:     bus,
	}

	tc := cascade.TriggerContext{
		EventID:   uuid.New(),
		EventType: "ci_failure",
		PRNumber:  530,
		HeadSHA:   "3d26b7f12345abcdef",
	}
	issueUUID := util.MustParseUUID(issueID).Bytes
	if err := spawner.Spawn(ctx, issueUUID, tc); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Assert: synth comment exists with type='system' and content includes
	// the trigger reason.
	var synthCommentID, content, commentType, authorType string
	var authorIDValid bool
	err := testPool.QueryRow(ctx, `
		SELECT id::text, content, type, author_type, (author_id IS NOT NULL)
		FROM comment
		WHERE issue_id = $1 AND type = 'system'
		ORDER BY created_at DESC LIMIT 1`, issueID,
	).Scan(&synthCommentID, &content, &commentType, &authorType, &authorIDValid)
	if err != nil {
		t.Fatalf("look up synth comment: %v", err)
	}
	if commentType != "system" {
		t.Errorf("synth comment type = %q, want 'system'", commentType)
	}
	if authorType != "system" {
		t.Errorf("synth comment author_type = %q, want 'system'", authorType)
	}
	if authorIDValid {
		t.Errorf("synth comment author_id must be NULL (migration 078 dropped NOT NULL); got non-null")
	}
	for _, want := range []string{"ci_failure", "PR #530", "3d26b7f1"} {
		if !strings.Contains(content, want) {
			t.Errorf("synth comment content missing %q\n--- got ---\n%s", want, content)
		}
	}

	// Assert: spawned task points at the synth comment and trigger_summary is populated.
	var triggerCommentID, triggerSummary string
	err = testPool.QueryRow(ctx, `
		SELECT trigger_comment_id::text, COALESCE(trigger_summary, '')
		FROM agent_task_queue
		WHERE issue_id = $1 AND status IN ('queued', 'dispatched')
		ORDER BY created_at DESC LIMIT 1`, issueID,
	).Scan(&triggerCommentID, &triggerSummary)
	if err != nil {
		t.Fatalf("look up spawned task: %v", err)
	}
	if triggerCommentID != synthCommentID {
		t.Errorf("task.trigger_comment_id = %q, want %q", triggerCommentID, synthCommentID)
	}
	if triggerSummary == "" {
		t.Error("task.trigger_summary is empty; expected non-empty preview of the synth comment")
	}
	if !strings.Contains(triggerSummary, "ci_failure") {
		t.Errorf("task.trigger_summary missing 'ci_failure'; got %q", triggerSummary)
	}
}

// TestCascadeSpawner_GatedRollback_DBRequired covers PUL-168 idempotency:
// when EnqueueTaskForIssueInTx returns ErrAssigneeAgentArchived, the Tx
// must roll back so no orphan synth comment is committed.
func TestCascadeSpawner_GatedRollback_DBRequired(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test DB not available")
	}
	ctx := context.Background()
	queries := db.New(testPool)

	// Create an issue assigned to an archived agent so EnqueueTaskForIssueInTx
	// returns ErrAssigneeAgentArchived → Tx rolls back.
	archivedAgentID := createArchivedAgent(t)
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	assignIssueToAgent(t, issueID, archivedAgentID)
	t.Cleanup(func() {
		clearTasks(t, issueID)
		deleteSystemCommentsForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	taskSvc := service.NewTaskService(queries, testPool, nil, events.New())
	spawner := &taskServiceSpawner{
		pool:    testPool,
		queries: queries,
		taskSvc: taskSvc,
		bus:     events.New(),
	}

	tc := cascade.TriggerContext{
		EventID:   uuid.New(),
		EventType: "ci_failure",
		PRNumber:  531,
		HeadSHA:   "deadbeef",
	}
	issueUUID := util.MustParseUUID(issueID).Bytes
	err := spawner.Spawn(ctx, issueUUID, tc)
	if err == nil {
		t.Fatal("Spawn must return an error when agent is archived")
	}
	// Must wrap cascade.ErrSpawnGated so the worker marks the row scope_filter_skip.
	if !isSpawnGated(err) {
		t.Errorf("Spawn error must wrap cascade.ErrSpawnGated; got %v", err)
	}

	// Assert: zero synth comments committed (Tx rolled back).
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM comment WHERE issue_id = $1 AND type = 'system'`,
		issueID,
	).Scan(&count); err != nil {
		t.Fatalf("count synth comments: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 orphan synth comments after gated rollback, got %d", count)
	}

	// Assert: zero queued tasks (rolled back).
	if got := countPendingTasks(t, issueID); got != 0 {
		t.Errorf("expected 0 queued tasks after gated rollback, got %d", got)
	}
}

func isSpawnGated(err error) bool {
	// errors.Is via the unwrap chain.
	for e := err; e != nil; {
		if e == cascade.ErrSpawnGated {
			return true
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = unwrapper.Unwrap()
	}
	return false
}

// assignIssueToAgent flips issue.assignee_id/type so the cascade
// EnqueueTaskForIssue path can find an agent to enqueue against.
func assignIssueToAgent(t *testing.T, issueID, agentID string) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`UPDATE issue SET assignee_type = 'agent', assignee_id = $1 WHERE id = $2`,
		agentID, issueID)
	if err != nil {
		t.Fatalf("assign issue to agent: %v", err)
	}
}

// deleteSystemCommentsForIssue cleans up synth comments between tests.
func deleteSystemCommentsForIssue(t *testing.T, issueID string) {
	t.Helper()
	_, _ = testPool.Exec(context.Background(),
		`DELETE FROM comment WHERE issue_id = $1 AND type = 'system'`, issueID)
}

// createArchivedAgent inserts an agent with archived_at set so
// EnqueueTaskForIssueInTx returns ErrAssigneeAgentArchived.
func createArchivedAgent(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	// Reuse the existing agent's runtime + runtime_mode so we don't need
	// to provision one. runtime_mode is NOT NULL with a CHECK
	// constraint (001_init.up.sql:41), so we must inherit it from the
	// base agent rather than picking a default.
	var runtimeID, runtimeMode string
	if err := testPool.QueryRow(ctx,
		`SELECT runtime_id::text, runtime_mode
		   FROM agent
		  WHERE workspace_id = $1 AND archived_at IS NULL LIMIT 1`,
		testWorkspaceID,
	).Scan(&runtimeID, &runtimeMode); err != nil {
		t.Fatalf("look up base agent: %v", err)
	}
	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_id, runtime_mode, visibility, archived_at)
		VALUES ($1, 'archived-test-agent', $2, $3, 'workspace', now())
		RETURNING id::text`, testWorkspaceID, runtimeID, runtimeMode,
	).Scan(&id); err != nil {
		t.Fatalf("create archived agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id)
	})
	return id
}

// TestNotification_SystemComment_NoFanout covers PUL-168 amendment 4:
// system-typed comments must not generate a `new_comment` notification
// for every issue subscriber. Otherwise every CI failure spams everyone.
func TestNotification_SystemComment_NoFanout(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test DB not available")
	}
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	subEmail := "notif-system-sub@multica.ai"
	subID := createTestUser(t, subEmail)
	t.Cleanup(func() { cleanupTestUser(t, subEmail) })

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	addTestSubscriber(t, issueID, "member", subID, "manual")

	bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": handler.CommentResponse{
				ID:         "00000000-0000-0000-0000-000000000001",
				IssueID:    issueID,
				AuthorType: "system",
				AuthorID:   "",
				Content:    "🤖 cascade wake-up: event_type=ci_failure, PR #530",
				Type:       "system",
			},
			"issue_title":  "system-comment fan-out test",
			"issue_status": "in_progress",
		},
	})

	if items := inboxItemsForRecipient(t, queries, subID); len(items) != 0 {
		t.Errorf("expected 0 inbox items for subscriber on system comment, got %d", len(items))
	}
}

// TestSubscriber_SystemComment_NoAutoSubscribe verifies the
// subscriber-listener side of the type='system' skip — without it the
// NULL author_id row would attempt to addSubscriber and either crash
// or write an "issue subscribed by NULL" row, both wrong.
func TestSubscriber_SystemComment_NoAutoSubscribe(t *testing.T) {
	if testPool == nil {
		t.Skip("integration test DB not available")
	}
	queries := db.New(testPool)
	bus := events.New()
	registerSubscriberListeners(bus, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_subscriber WHERE issue_id = $1`, issueID)
		cleanupTestIssue(t, issueID)
	})

	// Snapshot subscriber count BEFORE the event.
	before := countSubscribers(t, issueID)

	bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: testWorkspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": map[string]any{
				"id":          "00000000-0000-0000-0000-000000000002",
				"issue_id":    issueID,
				"author_type": "system",
				"author_id":   "", // empty string mirrors uuidStringNullable's NULL path
				"content":     "🤖 cascade wake-up",
				"type":        "system",
			},
		},
	})

	after := countSubscribers(t, issueID)
	if after != before {
		t.Errorf("system comment must not auto-subscribe; before=%d after=%d", before, after)
	}
}

func countSubscribers(t *testing.T, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_subscriber WHERE issue_id = $1`, issueID,
	).Scan(&n); err != nil {
		t.Fatalf("count subscribers: %v", err)
	}
	return n
}

