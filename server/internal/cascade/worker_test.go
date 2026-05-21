package cascade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// workerTestDBPool / workerMakeWorkspaceAndUser / workerInsertCascadeIssue
// are local fixtures specific to worker_test.go. They mirror the
// pattern from internal/handler/handler_test.go: read DATABASE_URL,
// fall back to dev defaults, skip if unreachable. Named with a
// worker prefix to avoid collision when a sibling test file is added.
func workerTestDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no database: %v", err)
		return nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
		return nil
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func workerMakeWorkspaceAndUser(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, func()) {
	t.Helper()
	ctx := context.Background()
	var userID, workspaceID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('test', $1) RETURNING id`,
		"worker-test-"+uuid.New().String()+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('worker-test', $1) RETURNING id`,
		"worker-test-"+uuid.New().String()[:8]).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		workspaceID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	wsUUID, _ := uuid.Parse(workspaceID)
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	}
	return wsUUID, cleanup
}

func workerInsertCascadeIssue(t *testing.T, pool *pgxpool.Pool, workspaceID uuid.UUID, number int, state string, _ *time.Time, progressJSON string) uuid.UUID {
	t.Helper()
	var creatorID string
	if err := pool.QueryRow(context.Background(),
		`SELECT user_id::text FROM member WHERE workspace_id = $1 LIMIT 1`, workspaceID).Scan(&creatorID); err != nil {
		t.Fatalf("lookup owner: %v", err)
	}
	var pj any
	if progressJSON != "" {
		pj = progressJSON
	}
	var id string
	if err := pool.QueryRow(context.Background(), `
        INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number,
                           cascade_state, cascade_started_at, cascade_progress)
        VALUES ($1, $2, 'in_progress', 'member', $3, $4, $5, now() - interval '5 minutes', $6::jsonb)
        RETURNING id`,
		workspaceID, "worker test "+state, creatorID, number, state, pj,
	).Scan(&id); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	u, _ := uuid.Parse(id)
	return u
}

// fakeSpawner records every Spawn call. HasActiveRun returns the
// stored bool.
type fakeSpawner struct {
	spawnCalls atomic.Int64
	hasActive  atomic.Bool
	spawnErr   error
	lastIssue  uuid.UUID
	lastCtx    TriggerContext
}

func (f *fakeSpawner) Spawn(_ context.Context, issueID uuid.UUID, tc TriggerContext) error {
	f.spawnCalls.Add(1)
	f.lastIssue = issueID
	f.lastCtx = tc
	return f.spawnErr
}
func (f *fakeSpawner) HasActiveRun(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.hasActive.Load(), nil
}

func setupWorkerTest(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, func()) {
	t.Helper()
	pool := workerTestDBPool(t)
	if pool == nil {
		return nil, uuid.Nil, uuid.Nil, nil
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	// Insert an issue with cascade_state='approved' so the worker
	// has a target to spawn for.
	issueID := workerInsertCascadeIssue(t, pool, ws, 9001, "approved", nil, `{"total_prs":3,"current_step":1}`)
	return pool, ws, issueID, cleanup
}

// insertRetrigger inserts a row directly with issue_id set so the
// worker's lookup path can be skipped and we test the rest of the
// pipeline.
func insertRetrigger(t *testing.T, pool *pgxpool.Pool, issueID uuid.UUID, prURL, headSHA, eventType string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, issue_id, pr_url, pr_number, head_sha, event_type)
        VALUES ($1, $2, $3, 1, $4, $5)
        RETURNING id`,
		uuid.New(), issueID, prURL, headSHA, eventType,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert retrigger: %v", err)
	}
	return id
}

func TestWorker_HappyPath_SpawnsAndMarks(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetrigger(t, pool, issueID, "https://github.com/o/r/pull/1", "sha-1", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected 1 spawn, got %d", sp.spawnCalls.Load())
	}
	if sp.lastIssue != issueID {
		t.Errorf("spawned wrong issue: got %v, want %v", sp.lastIssue, issueID)
	}

	// Row must be marked processed with action='spawn' and a non-empty
	// action_reason (PUL-198). For a non-pr_merged event there is no
	// deploy_flip prefix, so the bare "spawned" sentinel is expected.
	var action string
	var reason *string
	var processedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason, processed_at FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason, &processedAt); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn", action)
	}
	if processedAt == nil {
		t.Errorf("processed_at not set")
	}
	if reason == nil || *reason != "spawned" {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want %q", got, "spawned")
	}
}

func TestWorker_ActiveRun_QueuesPending(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetrigger(t, pool, issueID, "https://github.com/o/r/pull/2", "sha-q", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	sp.hasActive.Store(true) // active run on this issue

	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn when run active, got %d", sp.spawnCalls.Load())
	}

	// Pending row must exist for this issue.
	var pendingEID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT event_id FROM cascade_pending_event WHERE issue_id = $1`, issueID).Scan(&pendingEID); err != nil {
		t.Fatalf("expected pending row: %v", err)
	}

	// Action on the row must be 'queued_pending' with the PUL-198
	// reason describing why the spawn was deferred.
	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read action: %v", err)
	}
	if action != "queued_pending" {
		t.Errorf("action = %q, want queued_pending", action)
	}
	if reason == nil || *reason != "queued_pending: active run present" {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want %q", got, "queued_pending: active run present")
	}
}

