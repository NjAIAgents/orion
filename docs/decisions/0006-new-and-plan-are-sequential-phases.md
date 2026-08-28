# 0006: `orion new` and the plan stage are sequential phases, not two front doors

- Status: Accepted
- Date: 2026-08-28

## Context

`orion new "<idea>"` starts an interactive conversation (discovery) before
anything is derived, because the intent, spec and plan stages run through
`claude -p` and cannot ask the user anything once started — one ambiguous
premise there propagates into spec, plan, scaffold and the tracker tree,
costing far more to unwind than the conversation would have cost to have.
There was a question of whether interactive discovery and the async `plan`
stage should instead be two independent entry points a user picks between.

## Decision

`orion new` and the `plan` stage are sequential phases of one flow, not two
front doors, with the Jira project (created at `orion provision`) as the
handoff artifact between them.

## Consequences

- Interactive interrogation belongs in `new`, because a human is present
  at that point to answer it.
- The `plan` stage (and the stages after it — scaffold, decompose) keeps
  the async discovery gate instead: unresolved **Open questions** in the
  intent file block those stages until answered in the file itself, because
  by the time `plan` runs no human is assumed present to prompt.
- A user cannot skip straight to `plan` expecting an interactive prompt;
  ambiguity there surfaces as a blocking open question in the artifact, not
  a conversation.
