// Package multicacli is a thin wrapper around the local `multica` CLI binary.
// It exposes the four operations the mmbot daemon needs:
//
//   - CreateIssue (with --project + --assignee-id hard-guarded to marimo)
//   - AddComment (forwarding MM thread replies to the multica issue)
//   - GetIssue   (to observe status transitions on the outbound poll)
//   - ListComments (incremental --since pull for outbound forwarding)
//
// The CLI is preferred over the multica HTTP API because the CLI handles auth
// refresh, retries, and workspace routing transparently — and because reusing
// it inherits the same set of guarantees as every other agent on the box.
//
// Tests inject a fake Runner; production wires the real os/exec runner.
//
// See: plans://Multica/2026-06-17-pul-328-mattermost-bot-marimo.md (revision 2).
package multicacli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// MarimoProjectID is the canonical project UUID of the marimo project. The
// inbound handler ALWAYS pins --project to this constant, regardless of
// anything the MM user wrote — finding #3 of /plan-eng-review.
const MarimoProjectID = "458aa700-b3cd-402f-a50b-77c0d207eef2"

// DefaultBinary is the standard CLI executable name. Production wires this
// via PATH lookup; tests override.
const DefaultBinary = "multica"

// DefaultTimeout caps a single CLI invocation. Multica API rarely takes
// more than 1s; the cap is generous so a slow workspace doesn't kill the
// daemon under load.
const DefaultTimeout = 15 * time.Second

// Runner executes one CLI invocation with the given args (excluding the
// binary name itself) and optional stdin. Returns stdout bytes plus the
// exit error, if any. Tests inject a fake; production uses NewExecRunner.
type Runner interface {
	Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error)
}

// Client is the typed wrapper over the CLI runner.
type Client struct {
	runner  Runner
	logger  *slog.Logger
	// AssigneeAgentID is the multica agent UUID we always assign created
	// issues to (e.g. agent-2 in production). Read from configuration.
	AssigneeAgentID string
}

// Config bundles construction parameters.
type Config struct {
	// Binary is the CLI executable name or path. Zero falls back to
	// DefaultBinary.
	Binary string
	// Timeout caps an individual invocation. Zero falls back to
	// DefaultTimeout.
	Timeout time.Duration
	// Logger for non-fatal warnings (e.g. CLI deprecation hints on stderr).
	// nil → slog.Default.
	Logger *slog.Logger
	// AssigneeAgentID is the multica agent UUID newly created issues are
	// assigned to (e.g. agent-2). Required for production use; tests can
	// leave empty when the test path doesn't reach CreateIssue.
	AssigneeAgentID string
	// Runner overrides the os/exec runner. nil → NewExecRunner(Binary, Timeout).
	Runner Runner
}

// New constructs a Client. AssigneeAgentID is checked at the call site of
// CreateIssue rather than here so a misconfigured daemon fails loud rather
// than silently dropping marimo project pinning.
func New(cfg Config) *Client {
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Runner == nil {
		cfg.Runner = NewExecRunner(cfg.Binary, cfg.Timeout)
	}
	return &Client{
		runner:          cfg.Runner,
		logger:          cfg.Logger,
		AssigneeAgentID: cfg.AssigneeAgentID,
	}
}

// ── domain types ─────────────────────────────────────────────────────────────

// Issue is the multica issue shape, with only the fields the bot inspects.
type Issue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ProjectID  string `json:"project_id"`
	AssigneeID string `json:"assignee_id"`
}

// Comment is the multica comment shape.
type Comment struct {
	ID         string `json:"id"`
	IssueID    string `json:"issue_id"`
	AuthorID   string `json:"author_id"`
	AuthorType string `json:"author_type"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at"`
}

// ── CreateIssue ──────────────────────────────────────────────────────────────

// CreateIssueRequest is the bot's create-issue intent. The bot constructs
// this from an inbound MM top-level post; project + assignee are NOT taken
// from this struct — they're hard-pinned to marimo + AssigneeAgentID inside
// CreateIssue, defending against any prompt-injection inside Body.
type CreateIssueRequest struct {
	Title       string // first 80 chars of the MM message
	Description string // full MM message + "From: @mm-username" footer
}

// ErrAssigneeUnset means the Client wasn't configured with AssigneeAgentID
// at construction time. The daemon must set this before serving — failing
// here is a deployment bug, not a runtime condition.
var ErrAssigneeUnset = errors.New("multicacli: AssigneeAgentID required for CreateIssue")