func TestWorker_LoopGuard_TripsAfterThreshold(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	prURL := "https://github.com/o/r/pull/9999"
	// Pre-seed 3 distinct-head_sha 'spawn' rows in the 6h window.
	for i, sha := range []string{"a", "b", "c"} {
		_, err := pool.Exec(context.Background(), `
            INSERT INTO cascade_retrigger (event_id, issue_id, pr_url, pr_number, head_sha, event_type, action, processed_at)
            VALUES ($1, $2, $3, $4, $5, 'pr_title_edit', 'spawn', now() - interval '1 hour')`,
			uuid.New(), issueID, prURL, i+1, sha)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 4th retrigger — should trip the guard.
	rowID := insertRetrigger(t, pool, issueID, prURL, "d", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE pr_url = $1`, prURL)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn after loop guard, got %d", sp.spawnCalls.Load())
	}

	var state string
	if err := pool.QueryRow(context.Background(),
		`SELECT cascade_state FROM issue WHERE id = $1`, issueID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "loop_guarded" {
		t.Errorf("cascade_state = %q, want loop_guarded", state)
	}

	var action string
	var reason *string
	_ = pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason)
	if action != "loop_guard_skip" {
		t.Errorf("action = %q, want loop_guard_skip", action)
	}
	// PUL-198: action_reason carries the loop-guard count + window. The
	// pre-seeded 3 distinct head_shas trip the guard; the 4th retrigger
	// is the one being marked.
	if reason == nil || !strings.Contains(*reason, "loop_guard: 3 distinct head_shas") {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want loop_guard count description", got)
	}
}

func TestWorker_SpawnFailureLeavesRowUnprocessed(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetrigger(t, pool, issueID, "https://github.com/o/r/pull/3", "sha-fail", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{spawnErr: errors.New("spawn boom")}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Errorf("expected spawn attempt, got %d", sp.spawnCalls.Load())
	}

	var action *string
	var processedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT action, processed_at FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &processedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if processedAt != nil {
		t.Errorf("expected processed_at NULL after spawn failure, got %v", processedAt)
	}
	if action != nil {
		t.Errorf("expected action NULL after spawn failure, got %q", *action)
	}
}

func TestWorker_SpawnGatedMarksRowSkipped(t *testing.T) {
	// Companion to TestWorker_SpawnFailureLeavesRowUnprocessed:
	// when the Spawner returns ErrSpawnGated (deterministic refusal
	// like "issue has no assignee"), the worker must mark the row
	// processed with scope_filter_skip so the same event does not
	// loop the queue forever. The operator's fix lands on the NEXT
	// webhook delivery, not by replaying this row.
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetrigger(t, pool, issueID, "https://github.com/o/r/pull/4", "sha-gated", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{spawnErr: fmt.Errorf("no assignee: %w", ErrSpawnGated)}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Errorf("expected one spawn attempt, got %d", sp.spawnCalls.Load())
	}

	var action *string
	var processedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT action, processed_at FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &processedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if processedAt == nil {
		t.Error("expected processed_at set after spawn gated, got NULL")
	}
	if action == nil || *action != "scope_filter_skip" {
		got := "<nil>"
		if action != nil {
			got = *action
		}
		t.Errorf("action = %q, want scope_filter_skip", got)
	}

	// PUL-198: gated reason must wrap the Spawner's error text so the
	// operator sees "no assignee" / "agent archived" / etc. on the
	// audit line.
	var reason *string
	_ = pool.QueryRow(context.Background(),
		`SELECT action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&reason)
	if reason == nil || !strings.Contains(*reason, "no assignee") {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want to contain spawner error text", got)
	}
}

func TestWorker_NoIssueIDIsScopeSkip(t *testing.T) {
	// Per the worker's contract, rows with issue_id NULL are
	// scope-skipped (a follow-up will backfill them when title +
	// branch land on the row schema). This pins that behavior so a
	// future change doesn't silently spawn against NULL issues.
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	_ = ws

	var rowID int64
	if err := pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, pr_url, pr_number, head_sha, event_type)
        VALUES ($1, 'u', 1, 's', 'pr_title_edit')
        RETURNING id`, uuid.New()).Scan(&rowID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn for NULL issue_id, got %d", sp.spawnCalls.Load())
	}
	var action string
	var reason *string
	_ = pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason)
	if action != "scope_filter_skip" {
		t.Errorf("action = %q, want scope_filter_skip", action)
	}
	// PUL-198: nil-loader path records its own scope_filter reason
	// (distinct from the no-identifier and issue-not-found branches).
	if reason == nil || !strings.Contains(*reason, "no IssueLoader") {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want to mention IssueLoader", got)
	}
}

func TestWorker_DrainPending_SpawnsWhenPending(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	// Seed a pending event manually (in production the worker writes
	// this via queuePending; here we go straight to SQL to keep the
	// test focused on the drain side).
	eid := uuid.New()
	tcJSON := `{"event_id":"` + eid.String() + `","event_type":"pr_merged","pr_url":"https://x/y/pull/9","pr_number":9,"head_sha":"merged-sha"}`

	// queuePending needs a cascade_retrigger row to reference (FK).
	var retrigID int64
	_ = pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, issue_id, pr_url, pr_number, head_sha, event_type)
        VALUES ($1, $2, 'u', 9, 's', 'pr_merged') RETURNING id`,
		eid, issueID).Scan(&retrigID)
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, retrigID)

	_, err := pool.Exec(context.Background(),
		`INSERT INTO cascade_pending_event (issue_id, event_id, trigger_context) VALUES ($1, $2, $3::jsonb)`,
		issueID, eid, tcJSON)
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.DrainPending(context.Background(), issueID)

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected drain spawn, got %d", sp.spawnCalls.Load())
	}
	if sp.lastCtx.EventID != eid {
		t.Errorf("trigger context not propagated: got %v, want %v", sp.lastCtx.EventID, eid)
	}

	// Pending row must be gone.
	var n int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM cascade_pending_event WHERE issue_id = $1`, issueID).Scan(&n)
	if n != 0 {
		t.Errorf("pending row not deleted: count = %d", n)
	}
}

func TestWorker_DrainPending_NoPendingIsQuiet(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil)
	w.DrainPending(context.Background(), issueID)
	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn when no pending, got %d", sp.spawnCalls.Load())
	}
}

// fakeLoader records the identifier the worker resolved and returns
// the canned response. ErrIssueNotFound surfaces a real
// scope_filter_skip; any other error tests the retry behavior.
type fakeLoader struct {
	want  string
	resp  uuid.UUID
	err   error
	calls int
}

func (f *fakeLoader) LookupByIdentifier(_ context.Context, id string) (uuid.UUID, error) {
	f.calls++
	f.want = id
	return f.resp, f.err
}

// insertRetriggerWithLookup seeds a row with the new pr_title +
// branch columns populated; designed to exercise the
// PollOnce → resolveIssue → loader → spawn path.
func insertRetriggerWithLookup(t *testing.T, pool *pgxpool.Pool, prTitle, branch, prURL, headSHA, eventType string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, pr_url, pr_number, pr_title, head_sha, branch, event_type)
        VALUES ($1, $2, 1, NULLIF($3, ''), $4, NULLIF($5, ''), $6)
        RETURNING id`,
		uuid.New(), prURL, prTitle, headSHA, branch, eventType,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert retrigger: %v", err)
	}
	return id
}

