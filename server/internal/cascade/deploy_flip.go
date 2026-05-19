package cascade

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DecideDeployFlip is the pure decision layer for the PUL-194 server-side
// auto-flip to 'deployed' on pr_merged events.
//
// Eligible iff:
//   - status is NOT one of the terminal states 'deployed' / 'cancelled'
//     (idempotency + don't override a manual cancellation).
//   - cascadeState is NULL (single-PR work — no active cascade, this merge IS
//     the final state) OR 'completed' (defensive: covers admin SQL flipping
//     cascade_state to 'completed' before the worker processes the merge; in
//     the normal cascade flow this branch is unreachable because the agent
//     transitions cascade_state AFTER worker.processOne returns).
//
// Intermediate cascade-PR merges arrive with cascade_state='approved' and are
// intentionally rejected here so multi-PR semantics are preserved (the agent
// drives the cascade through each PR, the server only catches the tail).
//
// Pure function — no I/O. Caller (ApplyDeployFlip) is responsible for
// locking the issue row with FOR UPDATE before calling.
func DecideDeployFlip(status string, cascadeState pgtype.Text) bool {
	if status == "deployed" || status == "cancelled" {
		return false
	}
	if !cascadeState.Valid {
		return true
	}
	return cascadeState.String == "completed"
}

// ApplyDeployFlip writes the full deploy transition atomically:
//
//  1. SELECT ... FOR UPDATE on the issue row so concurrent flippers serialize.
//  2. DecideDeployFlip against the locked row — bail out as no-op when
//     ineligible (returns flipped=false, err=nil).
//  3. UpdateIssueStatusToDeployed — sets status='deployed', stamps deployed_at
//     on first arrival via COALESCE.
//  4. InsertStatusHistory with source='hook_pr_merged', ref_id=eventID — the
//     audit row downstream consumers key off. UNIQUE (source, ref_id) is the
//     idempotency contract: replayed GitHub deliveries hit the duplicate-row
//     branch and return (flipped=false, err=nil) so callers can safely call
//     this on every event.
//  5. EnqueueChildProgressFanout — PUL-164 contract for status-mutating
//     transactions. ShouldFanOutTransition already whitelists ' → deployed';
//     orphan children with no parent are a clean no-op inside the helper.
//
// All five steps share one pgx transaction; any error (other than the
// idempotent-replay branch) rolls everything back so a half-flipped issue
// never escapes.
//
// Caller — currently cascade.Worker.processOne for event_type='pr_merged' —
// must invoke this BEFORE the spawn/concurrency/loop-guard pipeline, because
// the deploy transition reflects "PR is in main" regardless of whether the
// agent runs. See plan revision dbdaf20 for the regression-guard rationale.
func ApplyDeployFlip(
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *db.Queries,
	issueID uuid.UUID,
	eventID uuid.UUID,
) (flipped bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("cascade.ApplyDeployFlip: begin tx: %w", err)
	}
	defer func() {
		// Safe after Commit — pgx rolls back only while the tx is still open.
		_ = tx.Rollback(ctx)
	}()
	qtx := queries.WithTx(tx)

	// Lock by id alone. db.GetIssueForUpdate requires (id, workspace_id) — a
	// safety net for the HTTP handler where workspace context comes from the
	// auth layer — but the cascade worker already resolved this issueID
	// earlier in the pipeline via IssueLoader.LookupByIdentifier, which is
	// workspace-scoped at construction (queriesIssueLoader.workspaceID). The
	// id is the FK + PK source of truth here; an extra workspace filter
	// would add a redundant predicate without changing the lock semantics.
	var (
		idCol            pgtype.UUID
		workspaceID      pgtype.UUID
		parentIssueID    pgtype.UUID
		currentStatus    string
		currentCascadeSt pgtype.Text
	)
	if err := tx.QueryRow(ctx, `
		SELECT id, workspace_id, parent_issue_id, status, cascade_state
		FROM issue
		WHERE id = $1
		FOR UPDATE`,
		pgtype.UUID{Bytes: issueID, Valid: true},
	).Scan(&idCol, &workspaceID, &parentIssueID, &currentStatus, &currentCascadeSt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Issue vanished between cascade_retrigger ingest and now —
			// nothing to flip. Treat as clean no-op.
			return false, nil
		}
		return false, fmt.Errorf("cascade.ApplyDeployFlip: lock issue: %w", err)
	}

	if !DecideDeployFlip(currentStatus, currentCascadeSt) {
		return false, nil
	}

	updated, err := qtx.UpdateIssueStatusToDeployed(ctx, idCol)
	if err != nil {
		return false, fmt.Errorf("cascade.ApplyDeployFlip: update status: %w", err)
	}

	refID := eventID.String()
	history, err := qtx.InsertStatusHistory(ctx, db.InsertStatusHistoryParams{
		IssueID:    idCol,
		FromStatus: pgtype.Text{String: currentStatus, Valid: true},
		ToStatus:   updated.Status,
		Source:     SourceHookPRMerged,
		ActorID:    pgtype.UUID{},
		ActorType:  pgtype.Text{String: "system", Valid: true},
		RefID:      pgtype.Text{String: refID, Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Idempotent replay: the (hook_pr_merged, event_id) pair already
			// recorded a flip. The earlier write also enqueued fan-out, so
			// re-running it here would be a double-notification. Roll back
			// the status update (idempotent re-write of the same final
			// state, fine to discard) and report no-op.
			return false, nil
		}
		return false, fmt.Errorf("cascade.ApplyDeployFlip: insert history: %w", err)
	}

	if parentIssueID.Valid {
		if _, _, fanErr := service.EnqueueChildProgressFanout(ctx, qtx, service.EnqueueChildProgressParams{
			WorkspaceID:     workspaceID,
			SourceHistoryID: history.ID,
			ChildIssueID:    idCol,
			PrevStatus:      currentStatus,
			NewStatus:       updated.Status,
			ActorID:         pgtype.UUID{},
			ActorType:       pgtype.Text{String: "system", Valid: true},
		}); fanErr != nil {
			return false, fmt.Errorf("cascade.ApplyDeployFlip: child_progress fanout: %w", fanErr)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("cascade.ApplyDeployFlip: commit: %w", err)
	}
	return true, nil
}

// SourceHookPRMerged is the issue_status_history.source value emitted by
// ApplyDeployFlip. The CHECK constraint for this value lands in migration
// 084_pr_merged_status_history_source.up.sql. Paired with the GitHub event_id
// as ref_id, the table's UNIQUE (source, ref_id) gives idempotent replay.
const SourceHookPRMerged = "hook_pr_merged"

// isUniqueViolation matches pgx-wrapped PG unique-constraint violation
// errors (SQLSTATE 23505). Mirrors handler.isUniqueViolation but kept local
// to the cascade package so deploy_flip.go has no cross-package dep on an
// unexported helper.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
