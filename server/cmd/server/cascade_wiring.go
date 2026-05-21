package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/cascade"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// startCascadeBackground constructs and starts the cascade worker +
// reconciliation cron in their own goroutines. Called once from
// NewRouterWithOptions after the TaskService is fully wired.
//
// No-ops when MULTICA_CASCADE_WEBHOOK_ENABLED is off OR when
// MULTICA_CASCADE_WORKSPACE_ID is missing — the worker can't lookup
// issues without a workspace context, so we fail-loud-at-startup
// rather than silently scope-skipping every event.
//
// The goroutines run under context.Background() for the process
// lifetime. Graceful shutdown of cascade work is a follow-up; the
// router doesn't currently expose a shutdown context to threads.
func startCascadeBackground(pool *pgxpool.Pool, queries *db.Queries, taskSvc *service.TaskService, bus *events.Bus, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if !cascadeFlagEnabled() {
		return
	}

	workspaceID := strings.TrimSpace(os.Getenv("MULTICA_CASCADE_WORKSPACE_ID"))
	if workspaceID == "" {
		logger.Warn("cascade.wiring.no_workspace",
			"hint", "set MULTICA_CASCADE_WORKSPACE_ID to enable cascade worker")
		return
	}
	wsUUID, err := uuid.Parse(workspaceID)
	if err != nil {
		logger.Error("cascade.wiring.invalid_workspace_id",
			"value", workspaceID, "error", err)
		return
	}

	spawner := &taskServiceSpawner{
		pool:    pool,
		queries: queries,
		taskSvc: taskSvc,
		bus:     bus,
		logger:  logger,
	}
	loader := &queriesIssueLoader{queries: queries, workspaceID: wsUUID}
	// PUL-194: pool + queries are also passed through so the worker can run
	// the server-side deploy auto-flip on pr_merged events. Both nil means
	// the auto-flip is silently disabled — keep them populated in production.
	// PUL-220: metrics is wired by main.go in a later commit; nil here keeps
	// the build green and disables the legacy-event-dropped counter until then.
	worker := cascade.NewWorker(pool, spawner, loader, pool, queries, nil, logger)

	// Reconciler nudge: log only at this wiring level. The
	// notify.Bridge (PR6) is the proper surface for off-platform
	// nudges; wiring the bridge requires Slack/Telegram env vars +
	// a CommentPoster adapter, which lands in a separate follow-up
	// alongside per-workspace channel routing. For now the cron
	// logs the stuck-cascade event so it shows up in the structured
	// log pipeline and observability picks it up.
	reconciler := cascade.NewReconciler(pool, func(_ context.Context, r cascade.StuckCascadeReport) {
		logger.Warn("cascade.stuck_detected",
			"issue_id", r.IssueID,
			"issue_number", r.IssueNumber,
			"last_event_at", r.LastEventAt,
			"staleness_hours", r.StalenessHours,
		)
	}, logger)

	go worker.Run(context.Background())
	go reconciler.Run(context.Background())

	logger.Info("cascade.wiring.started",
		"workspace_id", workspaceID,
		"github_real_adapter", os.Getenv("MULTICA_GITHUB_WEBHOOK_SECRET_CURRENT") != "",
		"github_pr_resolver", os.Getenv("MULTICA_GITHUB_API_TOKEN") != "")
}

// cascadeFlagEnabled mirrors webhooks.envEnabled but lives in main
// so we don't import a second time. Truthy values match the
// webhooks package parser.
func cascadeFlagEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_CASCADE_WEBHOOK_ENABLED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// taskServiceSpawner adapts service.TaskService to the
// cascade.Spawner interface.
//
// Spawn inserts a synthetic system comment carrying the cascade
// TriggerContext, then enqueues a task referencing it via the new
// trigger_comment_id wiring. The comment is the seam that lets
// daemon.buildCommentPrompt's existing [NEW COMMENT] path surface the
// wake-up reason — DO NOT remove the comment insert without also
// extending daemon.BuildPrompt to read TriggerSummary directly.
// See PUL-168.
//
// The comment insert and the task enqueue run in a single pgx tx
// so a transient enqueue failure rolls the comment back and the next
// worker retry inserts fresh state — no duplicate "🤖 cascade wake-up"
// comments accumulating in the thread.
type taskServiceSpawner struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	taskSvc *service.TaskService
	bus     *events.Bus
	logger  *slog.Logger
}