func TestWorker_ResolveIssue_PRTitleMatchTriggersLookup(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetriggerWithLookup(t, pool,
		"[PUL-9001] feat: x", "some/feat", "https://github.com/o/r/pull/1", "lookup-sha", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	loader := &fakeLoader{resp: issueID}
	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, loader, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if loader.calls != 1 || loader.want != "PUL-9001" {
		t.Fatalf("loader not called with PUL-9001: calls=%d, want=%q", loader.calls, loader.want)
	}
	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected spawn after lookup, got %d", sp.spawnCalls.Load())
	}

	// Row should now have issue_id set + action='spawn'.
	var issueValid bool
	var action string
	_ = pool.QueryRow(context.Background(),
		`SELECT issue_id IS NOT NULL, action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&issueValid, &action)
	if !issueValid {
		t.Errorf("issue_id not persisted after lookup")
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn", action)
	}
}

func TestWorker_ResolveIssue_BranchFallbackTriggersLookup(t *testing.T) {
	// Title doesn't carry [PUL-N] but the branch does — G4 fallback.
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetriggerWithLookup(t, pool,
		"title was edited", "agent-1/PUL-7777-foo", "u", "branch-sha", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	loader := &fakeLoader{resp: issueID}
	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, loader, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if loader.calls != 1 || loader.want != "PUL-7777" {
		t.Fatalf("branch fallback lookup mismatch: calls=%d, want=%q", loader.calls, loader.want)
	}
	if sp.spawnCalls.Load() != 1 {
		t.Errorf("expected spawn via branch fallback, got %d", sp.spawnCalls.Load())
	}
}

func TestWorker_ResolveIssue_IssueNotFoundIsScopeSkip(t *testing.T) {
	pool, _, _, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetriggerWithLookup(t, pool,
		"[PUL-1] x", "agent-1/pul-1-x", "u", "s", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	loader := &fakeLoader{err: ErrIssueNotFound}
	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, loader, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn when issue not found, got %d", sp.spawnCalls.Load())
	}
	var action string
	_ = pool.QueryRow(context.Background(),
		`SELECT action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action)
	if action != "scope_filter_skip" {
		t.Errorf("action = %q, want scope_filter_skip", action)
	}
}

