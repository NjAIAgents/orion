# Orion and nj-agents

Orion does not reimplement review, security scanning, testing or PR
authoring. [nj-agents](https://github.com/navjyotnishant/nj-agents) already
does all of it, with dedicated agents per dimension, a **required** secret
scanner rather than a heuristic fallback, and an adversarial verification
pass on security findings. A thinner reimplementation would be strictly
worse.

## Division of labour

| Concern | Owner |
|---|---|
| Deterministic enforcement: loops, budgets, wall clock, gates | **Orion** binary |
| Isolated sandboxed workspaces | **Orion** |
| Process supervision: kill, quota wait, retry, notify | **Orion** |
| Cross-project lesson memory | **Orion** |
| Artifact chain: intent, spec, plan | **Orion** skills |
| Review, security, testing, PR, docs, PM decomposition | **nj-agents** |

Orion is the engine. nj-agents is the skill library. Orion sequences; it does
not duplicate.

## Stage delegation

| Orion stage | Delegates to |
|---|---|
| 4 Test, planning | `/test-plan`, `/test-author`, `/test-suite-author` |
| 4 Test, running | `/review-tests-build` |
| 4 Test, repair | `/test-repair`, `/test-triage`, `/flake-watch` |
| 4 Test, end to end | `/e2e-suite`, `/e2e-run` |
| 4 Coverage gaps | `/test-gap-finder` |
| 5 Review | `/pre-push-review` (umbrella over five dimensions) |
| 5 Security, standard | `/review-secrets` (hard gate, blocks on a hit) |
| 5 Security, high risk | `/security-deep-review` (see cost note below) |
| 5 PR | `/pr-describe` (draft only) |
| 5 Commit | `/commit-assistant` |
| 6 CI failure triage | `/test-triage`, `/flake-watch` |
| 6 Release | `/changelog`, `/release-notes` |
| Decomposition | `/pm-plan` (Epic to Stories to Tasks) |

## The cost interaction, and why it needed a config field

`/security-deep-review` runs roughly 5 finder calls plus up to 15 verifier
calls, commonly 15 to 30 agent calls for a mid-size diff. Orion's breaker
defaults to 400 tool calls per session and counts every one.

That is a genuine conflict, not a theoretical one: a deep review invoked late
in a session trips the breaker mid-review, and the breaker is wrong to do it.
The review is exactly the expensive, legitimate work the budget exists to
protect.

Two mitigations, both in `delegation` in `orion.json`:

- `extra_tool_calls_for_review` widens the envelope while a delegated review
  orchestrator runs. Default 200, deliberately generous, because the failure
  mode of too small a number is a review that cannot finish.
- `deep_security_review_when` tiers the spend. Default `high-risk`: the deep
  pass runs when the change touches auth, crypto, payments, migrations,
  deserialization, Dockerfiles or Terraform, and the standard `/review-secrets`
  gate runs otherwise. Set `always` if the cost is acceptable, `never` to
  disable.

Running the deep pass on every change would be safer and would also make
Orion too expensive to use, which ends with people turning it off. Tiering is
the honest compromise.

## The contract conflict, resolved

nj-agents' founding rule is that **the human decides what gets committed**.
Its skills propose and never run git. Orion, configured for auto-merge on
green, violates that rule.

**Resolution, confirmed with the author: Orion may override it**, scoped to a
sandboxed workspace that Orion itself provisioned, where the blast radius is
a throwaway directory under `$ORION_HOME` rather than the user's repository.

Even there it is off by default and requires all of:

- `auto_merge.enabled: true`
- a non-production environment
- at least `min_eval_cases` real cases in `evals/`
- the named checks passing

Green means nothing when the eval suite is empty, which is why the case count
is a precondition rather than a suggestion.

Set `autonomy.dev` (the dev ENVIRONMENT, not the develop branch) to `gated_write` to withdraw the carve-out entirely and
stop at the PR everywhere, matching nj-agents exactly.

## Conforming to the harness

nj-agents ships a harness with class contracts and automated checks. Orion's
own skills (`beej`, `kalp`, `forge`) are authoring-class by its taxonomy:
they write an artifact into the repo. They should be validated against
`CONVENTIONS-authoring.md` and the harness checks before shipping, rather
than being assumed compatible.

## Verified against the installed toolkit

Checked against the real install at `~/.claude/skills` (38 skills) and
`CONVENTIONS.md` in the toolkit repo.

**Confirmed:**

- Every skill named in the table above exists, with one exception, corrected
  below.
- `/pre-push-review` is genuinely an umbrella over five dimensions, and the
  five standalone skills exist: `review-secrets`, `review-correctness`,
  `review-tests-build`, `review-dependencies`, `review-style`.
- **Non-interactive mode is real.** CONVENTIONS.md §5: mode is signalled by
  `NJ_AGENTS_CI=1`, a `--ci` argument, or the user stating it is for a
  pipeline. This was the load-bearing assumption of the whole delegation
  design, and it holds.
- **The exit-code contract is real.** §5: `0` for PASS or WARN, non-zero
  (`1`) for BLOCK. The supervisor can key off this directly.
- **`NJ_AGENTS_REPORT_DIR` is real.** §6: reports go to
  `${NJ_AGENTS_REPORT_DIR:-<repo>/.nj-agents-reports}/`, never into the repo
  tree unless gitignored.
- In CI mode, ambiguity resolves to the safe outcome: a suspected secret
  BLOCKs rather than waiting for confirmation. That matches Orion's posture.

**Corrected:** the CI failure triage row named `failure-triager`, which does
not exist. The real skills are `/test-triage` and `/flake-watch`.

**Still unverified:**

- whether `/security-deep-review` honours a diff scope when driven
  non-interactively. `--full` on a large repo would be very expensive, and
  the `extra_tool_calls_for_review` budget assumes a diff-scoped run.
- the exact argument syntax each skill accepts when invoked via `claude -p`.

**Note for Orion's own skills:** CONVENTIONS.md §7 makes "advises only, never
runs git push, never bypasses a hook" non-negotiable across every nj-agents
skill. Orion's auto-merge carve-out does not change that for the delegated
skills themselves; they still only advise, and Orion acts on the exit code.