func (s *taskServiceSpawner) Spawn(ctx context.Context, issueID uuid.UUID, tc cascade.TriggerContext) error {
	issue, err := s.queries.GetIssue(ctx, pgtype.UUID{Bytes: issueID, Valid: true})
	if err != nil {
		return fmt.Errorf("cascade spawner: load issue: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("cascade spawner: begin tx: %w", err)
	}
	defer func() {
		// Safe to call after Commit — pgx rolls back only when the tx
		// is still open.
		_ = tx.Rollback(ctx)
	}()

	qtx := s.queries.WithTx(tx)
	comment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		// AuthorID stays Valid=false → NULL in the row. Allowed by
		// migration 078 (author_id is now nullable); the notification
		// + subscriber listeners skip type='system' so the NULL is
		// never dereferenced downstream.
		AuthorID: pgtype.UUID{},
		Content:  formatCascadeWakeMessage(tc),
		Type:     "system",
		ParentID: pgtype.UUID{},
	})
	if err != nil {
		return fmt.Errorf("cascade spawner: create synth comment: %w", err)
	}

	task, err := s.taskSvc.EnqueueTaskForIssueInTx(ctx, qtx, issue, comment.ID)
	if err != nil {
		// Translate deterministic enqueue gates into the cascade
		// permanent-skip sentinel so the worker stops retrying. The
		// tx rolls back via this returned error → no orphan synth
		// comment in the thread. The operator fix (assign agent,
		// unarchive, wire runtime) will be picked up by the NEXT
		// webhook delivery, not by replaying this row.
		if errors.Is(err, service.ErrIssueHasNoAssignee) ||
			errors.Is(err, service.ErrAssigneeAgentArchived) ||
			errors.Is(err, service.ErrAssigneeAgentNoRuntime) {
			return fmt.Errorf("cascade spawner: enqueue gated (%w): %v", cascade.ErrSpawnGated, err)
		}
		return fmt.Errorf("cascade spawner: enqueue: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("cascade spawner: commit tx: %w", err)
	}

	// Post-commit: announce the task so daemon/UI observers wake up,
	// and publish the synth comment so realtime issue-thread bubbles
	// render. notification_listeners + subscriber_listeners skip
	// type="system" so this publish does NOT fan out a `new_comment`
	// notification to every issue subscriber.
	s.taskSvc.AnnounceTaskQueued(ctx, task)
	s.publishSynthCommentCreated(issue, comment)

	return nil
}

func (s *taskServiceSpawner) publishSynthCommentCreated(issue db.Issue, comment db.Comment) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: uuidString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": map[string]any{
				"id":          uuidString(comment.ID),
				"issue_id":    uuidString(comment.IssueID),
				"author_type": comment.AuthorType,
				"author_id":   uuidStringNullable(comment.AuthorID),
				"content":     comment.Content,
				"type":        comment.Type,
				"parent_id":   uuidStringNullable(comment.ParentID),
				"created_at":  comment.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
			},
			"issue_title":  issue.Title,
			"issue_status": issue.Status,
		},
	})
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func uuidStringNullable(u pgtype.UUID) any {
	if !u.Valid {
		return nil
	}
	return uuid.UUID(u.Bytes).String()
}

// formatCascadeWakeMessage renders the synth-comment body. Format is
// pinned for test assertions: the agent must see event_type, the PR
// number, the short head_sha, and an actionable CLI hint. The agent
// reads this literally — vague wording = vague behavior, which is the
// exact regression PUL-168 closes. If this format changes, update
// cascade_wiring_test.go too.
func formatCascadeWakeMessage(tc cascade.TriggerContext) string {
	shortSHA := tc.HeadSHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🤖 cascade wake-up: event_type=%s", tc.EventType)
	if tc.PRNumber > 0 {
		fmt.Fprintf(&b, ", PR #%d", tc.PRNumber)
	}
	if shortSHA != "" {
		fmt.Fprintf(&b, ", head_sha=%s", shortSHA)
	}
	b.WriteString("\n\n")
	switch tc.EventType {
	case "ci_failure":
		if tc.PRNumber > 0 {
			fmt.Fprintf(&b, "Investigate via `gh pr checks %d` and `gh run list --branch <branch> --limit 5`. ", tc.PRNumber)
		} else {
			b.WriteString("Investigate via `gh pr checks <pr>` and `gh run list --branch <branch> --limit 5`. ")
		}
		b.WriteString("Do NOT poll the issue thread expecting new comments — CI is the reason you were woken, not a new human reply.\n")
	default:
		fmt.Fprintf(&b, "Generic cascade wake-up; inspect the event payload for `%s` to decide what changed.\n", tc.EventType)
	}
	if tc.PRURL != "" {
		fmt.Fprintf(&b, "\nPR: %s\n", tc.PRURL)
	}
	return b.String()
}

func (s *taskServiceSpawner) HasActiveRun(ctx context.Context, issueID uuid.UUID) (bool, error) {
	active, err := s.queries.HasActiveTaskForIssue(ctx, pgtype.UUID{Bytes: issueID, Valid: true})
	if err != nil {
		return false, fmt.Errorf("cascade spawner: has-active query: %w", err)
	}
	return active, nil
}

// queriesIssueLoader resolves a "PUL-N" identifier to an issue UUID
// by parsing the trailing number and calling GetIssueByNumber. The
// workspace is fixed at construction (single-tenant assumption from
// MULTICA_CASCADE_WORKSPACE_ID); multi-workspace routing is a
// follow-up that needs a repo→workspace mapping table.
type queriesIssueLoader struct {
	queries     *db.Queries
	workspaceID uuid.UUID
}

func (l *queriesIssueLoader) LookupByIdentifier(ctx context.Context, identifier string) (uuid.UUID, error) {
	// Identifier shape is "PREFIX-N" by contract from
	// cascade.LookupIssueIdentifier. Split on the last dash.
	dash := strings.LastIndex(identifier, "-")
	if dash <= 0 || dash == len(identifier)-1 {
		return uuid.Nil, fmt.Errorf("cascade loader: malformed identifier %q", identifier)
	}
	numStr := identifier[dash+1:]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("cascade loader: parse number from %q: %w", identifier, err)
	}
	issue, err := l.queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
		WorkspaceID: pgtype.UUID{Bytes: l.workspaceID, Valid: true},
		Number:      int32(num),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, cascade.ErrIssueNotFound
		}
		return uuid.Nil, err
	}
	return uuid.UUID(issue.ID.Bytes), nil
}