func TestWorker_ResolveIssue_LoaderRetryableErrorLeavesRow(t *testing.T) {
	// A non-ErrIssueNotFound error (DB hiccup, etc.) must leave the
	// row unprocessed so the next tick retries — distinct from
	// scope-skip which is a terminal mark.
	pool, _, _, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetriggerWithLookup(t, pool,
		"[PUL-1] x", "", "u", "s", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	loader := &fakeLoader{err: errors.New("transient db error")}
	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, loader, nil, nil, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn on transient error, got %d", sp.spawnCalls.Load())
	}
	var processedAt *time.Time
	var action *string
	_ = pool.QueryRow(context.Background(),
		`SELECT action, processed_at FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &processedAt)
	if processedAt != nil {
		t.Errorf("expected processed_at NULL for retry, got %v", processedAt)
	}
	if action != nil {
		t.Errorf("expected action NULL for retry, got %q", *action)
	}
}

func TestWorker_NilLoader_LegacyScopeSkip(t *testing.T) {
	// Nil loader = pre-wiring deployment. Worker must still scope-
	// skip rows with NULL issue_id (the pre-FU1 behavior); a row
	// WITHOUT issue_id never reaches the loader.
	pool, _, _, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	rowID := insertRetriggerWithLookup(t, pool,
		"[PUL-1] x", "agent-1/pul-1-x", "u", "s", "pr_title_edit")
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil) // nil loader
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn with nil loader, got %d", sp.spawnCalls.Load())
	}
	var action string
	_ = pool.QueryRow(context.Background(),
		`SELECT action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action)
	if action != "scope_filter_skip" {
		t.Errorf("action = %q, want scope_filter_skip", action)
	}
}

