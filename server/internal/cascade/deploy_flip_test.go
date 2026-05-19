package cascade

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// insertIssueForDeployFlip inserts an issue with the given starting status
// and an explicit cascade_state (empty string → NULL). Distinct from
// workerInsertCascadeIssue, which forces a non-NULL cascade_state and a
// fixed 'in_progress' starting status — both wrong for the single-PR /
// terminal-status cases ApplyDeployFlip needs to cover.
func insertIssueForDeployFlip(
	t *testing.T,
	pool *pgxpool.Pool,
	workspaceID uuid.UUID,
	number int,
	status string,
	cascadeState string,
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
	var cs any
	if cascadeState != "" {
		cs = cascadeState
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, creator_type, creator_id, number,
		                   cascade_state, cascade_started_at)
		VALUES ($1, $2, $3, 'member', $4, $5, $6,
		        CASE WHEN $6::text IS NULL THEN NULL ELSE now() - interval '5 minutes' END)
		RETURNING id`,
		workspaceID, "deploy-flip test "+status, status, creatorID, number, cs,
	).Scan(&id); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	u, _ := uuid.Parse(id)
	return u
}

// TestDecideDeployFlip covers the pure-decision layer. No I/O.
// Goal: lock in the eligibility matrix from the plan ("Validation"
// in 2026-05-19-pul-194-cascade-deploy-status-flip.md) so a future
// edit cannot regress the cascade-vs-non-cascade or terminal-status
// branches without breaking this table.
func TestDecideDeployFlip(t *testing.T) {
	type row struct {
		name         string
		status       string
		cascadeState pgtype.Text
		want         bool
	}
	cases := []row{
		{"single_pr_todo_flips", "todo", pgtype.Text{}, true},
		{"single_pr_in_progress_flips", "in_progress", pgtype.Text{}, true},
		{"single_pr_in_review_flips", "in_review", pgtype.Text{}, true},
		{"single_pr_waiting_flips", "waiting", pgtype.Text{}, true},
		{"single_pr_developing_flips", "developing", pgtype.Text{}, true},
		{"cascade_completed_flips_defensively", "in_progress", pgtype.Text{String: "completed", Valid: true}, true},
		{"cascade_approved_intermediate_no_flip", "in_progress", pgtype.Text{String: "approved", Valid: true}, false},
		{"cascade_paused_no_flip", "in_progress", pgtype.Text{String: "paused", Valid: true}, false},
		{"cascade_loop_guarded_no_flip", "in_progress", pgtype.Text{String: "loop_guarded", Valid: true}, false},
		{"already_deployed_idempotent", "deployed", pgtype.Text{}, false},
		{"cancelled_never_flips", "cancelled", pgtype.Text{}, false},
		{"cancelled_with_completed_cascade_still_no", "cancelled", pgtype.Text{String: "completed", Valid: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideDeployFlip(c.status, c.cascadeState)
			if got != c.want {
				t.Errorf("DecideDeployFlip(%q, %+v) = %v, want %v", c.status, c.cascadeState, got, c.want)
			}
		})
	}
}

// TestApplyDeployFlip_HappyPath covers the canonical single-PR case
// (cascade_state IS NULL): issue moves todo → deployed, deployed_at
// stamped, history row written, fan-out enqueued when parent exists.
//
// This is the regression scenario PUL-194 fixed — without it,
// PUL-81/107-style merges left their multica tickets stuck in
// todo/in_progress.
func TestApplyDeployFlip_HappyPath(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	issueID := insertIssueForDeployFlip(t, pool, ws, 19401, "todo", "")
	eventID := uuid.New()
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)
	}()

	flipped, err := ApplyDeployFlip(context.Background(), pool, queries, issueID, eventID)
	if err != nil {
		t.Fatalf("ApplyDeployFlip: %v", err)
	}
	if !flipped {
		t.Fatalf("expected flipped=true")
	}

	// Status, deployed_at, updated_at all moved.
	var status string
	var deployedAt *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, deployed_at::text FROM issue WHERE id = $1`, issueID).
		Scan(&status, &deployedAt); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if status != "deployed" {
		t.Errorf("status = %q, want deployed", status)
	}
	if deployedAt == nil {
		t.Errorf("deployed_at not stamped")
	}

	// History row with source=hook_pr_merged, ref_id=eventID.
	var fromStatus, toStatus, source, refID, actorType string
	if err := pool.QueryRow(context.Background(),
		`SELECT from_status, to_status, source, ref_id, actor_type
		 FROM issue_status_history
		 WHERE issue_id = $1 AND source = $2`,
		issueID, SourceHookPRMerged,
	).Scan(&fromStatus, &toStatus, &source, &refID, &actorType); err != nil {
		t.Fatalf("read history: %v", err)
	}
	if toStatus != "deployed" || source != SourceHookPRMerged ||
		refID != eventID.String() || actorType != "system" {
		t.Errorf("history mismatch: from=%q to=%q source=%q ref=%q actor=%q",
			fromStatus, toStatus, source, refID, actorType)
	}
}