// CreateIssue invokes `multica issue create --project <marimo>
// --assignee-id <agent> --title <truncated> --description-stdin`. The
// --project and --assignee-id are CONSTANTS in our process; the request
// struct cannot influence them. Returns the parsed Issue.
func (c *Client) CreateIssue(ctx context.Context, req CreateIssueRequest) (Issue, error) {
	if c.AssigneeAgentID == "" {
		return Issue{}, ErrAssigneeUnset
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "(no title)"
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:80])
	}

	args := []string{
		"issue", "create",
		"--project", MarimoProjectID,
		"--assignee-id", c.AssigneeAgentID,
		"--title", title,
		"--description-stdin",
		"--output", "json",
	}
	stdout, err := c.runner.Run(ctx, args, strings.NewReader(req.Description))
	if err != nil {
		return Issue{}, fmt.Errorf("multicacli: create issue: %w", err)
	}
	var out Issue
	if err := json.Unmarshal(stdout, &out); err != nil {
		return Issue{}, fmt.Errorf("multicacli: decode create response: %w (body=%q)", err, truncate(stdout, 200))
	}
	// Defense-in-depth assertion: surface a loud error if the CLI somehow
	// landed the issue in the wrong project. This should be impossible
	// given the --project flag, but checking here catches a CLI bug
	// before it becomes a security incident.
	if out.ProjectID != MarimoProjectID {
		return Issue{}, fmt.Errorf("multicacli: created issue %s landed in project %q, expected marimo %q",
			out.Identifier, out.ProjectID, MarimoProjectID)
	}
	return out, nil
}

// ── AddComment ───────────────────────────────────────────────────────────────

// AddComment forwards a MM thread-reply into multica. Returns the new
// Comment so the caller can record `multica_comment_id ↔ mm_post_id` in
// state for echo dedup.
func (c *Client) AddComment(ctx context.Context, issueID, content string) (Comment, error) {
	if issueID == "" {
		return Comment{}, errors.New("multicacli: issueID required")
	}
	args := []string{
		"issue", "comment", "add", issueID,
		"--content-stdin",
		"--output", "json",
	}
	stdout, err := c.runner.Run(ctx, args, strings.NewReader(content))
	if err != nil {
		return Comment{}, fmt.Errorf("multicacli: add comment: %w", err)
	}
	var out Comment
	if err := json.Unmarshal(stdout, &out); err != nil {
		return Comment{}, fmt.Errorf("multicacli: decode comment response: %w (body=%q)", err, truncate(stdout, 200))
	}
	return out, nil
}

// ── GetIssue ─────────────────────────────────────────────────────────────────

// GetIssue fetches one issue by UUID. Used by outbound polling to observe
// status transitions.
func (c *Client) GetIssue(ctx context.Context, issueID string) (Issue, error) {
	if issueID == "" {
		return Issue{}, errors.New("multicacli: issueID required")
	}
	args := []string{"issue", "get", issueID, "--output", "json"}
	stdout, err := c.runner.Run(ctx, args, nil)
	if err != nil {
		return Issue{}, fmt.Errorf("multicacli: get issue: %w", err)
	}
	var out Issue
	if err := json.Unmarshal(stdout, &out); err != nil {
		return Issue{}, fmt.Errorf("multicacli: decode get response: %w", err)
	}
	return out, nil
}

// ── ListComments ─────────────────────────────────────────────────────────────

// ListComments returns comments on an issue created strictly after `since`.
// Empty `since` returns the latest 50.
//
// The CLI prints a brief "Showing N of M comments." line to stderr before
// the JSON payload; we don't capture stderr, so the JSON-only stdout decodes
// cleanly.
func (c *Client) ListComments(ctx context.Context, issueID string, since time.Time) ([]Comment, error) {
	if issueID == "" {
		return nil, errors.New("multicacli: issueID required")
	}
	args := []string{"issue", "comment", "list", issueID, "--output", "json"}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Format(time.RFC3339))
	}
	stdout, err := c.runner.Run(ctx, args, nil)
	if err != nil {
		return nil, fmt.Errorf("multicacli: list comments: %w", err)
	}
	// Some CLI versions prefix the JSON array with a "Showing N of M" line
	// on stdout. Be lenient: scan for the first '['.
	if i := bytes.IndexByte(stdout, '['); i >= 0 {
		stdout = stdout[i:]
	}
	var out []Comment
	if err := json.Unmarshal(stdout, &out); err != nil {
		return nil, fmt.Errorf("multicacli: decode comment list: %w (body=%q)", err, truncate(stdout, 200))
	}
	return out, nil
}

// ── exec runner ──────────────────────────────────────────────────────────────

// execRunner is the production Runner; shells out to the CLI binary.
type execRunner struct {
	binary  string
	timeout time.Duration
}

// NewExecRunner wraps `binary` (a CLI executable name or path) with a Runner
// that performs os/exec.CommandContext with the configured per-call timeout.
func NewExecRunner(binary string, timeout time.Duration) Runner {
	if binary == "" {
		binary = DefaultBinary
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &execRunner{binary: binary, timeout: timeout}
}

func (r *execRunner) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, r.binary, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		// Surface a few bytes of stderr in the error message so an oncall
		// reading logs can see the actual failure without grepping
		// elsewhere.
		return stdout, fmt.Errorf("%s %s: %w (stderr=%q)",
			r.binary, strings.Join(args, " "), err, truncate(stderr.Bytes(), 200))
	}
	return stdout, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