// TestWorker_AutoFlipsToDeployedOnPRMerged is the regression guard
// against PUL-194's "hook after spawn" trap. With txPool + queries
// wired, an `event_type='pr_merged'` event MUST flip status to
// `deployed` *even when* the spawn pipeline short-circuits via the
// active-concurrent-run branch (queue_pending → return). Status
// reflects "PR is in main", which is independent of whether the
// agent wakes up.
//
// This test is intentionally separate from the deploy_flip_test.go
// unit tests of ApplyDeployFlip — it asserts the *worker* wires the
// call in the right place in processOne.
//
// PUL-198 Part 2 note: this scenario uses cascade_plan_url (not
// cascade_state='approved') so the worker's new opt-in gate passes
// AND DecideDeployFlip still flips. cascade_state='approved' would
// pass the gate but block the flip — DecideDeployFlip rejects the
// non-terminal cascade states intentionally to preserve multi-PR
// semantics where the agent drives the final transition.
func TestWorker_AutoFlipsToDeployedOnPRMerged(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	// Cascade-eligible via cascade_plan_url (cascade_state stays NULL
	// so DecideDeployFlip flips). The plan URL is the PUL-198 Part 2
	// opt-in primitive that lets self-published plans wake the agent.
	issueID := insertIssueWithPlanURL(t, pool, ws, 19420, "todo",
		"https://github.com/rabbeet/plans/blob/main/Multica/test.md")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/100", "sha-merged", "pr_merged")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)
	defer pool.Exec(context.Background(),
		`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_pending_event WHERE issue_id = $1`, issueID)

	sp := &fakeSpawner{}
	sp.hasActive.Store(true) // force queue_pending → return path

	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	// Pipeline-side: queue_pending was the path taken.
	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn while active run held, got %d", sp.spawnCalls.Load())
	}
	var action string
	if err := pool.QueryRow(context.Background(),
		`SELECT action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action); err != nil {
		t.Fatalf("read action: %v", err)
	}
	if action != "queued_pending" {
		t.Errorf("action = %q, want queued_pending", action)
	}

	// Status side: flip MUST have happened despite the queue path.
	var status string
	var deployedAt *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, deployed_at::text FROM issue WHERE id = $1`, issueID,
	).Scan(&status, &deployedAt); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if status != "deployed" {
		t.Errorf("status = %q, want deployed — flip is wired AFTER spawn instead of BEFORE", status)
	}
	if deployedAt == nil {
		t.Errorf("deployed_at not stamped")
	}

	// History row should exist with source=hook_pr_merged.
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_status_history
		 WHERE issue_id = $1 AND source = $2`,
		issueID, SourceHookPRMerged,
	).Scan(&count); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if count != 1 {
		t.Errorf("history rows = %d, want 1", count)
	}
}

// TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix is the
// PUL-198 prefix guard: when a pr_merged event flips status AND also
// spawns, the cascade_retrigger row's action_reason must encode BOTH
// outcomes on one line so the operator sees them through a single
// `multica issue cascade` row. The flip itself is asserted by
// TestWorker_AutoFlipsToDeployedOnPRMerged — this one pins the audit
// prefix specifically.
func TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	// Cascade-eligible via cascade_plan_url (cascade_state NULL so
	// DecideDeployFlip flips) — see TestWorker_AutoFlipsToDeployedOnPRMerged
	// for the rationale; cascade_state='approved' would pass the gate
	// but make the flip a no-op (multi-PR semantics).
	issueID := insertIssueWithPlanURL(t, pool, ws, 19822, "todo",
		"https://github.com/rabbeet/plans/blob/main/Multica/test.md")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/198", "sha-pul198", "pr_merged")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)
	defer pool.Exec(context.Background(),
		`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)

	sp := &fakeSpawner{} // no active run → spawn path
	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected 1 spawn, got %d", sp.spawnCalls.Load())
	}

	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn", action)
	}
	got := ""
	if reason != nil {
		got = *reason
	}
	// Prefix shape is "deploy_flip=flipped status_to=deployed; spawned"
	// — keep both halves visible to the operator.
	if !strings.HasPrefix(got, "deploy_flip=flipped") {
		t.Errorf("action_reason = %q, want to start with deploy_flip=flipped", got)
	}
	if !strings.Contains(got, "spawned") {
		t.Errorf("action_reason = %q, want to contain spawn outcome", got)
	}
}

// TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix_Noop is the
// PUL-202 sibling for the deploy_flip=noop; branch: when DecideDeployFlip
// rejects (issue already in a terminal status), ApplyDeployFlip returns
// (false, nil) and processOne emits a "deploy_flip=noop; " prefix. The
// happy-path sibling (:765) pins flipped; this one pins noop.
func TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix_Noop(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	// status='deployed' makes DecideDeployFlip return false → noop path.
	// plan_url opts the issue into the PUL-198 cascade-gate so spawn fires.
	issueID := insertIssueWithPlanURL(t, pool, ws, 20211, "deployed",
		"https://github.com/rabbeet/plans/blob/main/Multica/test.md")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/202", "sha-pul202-noop", "pr_merged")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)
	defer pool.Exec(context.Background(),
		`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected 1 spawn, got %d", sp.spawnCalls.Load())
	}

	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn", action)
	}
	got := ""
	if reason != nil {
		got = *reason
	}
	if !strings.HasPrefix(got, "deploy_flip=noop") {
		t.Errorf("action_reason = %q, want to start with deploy_flip=noop", got)
	}
	if !strings.Contains(got, "spawned") {
		t.Errorf("action_reason = %q, want to contain spawn outcome", got)
	}
}

// TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix_Failed is the
// PUL-202 sibling for the deploy_flip=failed: <err>; branch. Failure is
// injected by passing a separate, pre-Closed pgxpool as the worker's
// txPool: ApplyDeployFlip's first call is pool.Begin(ctx), which fails
// on a closed pool — exercising the err != nil branch in processOne
// without touching production code. The live `pool` is still used for
// cascade_retrigger reads/writes, so the spawn fall-through path stays
// observable. The exact pgx error string is implementation-defined and
// must NOT be asserted; only the "deploy_flip=failed:" prefix is
// contract.
func TestWorker_PRMergedActionReasonCarriesDeployFlipPrefix_Failed(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	// status='todo' would normally flip; the closed txPool forces
	// ApplyDeployFlip to error before DecideDeployFlip even runs.
	// plan_url opts the issue into the PUL-198 cascade-gate so spawn fires.
	issueID := insertIssueWithPlanURL(t, pool, ws, 20212, "todo",
		"https://github.com/rabbeet/plans/blob/main/Multica/test.md")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/203", "sha-pul202-failed", "pr_merged")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)
	defer pool.Exec(context.Background(),
		`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)

	closedFlipPool := openAndClosePool(t)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, closedFlipPool, queries, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected 1 spawn (best-effort fall-through after flip failure), got %d", sp.spawnCalls.Load())
	}

	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn (flip failure must NOT block spawn pipeline)", action)
	}
	got := ""
	if reason != nil {
		got = *reason
	}
	if !strings.HasPrefix(got, "deploy_flip=failed:") {
		t.Errorf("action_reason = %q, want to start with deploy_flip=failed:", got)
	}
	if !strings.Contains(got, "spawned") {
		t.Errorf("action_reason = %q, want to contain spawn outcome", got)
	}
}

