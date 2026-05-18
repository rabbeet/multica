package daemon

import (
	"strings"
	"testing"
)

// TestBuildCommentPrompt_SystemTrigger covers PUL-168: when the cascade
// Spawner inserts a synthetic comment with author_type='system' and the
// daemon wakes the agent on it, the rendered prompt MUST surface the
// trigger reason verbatim (so the agent reads ci_failure, PR number,
// head_sha) and MUST NOT inject the "⚠️ posted by another agent" warning
// — that warning tells the agent to consider not replying, which is the
// exact wrong advice for a CI failure investigation.
func TestBuildCommentPrompt_SystemTrigger(t *testing.T) {
	task := Task{
		IssueID:               "issue-uuid-stub",
		TriggerCommentID:      "trigger-comment-uuid-stub",
		TriggerCommentContent: "🤖 cascade wake-up: event_type=ci_failure, PR #530, head_sha=3d26b7f1\n\nInvestigate via `gh pr checks 530`",
		TriggerAuthorType:     "system",
	}
	out := buildCommentPrompt(task)
	mustContain := []string{
		"event_type=ci_failure",
		"PR #530",
		"head_sha=3d26b7f1",
		"gh pr checks 530",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("buildCommentPrompt[system] missing %q\n--- full ---\n%s", s, out)
		}
	}
	if strings.Contains(out, "⚠️ The triggering comment was posted by another agent") {
		t.Errorf("buildCommentPrompt[system] must NOT inject the 'another agent' warning — that path is for author_type='agent'; got:\n%s", out)
	}
}

// TestBuildCommentPrompt_AgentTrigger keeps the existing agent-to-agent
// warning wired — a regression here would silently strip the
// loop-prevention advice that keeps two agents from ping-ponging
// thank-you replies.
func TestBuildCommentPrompt_AgentTrigger(t *testing.T) {
	task := Task{
		IssueID:               "issue-uuid-stub",
		TriggerCommentID:      "trigger-comment-uuid-stub",
		TriggerCommentContent: "thanks, looks good",
		TriggerAuthorType:     "agent",
		TriggerAuthorName:     "agent-1",
	}
	out := buildCommentPrompt(task)
	if !strings.Contains(out, "⚠️ The triggering comment was posted by another agent") {
		t.Errorf("buildCommentPrompt[agent] must keep the 'another agent' warning; got:\n%s", out)
	}
}

// TestBuildQuickCreatePromptRules locks in the rules that govern how the
// quick-create agent is allowed to translate raw user input into the issue
// description body. Each substring corresponds to a concrete failure mode
// observed in production output:
//   - meta-instructions ("create an issue", "cc @X") leaking into the body
//   - the Context section being misused as an apology log when no external
//     references were actually fetched
//   - hard-line rules being silently dropped on prompt rewrites
func TestBuildQuickCreatePromptRules(t *testing.T) {
	out := buildQuickCreatePrompt(Task{QuickCreatePrompt: "fix the login button color"})

	mustContain := []string{
		// high-fidelity invariant
		"Faithfully restate what the user wants",
		"Preserve specific names, identifiers, file paths",
		// strip non-spec material: verbal routing wrappers + conversational fillers
		"verbal routing wrappers about creating the issue",
		"pure conversational fillers",
		// cc routing must survive: mention link stays in description so the
		// auto-subscribe path fires (multica issue create has no --subscriber flag)
		"CC exception",
		"auto-subscribes members",
		// context section is conditional and must not be an apology log
		"include ONLY when the input cited external resources",
		"never use it as an apology log",
		// hard rules
		"never invent requirements",
		"never reduce multi-sentence input",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("buildQuickCreatePrompt output missing required rule: %q", s)
		}
	}
}
