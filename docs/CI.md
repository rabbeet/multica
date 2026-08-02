# Multica CI/CD pipeline

This document describes the CI/CD pipeline being ported into `rabbeet/multica`
from `rabbeet/Pulse` under PUL-235.

## Status (2026-05-23)

- **Phase**: M1 landed (this PR). Telegram composite + OpenCode security smoke +
  this doc.
- **Next**: M2 (code-review.yml + code-review-fix.yml), M3 (auto-gofmt +
  auto-merge), M4 (pr-test-autofix + ci-autofix), M5 (release-watchdog).
- **Follow-up**: PUL-236 extracts the duplicated workflows into reusable
  workflows (`workflow_call`) hosted in `rabbeet/Pulse`, and reduces multica's
  copies to thin caller stubs.

See the design doc at
[`rabbeet/plans:Multica/2026-05-23-pul-235-pulse-pipeline-port-to-multica.md`](https://github.com/rabbeet/plans/blob/main/Multica/2026-05-23-pul-235-pulse-pipeline-port-to-multica.md)
for the full rollout plan, premise challenge, and approach selection.

## Architecture overview

Once all PRs land, agent PRs against `rabbeet/multica` will flow through this
cascade with no human in the loop on the happy path:

```
PR opened (agent-* branch)
    │
    ├─► auto-gofmt           (formats Go + JS on agent-* branches)
    │       │
    │       └─► push triggers CI re-run
    │
    ├─► CI                   (frontend + backend tests, lint, typecheck, build)
    │       │
    │       ├─► [green]   code-review                (OpenCode posts verdict + labels)
    │       │             ├─► [opencode-approved]   auto-merge-on-approval (squashes + deletes branch)
    │       │             ├─► [opencode-fix-needed] code-review-fix       (OpenCode commits fixes, retriggers review)
    │       │             └─► [needs-human-review]  STOP, human required
    │       │
    │       └─► [red]     pr-test-autofix            (OpenCode diagnoses + commits test fixes)
    │
    └─► (after merge to main)
            │
            ├─► [main CI green]   Release (on tag push only — GoReleaser)
            │
            └─► [main CI red]     ci-autofix          (opens follow-up PR with fix)
                                       │
                                       └─► auto-merge once green

post-merge-release-watchdog runs every 5 min, alerts via Telegram if a tag
push has no successful Release run within 25 min.
```

## Required secrets

Provision in `rabbeet/multica` Settings → Secrets and variables → Actions.
**Phase 0 prerequisite** — must be set before any of M2-M5 land, otherwise
workflows will fail at secret resolution.

| Secret | Used by | Source |
|---|---|---|
| `OPENCODE_AUTH_JSON` | code-review, code-review-fix, ci-autofix, pr-test-autofix | OpenCode OpenAI Codex OAuth `auth.json` contents |
| `GH_AUTOFIX_TOKEN` | auto-gofmt, auto-merge-on-approval, code-review-fix, ci-autofix, pr-test-autofix | `op://Pulse-Dev/Pulse-env/GH_AUTOFIX_TOKEN` (same PAT shared with Pulse; `pulse-autofix-bot` identity already has rights on this repo) |
| `TELEGRAM_BOT_TOKEN` | code-review, ci-autofix, post-merge-release-watchdog, pr-test-autofix | `op://Pulse-Dev/Pulse-env/TELEGRAM_BOT_TOKEN` |
| `TELEGRAM_CHAT_ID` | (same as above) | `op://Pulse-Dev/Pulse-env/TELEGRAM_CHAT_ID` |

One-liner to provision (run from a machine with 1Password CLI + `gh` auth):

```bash
for K in OPENCODE_AUTH_JSON GH_AUTOFIX_TOKEN TELEGRAM_BOT_TOKEN TELEGRAM_CHAT_ID; do
  gh secret set --repo rabbeet/multica "$K" --body "$(op read "op://Pulse-Dev/Pulse-env/$K")"
done
```

(Repeat with `--repo rabbeet/multica-server` for the infra repo once S1-S4 land.)

Set the required repository variable to the OpenAI model consumed by every
OpenCode workflow, for example:

```bash
gh variable set OPENCODE_MODEL --repo rabbeet/multica --body 'openai/<model>'
```

## PAT rotation

`GH_AUTOFIX_TOKEN` is owned at the `rabbeet`-org level. PUL-141 documents the
2026-05-17 PAT-death incident (post-`clickavia → rabbeet` ownership transfer).
Annual rotation, calendar-tracked. When rotated, refresh in all three repos
(Pulse + multica + multica-server) in the same maintenance window.

## Bypass labels

Standard labels recognized by the autofix workflows (same semantics as Pulse):

| Label | Effect |
|---|---|
| `opencode-approved` | Auto-merge-on-approval squashes + merges on CI green. Set by code-review only after a machine-readable current-SHA approval. |
| `opencode-fix-needed` | Triggers code-review-fix to autocommit fixes. Set by code-review when the machine-readable verdict requires changes. |
| `needs-human-review` | Hard-stop: all autofix workflows skip. Set after 3 fix rounds OR on autofix-revert-detected, OR manually. |
| `fix-round-N` | Internal counter. Round 4 escalates to `needs-human-review`. |
| `pr-test-autofix-disabled` | Per-PR escape hatch — disables pr-test-autofix for that PR only. |
| `ci-autofix` (PR label) | Identifies a ci-autofix-generated PR; loop guard. |

## Manual escape hatches

- `[ci-autofix]` in a commit message → ci-autofix workflow skips itself
  (loop prevention).
- `[auto-review-fix]` in a commit message → code-review-fix skips re-triggering
  itself.
- `[auto-test-fix]` in a commit message → pr-test-autofix skips re-triggering
  itself.
- `[skip ci]` in a commit message → CI workflow skips.
- `[skip release]` in a tag annotation → post-merge-release-watchdog stays
  quiet for that tag.
- Repository variable `ENABLE_PR_TEST_AUTOFIX=false` → kills pr-test-autofix
  entirely without touching workflow files.

## Approach choice and follow-up

**Why lift-and-shift, not reusable workflows directly?** Office-hours premise
challenge (PUL-235 thread, 2026-05-23) surfaced reusable workflows
(`workflow_call`) as the architecturally-cleaner alternative. We picked the
hybrid: ship lift-and-shift now to unblock multica agents this week, queue the
extraction as **PUL-236**.

**PUL-236 pre-committed defaults** (decided during PUL-235 design):

- **Versioning**: `@v1` immutable major tag (matches `actions/checkout@v4`
  convention).
- **Per-repo customization**: `caller-repo/.github/prompts/*.md` files read by
  reusable workflows via `inputs: project_patterns_file`.

The extraction issue must be opened (not just promised) the moment the last
PUL-235 PR merges. Without that tracking, "temporary copy" becomes "permanent
copy" — exactly the failure mode this section exists to prevent.

## References

- Design doc: [`rabbeet/plans:Multica/2026-05-23-pul-235-pulse-pipeline-port-to-multica.md`](https://github.com/rabbeet/plans/blob/main/Multica/2026-05-23-pul-235-pulse-pipeline-port-to-multica.md)
- Originating issue: [PUL-235](https://multica.ai/issues/PUL-235) (Копирование pipeline с Pulse на Multica)
- Source pipeline: `rabbeet/Pulse/.github/workflows/` (11 workflows, 2436 LOC pre-refactor)
- PAT-death incident: PUL-141 — informs the secret-rotation cadence
- Reusable workflows constraint (`workflow_run` is same-repo only): [GitHub Docs — workflow_run events](https://docs.github.com/en/actions/reference/events-that-trigger-workflows#workflow_run)