// openAndClosePool returns a pgxpool that is open against the same DSN
// as workerTestDBPool but has already been Closed. Used to inject a
// deterministic pool.Begin error into ApplyDeployFlip for the
// deploy_flip=failed test branch.
func openAndClosePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("open flip pool: %v", err)
	}
	p.Close()
	return p
}

// TestWorker_NonPRMergedEventDoesNotFlip locks in the negative side:
// only event_type='pr_merged' triggers the auto-flip. pr_title_edit
// (still in the CHECK constraint as a Deprecated value after PUL-212)
// continues to spawn-without-flip. Legacy ci_failure /
// pr_review_change rows go through the PUL-212 CQ2 short-circuit
// (see TestWorker_LegacyCIFailureRow_ScopeFilterSkips below) and
// also never flip; this test pins the eventType != pr_merged branch
// in ApplyDeployFlip's outer guard which is the orthogonal defense.
func TestWorker_NonPRMergedEventDoesNotFlip(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	issueID := insertIssueForDeployFlip(t, pool, ws, 19421, "in_progress", "")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/101", "sha-ci", "pr_title_edit")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == "deployed" {
		t.Errorf("pr_title_edit event flipped status to deployed (must only fire on pr_merged)")
	}

	// PUL-202 regression-guard: non-pr_merged events must never leak a
	// "deploy_flip=" prefix into action_reason. A future edit that moves
	// the flipPrefix-init block outside the `if eventType == pr_merged`
	// branch would silently break the audit contract; this assert pins it.
	var reasonGot *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&reasonGot); err != nil {
		t.Fatalf("read action_reason: %v", err)
	}
	if reasonGot != nil && strings.Contains(*reasonGot, "deploy_flip=") {
		t.Errorf("non-pr_merged event leaked deploy_flip= prefix: %q", *reasonGot)
	}
}

// insertIssueWithPlanURL inserts a single-PR-style issue (cascade_state
// NULL) that opts in to cascade behaviour via cascade_plan_url. Mirrors
// insertIssueForDeployFlip but covers the PUL-198 Part 2 primitive.
func insertIssueWithPlanURL(
	t *testing.T,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
	number int,
	status, planURL string,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var creatorID string
	if err := pool.QueryRow(ctx,
		`SELECT user_id::text FROM member WHERE workspace_id = $1 LIMIT 1`,
		workspaceID,
	).Scan(&creatorID); err != nil {
		t.Fatalf("lookup owner: %v", err)
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number,
		                   cascade_plan_url)
		VALUES ($1, $2, $3, 'member', $4, $5, $6)
		RETURNING id`,
		workspaceID, "plan-url test "+status, status, creatorID, number, planURL,
	).Scan(&id); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	u, _ := uuid.Parse(id)
	return u
}

// TestWorker_PRMergedSinglePR_FlipsButNoSpawn pins the PUL-198 Part 2
// contract for issues that did NOT opt in to cascade: deploy-flip MUST
// still run (status → 'deployed', PUL-194 invariant), but the worker
// MUST NOT spawn. The cascade_retrigger row terminates as
// action='single_pr_no_spawn' with the deploy_flip prefix carried in
// action_reason so the audit row tells both halves of the story.
func TestWorker_PRMergedSinglePR_FlipsButNoSpawn(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	// No cascade_state, no cascade_plan_url — the canonical single-PR
	// issue. status='todo' so DecideDeployFlip eligibly flips.
	issueID := insertIssueForDeployFlip(t, pool, ws, 19830, "todo", "")
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/198", "sha-no-cascade", "pr_merged")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)
	defer pool.Exec(context.Background(),
		`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("expected no spawn for non-cascade issue, got %d", sp.spawnCalls.Load())
	}

	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "deployed" {
		t.Errorf("status = %q, want deployed — deploy-flip must run even when gate blocks spawn", status)
	}

	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if action != "single_pr_no_spawn" {
		t.Errorf("action = %q, want single_pr_no_spawn", action)
	}
	got := ""
	if reason != nil {
		got = *reason
	}
	if !strings.HasPrefix(got, "deploy_flip=flipped") {
		t.Errorf("action_reason = %q, want to start with deploy_flip=flipped (both halves on one line)", got)
	}
	if !strings.Contains(got, "single_pr_no_spawn") {
		t.Errorf("action_reason = %q, want to contain single_pr_no_spawn", got)
	}
}