// TestApplyDeployFlip_Idempotent verifies the UNIQUE (source, ref_id)
// contract: replaying the same event yields no second history row and
// returns flipped=false the second time (the issue is already at the
// terminal status).
func TestApplyDeployFlip_Idempotent(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	issueID := insertIssueForDeployFlip(t, pool, ws, 19402, "todo", "")
	eventID := uuid.New()
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM issue_status_history WHERE issue_id = $1`, issueID)
	}()

	first, err := ApplyDeployFlip(context.Background(), pool, queries, issueID, eventID)
	if err != nil || !first {
		t.Fatalf("first ApplyDeployFlip: flipped=%v err=%v", first, err)
	}
	second, err := ApplyDeployFlip(context.Background(), pool, queries, issueID, eventID)
	if err != nil {
		t.Fatalf("second ApplyDeployFlip err: %v", err)
	}
	if second {
		t.Errorf("second ApplyDeployFlip: flipped=true, want false (already deployed)")
	}
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_status_history
		 WHERE issue_id = $1 AND source = $2`,
		issueID, SourceHookPRMerged,
	).Scan(&count); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if count != 1 {
		t.Errorf("history row count = %d, want 1", count)
	}
}

// TestApplyDeployFlip_IntermediateCascadeMergeNoFlip ensures the
// multi-PR cascade semantics: when cascade_state='approved' (active
// cascade, intermediate PR merged), we do NOT flip. The agent stays
// in charge through the per-PR template.
func TestApplyDeployFlip_IntermediateCascadeMergeNoFlip(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	issueID := insertIssueForDeployFlip(t, pool, ws, 19403, "in_progress", "approved")
	eventID := uuid.New()

	flipped, err := ApplyDeployFlip(context.Background(), pool, queries, issueID, eventID)
	if err != nil {
		t.Fatalf("ApplyDeployFlip: %v", err)
	}
	if flipped {
		t.Errorf("flipped=true on intermediate cascade merge — must remain agent-driven")
	}
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status == "deployed" {
		t.Errorf("intermediate cascade merge flipped status to deployed")
	}
}

// TestApplyDeployFlip_CancelledStaysCancelled covers the terminal-state
// branch: a cancelled issue never auto-flips, even if its PR somehow
// gets merged after cancellation.
func TestApplyDeployFlip_CancelledStaysCancelled(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	issueID := insertIssueForDeployFlip(t, pool, ws, 19404, "cancelled", "")
	eventID := uuid.New()

	flipped, err := ApplyDeployFlip(context.Background(), pool, queries, issueID, eventID)
	if err != nil {
		t.Fatalf("ApplyDeployFlip: %v", err)
	}
	if flipped {
		t.Errorf("flipped=true on cancelled issue")
	}
}

// TestApplyDeployFlip_ChildProgressFanoutWhenParentExists is the
// regression guard for the missed PUL-164 contract caught by
// /plan-eng-review: server-side flips must enqueue
// issue_child_progress_outbox so parents are notified, mirroring
// every other status-mutation path (handler.UpdateIssue,
// service.CommentService.Create, TaskService failed-task reset).
func TestApplyDeployFlip_ChildProgressFanoutWhenParentExists(t *testing.T) {
	pool := workerTestDBPool(t)
	if pool == nil {
		return
	}
	ws, cleanup := workerMakeWorkspaceAndUser(t, pool)
	defer cleanup()
	queries := db.New(pool)

	parentID := insertIssueForDeployFlip(t, pool, ws, 19410, "in_progress", "")
	childID := insertIssueForDeployFlip(t, pool, ws, 19411, "todo", "")
	if _, err := pool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id=$1 WHERE id=$2`, parentID, childID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM issue_child_progress_outbox WHERE child_issue_id = $1`, childID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM issue_status_history WHERE issue_id = $1`, childID)
	}()
	eventID := uuid.New()

	flipped, err := ApplyDeployFlip(context.Background(), pool, queries, childID, eventID)
	if err != nil {
		t.Fatalf("ApplyDeployFlip: %v", err)
	}
	if !flipped {
		t.Fatalf("expected flipped=true")
	}

	var outboxCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_child_progress_outbox WHERE child_issue_id = $1`,
		childID,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox row count = %d, want 1 (parent must be notified)", outboxCount)
	}
}
