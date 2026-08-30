# 0013: `orion new` creates the tracker project and no workspace

- Status: Accepted
- Date: 2026-08-30
- Ticket: OR-148

## Context

[0006](0006-new-and-plan-are-sequential-phases.md) made `orion new` and the
plan stage sequential phases with the tracker project as the handoff artifact.
`orion new` nevertheless still provisioned a workspace and pointed at the
intent stage, because it predated that split.

OR-149 then shipped `orion plan <KEY>`, which provisions the workspace as its
FIRST action, named by the canonical slug derived from the project's name
([0009](0009-canonical-slug-one-name.md)). OR-148 left one question open
before building the front half: does `orion new` still provision a workspace,
or only the tracker project?

## Decision

`orion new` creates the tracker project and nothing else. It provisions no
workspace and leaves nothing on disk.

The decision is not a preference between two defensible options; the other one
is already ruled out. [0012](0012-one-workspace-per-tracker-project.md) says a
tracker project gets exactly ONE workspace, and gives the reason: two
workspaces claiming one project leave every later stage asking which of them
owns it and getting two answers. A `new` that provisioned a workspace, followed
by the `orion plan` that provisions another under the canonical slug, produces
exactly that pair — the first one orphaned, bound to nothing, and named with a
random suffix that no longer matches the tracker or the repo.

## Consequences

- `orion new`'s output is a named project carrying a real description, and its
  next step is `orion plan <KEY>`. The elaborated idea lives only in that
  description until `plan` runs. This is not a gap: `orion plan` already reads
  `Project.Description` back as the idea it designs from, and re-deriving it
  from the original flat text would be the second source of truth 0006's
  handoff exists to remove.
- `Tracker.CreateProject` takes a description. It previously hardcoded
  "Provisioned by Orion.", which is what `orion plan` was reading as the
  statement of the work.
- The interrogation in `new` is SYNCHRONOUS, and this is the only place in the
  system where that is right. `internal/discovery` exists because the intent
  stage runs through `claude -p` with nobody to ask; a human is present here by
  definition, so the questions are answered now rather than written into a file
  for a later stage to be blocked by.
- Creation routes through `adopt.RemotePlan.Describe()`'s existing
  describe-then-confirm gate, the same one `orion init` uses, because that is
  the only place the sentence "a Jira project cannot be deleted without admin
  rights" is said before somebody agrees. A second confirmation pattern beside
  it would be one more prompt to train people to skim.
- The project Slack channel moves with the workspace, into `orion plan`.
- `--from`, `--template` and `--container` shaped a workspace and are refused
  by `orion new` rather than silently ignored. Cloning an existing repository
  into a workspace (docs/USAGE.md §4a) has no front door until `orion plan`
  grows one; adopting Orion inside a checkout (`orion init`) is unaffected.