// TestWorker_CascadePlanURL_SpawnsAndPropagatesURL is the positive
// PUL-198 Part 2 test: an issue with cascade_plan_url set (but no
// cascade_state) is a self-published cascade. Worker spawns, and the
// TriggerContext carries the plan URL so the woken agent can locate
// the governing plan without re-deriving it.
func TestWorker_CascadePlanURL_SpawnsAndPropagatesURL(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	planURL := "https://github.com/rabbeet/plans/blob/main/Multica/2026-05-19-pul-198-pr2-cascade-plan-url.md"
	issueID := insertIssueWithPlanURL(t, pool, ws, 19831, "in_progress", planURL)
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/199", "sha-plan", "pr_title_edit")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, pool, queries, nil, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected 1 spawn (cascade_plan_url opt-in), got %d", sp.spawnCalls.Load())
	}
	if sp.lastCtx.CascadePlanURL != planURL {
		t.Errorf("TriggerContext.CascadePlanURL = %q, want %q", sp.lastCtx.CascadePlanURL, planURL)
	}

	var action string
	if err := pool.QueryRow(context.Background(),
		`SELECT action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action); err != nil {
		t.Fatalf("read action: %v", err)
	}
	if action != "spawn" {
		t.Errorf("action = %q, want spawn", action)
	}
}

// TestWorker_NoGate_WhenQueriesNil pins the partial-wiring fall-through:
// when queries is nil (deploy-flip surface not wired — e.g. unit tests
// that don't supply it, or a startup where the wiring is incomplete)
// the PUL-198 Part 2 gate is skipped and the legacy spawn-always
// behaviour holds. Without this, an incomplete startup would silently
// drop every cascade event as single_pr_no_spawn — a regression the
// nil-loader and nil-txPool branches both guard against.
func TestWorker_NoGate_WhenQueriesNil(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	// Even though setupWorkerTest sets cascade_state='approved', the
	// gate doesn't run because queries=nil, so the test would pass
	// regardless. The point is: no single_pr_no_spawn action appears
	// when the gate is unwired.
	rowID := insertRetrigger(t, pool, issueID,
		"https://github.com/o/r/pull/500", "sha-nogate", "pr_title_edit")
	defer pool.Exec(context.Background(),
		`DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	w := NewWorker(pool, sp, nil, nil, nil, nil, nil) // queries=nil
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 1 {
		t.Fatalf("expected spawn under nil-queries fall-through, got %d", sp.spawnCalls.Load())
	}

	var action string
	if err := pool.QueryRow(context.Background(),
		`SELECT action FROM cascade_retrigger WHERE id = $1`, rowID).Scan(&action); err != nil {
		t.Fatalf("read action: %v", err)
	}
	if action == "single_pr_no_spawn" {
		t.Errorf("action = %q, must NOT be single_pr_no_spawn when gate is unwired", action)
	}
}

