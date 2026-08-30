# 0012: `orion new` creates the tracker project AND keeps provisioning the workspace

- Status: Accepted
- Date: 2026-08-30
- Ticket: OR-148

## Context

OR-148 turns `orion new` into the interactive front half of the flow:
elaborate a flat-text idea, finalise the project name in a synchronous
Socratic exchange, and create the Jira project with the elaborated
description. Decision 0006 already names that Jira project as the handoff
artifact the plan phase consumes.

That left one question open. If the plan phase provisions the workspace as
its first action, `new` could be purely about the idea and the tracker: no
local artifacts at all, with the elaborated idea living only in the Jira
description until `plan` runs. That is the cleaner division on paper, and
both readings were defensible.

## Decision

`orion new` creates the tracker project **and** keeps provisioning the
workspace. The Jira project remains the handoff artifact; the workspace is
what makes the exchange survive the tracker being absent.

## Consequences

- The elaborated description is durable without Jira. Orion's standing rule
  is that external systems are detected at runtime and never required, and
  `tracker.NewJiraFromEnv` failing is an ordinary state, not an error. A
  `new` that wrote nothing locally would hold the only interactive exchange
  in the system and then, on an unconfigured machine, have nowhere to put
  the answer — the conversation would happen and produce nothing.
- The description is written to the workspace's `task.json` as well as to
  the Jira project, so the two are not a single point of failure for the
  same text.
- Nothing downstream is stranded. `orion provision`, `orion run` and
  `orion status` are all keyed on a workspace id, and this keeps the only
  command that mints one.
- The cost is that removing workspace provisioning from `new` later, if the
  plan phase does take it over, is a change this decision has to be revised
  for. That is a small, reversible edit; shipping the opposite and being
  wrong would leave the greenfield path unusable in the meantime.
- `orion provision` now refuses to create a second tracker project for a
  workspace that already has a binding. Creating one would resolve to a
  fresh key rather than fail, and a Jira project cannot be deleted without
  admin rights, so the duplicate would be permanent and silent.
