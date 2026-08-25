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
| 6 CI failure triage | `failure-triager` |
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

Set `autonomy.dev` to `gated_write` to withdraw the carve-out entirely and
stop at the PR everywhere, matching nj-agents exactly.

## Conforming to the harness

nj-agents ships a harness with class contracts and automated checks. Orion's
own skills (`beej`, `kalp`, `forge`) are authoring-class by its taxonomy:
they write an artifact into the repo. They should be validated against
`CONVENTIONS-authoring.md` and the harness checks before shipping, rather
than being assumed compatible.

## Unverified

This mapping comes from the nj-agents README and its published documentation
site, not from reading its `SKILL.md` files. Before shipping, confirm:

- exact skill names and invocation syntax
- **whether these skills can be invoked at all from a non-interactive
  `claude -p` run**, which is how the supervisor drives them. This is the
  load-bearing assumption of the whole delegation design.
- the CI flag (`NJ_AGENTS_CI=1`) and the exit-code contract
- the report directory variable (`NJ_AGENTS_REPORT_DIR`)
- whether `/security-deep-review` honours a diff scope when driven
  non-interactively, since `--full` on a large repo would be very expensive

If those skills turn out to be interactive-only, the delegation design needs
rework: either the supervisor drives them differently, or Orion invokes them
from a foreground session and the autonomy story shrinks.