// TestWorker_LegacyCIFailureRow_ScopeFilterSkips is the PUL-212 CQ2
// regression: a cascade_retrigger row with event_type='ci_failure'
// (left over from before this PR's classifier change, not yet drained
// by migration 087) must short-circuit in processOne to
// scope_filter_skip with action_reason='legacy_event_dropped:ci_failure',
// without calling Spawn or ApplyDeployFlip. This closes the
// deploy → migration race window: rabbeet/Pulse's pr-test-autofix.yml
// is the canonical handler for CI failures; cascade waking the agent
// in the multica issue at the same time = double-Claude conflict.
//
// The test uses an issue with cascade_state='approved' (opt-in
// cascade) to exercise the worst case: without CQ2, that issue would
// have been spawn'd by the worker after the classifier was already
// gone.
func TestWorker_LegacyCIFailureRow_ScopeFilterSkips(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	// Direct INSERT mimics a row left over from the pre-PUL-212
	// classifier. cascade_state on the issue is 'approved' (set by
	// setupWorkerTest) so the spawn-eligibility gate would fire if
	// CQ2 weren't there.
	var rowID int64
	if err := pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, issue_id, pr_url, pr_number, head_sha, event_type)
        VALUES ($1, $2, $3, 99, $4, 'ci_failure')
        RETURNING id`,
		uuid.New(), issueID, "https://github.com/o/r/pull/212", "sha-cq2-ci",
	).Scan(&rowID); err != nil {
		t.Fatalf("insert legacy ci_failure row: %v", err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	metrics := NewMetrics()
	w := NewWorker(pool, sp, nil, nil, nil, metrics, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("CQ2 guard must not spawn for legacy ci_failure row, got %d spawn(s)", sp.spawnCalls.Load())
	}

	var action string
	var reason *string
	var processedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason, processed_at FROM cascade_retrigger WHERE id = $1`,
		rowID).Scan(&action, &reason, &processedAt); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if action != "scope_filter_skip" {
		t.Errorf("action = %q, want scope_filter_skip", action)
	}
	if processedAt == nil {
		t.Errorf("processed_at not set — CQ2 must mark the row processed")
	}
	wantReason := "legacy_event_dropped:ci_failure"
	if reason == nil || *reason != wantReason {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want %q", got, wantReason)
	}
	// PUL-220: assert the Prometheus counter ticked. Without this
	// the wiring could silently break (counter stays 0 in prod) and
	// the metric we built to gate PUL-217 would lie about drain
	// completion.
	if got := metrics.Snapshot().LegacyEventDropped["ci_failure"]; got != 1 {
		t.Errorf("metrics.LegacyEventDropped[ci_failure] = %d, want 1", got)
	}
}

// TestWorker_LegacyPRReviewChangeRow_ScopeFilterSkips mirrors the
// ci_failure CQ2 test for the other dropped event type. Same
// rationale: rabbeet/Pulse's code-review-fix.yml owns
// changes_requested review fixes inside the PR; cascade waking the
// agent in the multica issue at the same time = double-Claude
// conflict. The CQ2 guard short-circuits before the issue's
// cascade_state='approved' opt-in would have caused a spawn.
func TestWorker_LegacyPRReviewChangeRow_ScopeFilterSkips(t *testing.T) {
	pool, _, issueID, cleanup := setupWorkerTest(t)
	if pool == nil {
		return
	}
	defer cleanup()

	var rowID int64
	if err := pool.QueryRow(context.Background(), `
        INSERT INTO cascade_retrigger (event_id, issue_id, pr_url, pr_number, head_sha, event_type)
        VALUES ($1, $2, $3, 99, $4, 'pr_review_change')
        RETURNING id`,
		uuid.New(), issueID, "https://github.com/o/r/pull/213", "sha-cq2-rev",
	).Scan(&rowID); err != nil {
		t.Fatalf("insert legacy pr_review_change row: %v", err)
	}
	defer pool.Exec(context.Background(), `DELETE FROM cascade_retrigger WHERE id = $1`, rowID)

	sp := &fakeSpawner{}
	metrics := NewMetrics()
	w := NewWorker(pool, sp, nil, nil, nil, metrics, nil)
	w.PollOnce(context.Background())

	if sp.spawnCalls.Load() != 0 {
		t.Errorf("CQ2 guard must not spawn for legacy pr_review_change row, got %d spawn(s)", sp.spawnCalls.Load())
	}

	var action string
	var reason *string
	if err := pool.QueryRow(context.Background(),
		`SELECT action, action_reason FROM cascade_retrigger WHERE id = $1`,
		rowID).Scan(&action, &reason); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if action != "scope_filter_skip" {
		t.Errorf("action = %q, want scope_filter_skip", action)
	}
	wantReason := "legacy_event_dropped:pr_review_change"
	if reason == nil || *reason != wantReason {
		got := "<nil>"
		if reason != nil {
			got = *reason
		}
		t.Errorf("action_reason = %q, want %q", got, wantReason)
	}
	// PUL-220: assert the per-event-type counter tick. Mirrors the
	// ci_failure check above; same rationale.
	if got := metrics.Snapshot().LegacyEventDropped["pr_review_change"]; got != 1 {
		t.Errorf("metrics.LegacyEventDropped[pr_review_change] = %d, want 1", got)
	}
}
