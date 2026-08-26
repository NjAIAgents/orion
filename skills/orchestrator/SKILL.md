---
name: orion
description: >
  Runs an idea end to end through the AI-native SDLC: intent, spec, plan, build,
  verify, review, pull request. Use when someone hands over a project or feature
  and wants it taken from idea to reviewable PR, or asks what stage something is at,
  or wants to resume a paused piece of work.
  Triggers on "build me X", "take this from idea to PR", "run the full cycle",
  "orchestrate this", "what stage is this at", "resume the work on X".
  Delegates review, security, verification and PR authoring to nj-agents skills
  rather than duplicating them.
---

# Orion: the orchestrator

Orion sequences six stages. Each ends by committing an artifact; the next
begins by reading it. The chain of commits is the audit trail.

```
intent.md -> spec.md -> plan.md -> diff+tests -> PR+findings -> incident record
```

## The stages, and who owns each

| Stage | Owner | Artifact |
|---|---|---|
| 1 Intent | `beej` skill | `intent/<slug>.intent.md` |
| 2 Spec | `kalp` skill | `specs/<slug>.spec.md` |
| 3 Plan | `kalp` skill | `plans/<slug>.plan.md` |
| 3b Scaffold | **nj-agents** `/scaffold-project` | repository skeleton, OSPS baseline |
| 3c Provision | `orion` binary | remote repo, branches, Jira project |
| 3d Decompose | **nj-agents** `/pm-plan` | Epic/Story/Task tree, approved first |
| 4 Build | `forge` skill | diff + tests, on a branch cut from develop |
| 4b Verify | **nj-agents** `/review-tests-build` | test output |
| 5 Review | **nj-agents** `/pre-push-review` | severity-ranked findings |
| 5b Security | **nj-agents** `/review-secrets` | hard gate, blocks on a hit |
| 5c PR | **nj-agents** `/pr-describe` | PR into `develop`, never `main` |
| 6 Maintain | `orion` binary | breach becomes a new intent.md |

**Do not reimplement the delegated stages.** nj-agents already does them
better, with dedicated agents per review dimension and a required secret
scanner rather than a heuristic. If nj-agents is not installed, say so and
stop rather than substituting a weaker version.

## The branch model

`main` is the release branch. `develop` is the integration branch and the base
for every pull request. Every task gets its own branch cut from `develop`.

Both are push-protected by the gate hook. Protecting only `main` would leave
the pull request into `develop` optional, and an optional gate is not a gate.

## Guardrails are not advice

The `orion` binary enforces, deterministically, through hooks:

- **breaker** stops identical repeated calls, repeated failures of the same
  command, tool-call budget, unverified edit runs, blast radius, wall clock
- **gate** refuses production deploys without a named authorization, refuses
  pushes to the default branch, refuses force pushes
- **shield** refuses edits to protected paths, refuses edits to test files
  during a fix, refuses implementation before a plan exists

When one of these blocks you, it is correct. Read the message, which always
names a route forward. Do not look for a way around it.

## What Orion does not decide

A human approves the PR. Orion prepares everything up to that point and
stops. Inside a sandboxed workspace with auto-merge explicitly enabled, dev
branches may merge on green, and only there, and only once the eval suite
holds real cases.

## Resuming

`orion status <id>` shows the stage, the breaker state, and whether the run
is waiting on a quota reset. Work paused on quota records a resume time;
nothing sleeps on it.
