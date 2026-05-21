package cascade

import "regexp"

// LookupIssueIdentifier extracts the issue identifier (e.g. "PUL-102")
// from the PR title or, failing that, the branch name. Returns the
// stringified PUL-N when found, empty otherwise. The caller then
// uses GetIssueByNumber on multica's queries to map number → issue
// UUID inside the workspace.
//
// G4 amendment: title parsing is primary; branch fallback covers
// the case where a reviewer edited the title and dropped the prefix.
// Both regexes are case-insensitive on the alpha prefix so user
// typos like "pul-102" still match.
//
// The shape `[A-Z]+-[0-9]+` accepts any multica issue-prefix the
// workspace has configured (PUL, MUL, OPS, …) so this code does not
// hardcode "PUL".
func LookupIssueIdentifier(prTitle, branch string) string {
	if id := matchTitle(prTitle); id != "" {
		return id
	}
	return matchBranch(branch)
}

// titleRegex matches a leading [PREFIX-N] bracket. Anchored to the
// start so a PUL reference deeper in the title (e.g. "fix typo in
// PUL-99 ref") does not falsely match — PR titles authored by
// agents put the identifier first by convention.
var titleRegex = regexp.MustCompile(`^\[([A-Za-z]+-[0-9]+)\]`)

func matchTitle(s string) string {
	m := titleRegex.FindStringSubmatch(s)
	if len(m) != 2 {
		return ""
	}
	return normalize(m[1])
}

// branchRegex matches the cascade conventions seen in production:
//
//	agent-<N>/<prefix>-<N>-<slug>      canonical, dash separator
//	agent-<N>/<prefix><N>-<slug>       no dash between prefix and number
//	agent-<N>/<sub>-<prefix><N>-<slug> sub-classifier prefix (e.g. u1-)
//
// Anchored to start. Pre-cascade conventions like `feat/pul-1-foo`
// do not match — the scope filter only triggers for agent-driven
// PRs. The alpha prefix is required to be >=2 chars so single-letter
// unit ids like "u1" / "v2" do not get mistaken for an identifier.
//
// The sub-classifier group uses lazy `??` so the engine prefers to
// skip it and bind the first <alpha-2+><digits> pair as the
// identifier. Greedy `?` would let `pul196-` be consumed as a "sub"
// and resolve `agent-1/pul196-pr5.x-slug` to PR-5 instead of
// PUL-196 — RE2's leftmost-first matching makes the failure
// deterministic, not stochastic. See PUL-216 for the empirical
// trace against all observed prod-miss branches.
//
// When the prefix-N pair matches a name no workspace recognises
// (e.g. "PR-5" from `agent-1/pr5-only-rev`), downstream IssueLoader
// returns ErrIssueNotFound and the worker scope_filter_skips with a
// distinct reason — strictly safer than a stricter regex that
// silently drops real PUL-N rows.
var branchRegex = regexp.MustCompile(
	`^agent-[0-9a-zA-Z]+/(?:[a-zA-Z0-9.]+-)??([A-Za-z]{2,})-?([0-9]+)(?:[-_./].*)?$`,
)

func matchBranch(s string) string {
	m := branchRegex.FindStringSubmatch(s)
	if len(m) != 3 {
		return ""
	}
	return normalize(m[1]) + "-" + m[2]
}

// normalize uppercases the alpha prefix so look-ups against
// workspace.issue_prefix are consistent regardless of caller casing.
// "pul-99" and "PUL-99" must map to the same issue.
func normalize(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// agentBranchRegex matches the looser "any agent-driven branch"
// shape used by the scope filter. Unlike branchRegex, this does not
// require a `<PREFIX-N>` segment after the agent prefix — the scope
// filter only confirms "yes, an agent owns this branch", regardless
// of whether an identifier can be extracted. Identifier extraction
// is branchRegex's job and is intentionally stricter.
var agentBranchRegex = regexp.MustCompile(`^agent-[0-9a-zA-Z]+/`)

// InScope reports whether a PR title + branch combination satisfies
// the cascade scope filter (C4): only agent-driven PRs trigger the
// pipeline. A PR is in-scope when either the title carries a
// `[PREFIX-N]` bracket OR the branch starts with `agent-<id>/`
// (regardless of what follows the slash).
//
// Manual user PRs (no agent branch, no bracket prefix) return false
// — webhook handler logs them and skips. The filter lives here, not
// in the router, per C4 "scope filter ONE place — in handler".
func InScope(prTitle, branch string) bool {
	if titleRegex.MatchString(prTitle) {
		return true
	}
	if agentBranchRegex.MatchString(branch) {
		return true
	}
	return false
}
