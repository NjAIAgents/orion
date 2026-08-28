# 0005: Agent roster is global, not per-repo

- Status: Accepted — shipped
- Date: 2026-08-28 (recorded); shipped OR-132
- Ticket: OR-132

## Context

The agent roster — name, model and effort per actor (implementer, QA,
devops engineer, PR describer, ...) — originally lived as an `agents`
block inside each repository's own `orion.json`, meaning it had to be
re-declared every time Orion was adopted into a new repo, and could drift
between repos for no reason tied to the repo itself.

## Decision

The roster moved to a single global file, `~/.orion/agents.json`, shared
by every project. `orion config agents` writes this file directly;
per-project `agents` blocks in `orion.json` are no longer read.

## Consequences

- Who the implementer is and what model/effort it runs on is an operator
  preference, not a per-checkout one — adopting Orion into a new repo does
  not require restating the roster.
- Existing per-project `agents` blocks in `orion.json` can be deleted; they
  are inert.
- Any future actor-level setting (model, effort, or similar) defaults to
  living in this global file unless there is a specific reason the setting
  must vary per repository.
